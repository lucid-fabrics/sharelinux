import CryptoKit
import Foundation
import os

public enum DropReason: Equatable, Sendable {
    case malformed
    case handshakeRateLimit
    case badMac
    case handshakeFailed
    case unpaired
    case staleTimestamp
    case noSession
    case replay
    case authenticationFailed
    case expired
}

public enum ChannelEvent: Equatable, Sendable {
    /// A new session took over after its first authenticated packet.
    case sessionStarted(peerKey: Data)
    /// Gate accepted this (clamped) input.
    case input(InputPacket)
    case keepalive
    case denied(HelperState)
    case ended(EndReason)
    case dropped(DropReason)
    /// Handshake answered; the session only goes live on its first authenticated packet.
    case handshakeAnswered
}

public enum EndReason: Equatable, Sendable {
    case bye, silence, expired, replaced, unpaired
    /// The runtime saw the Screen Sharing connection from this peer go away (design 3.3 rule 3).
    case screenSharingEnded
    /// Local decision: kill switch, toggle off, sleep, key removed.
    case local
}

public struct ChannelResult: Equatable, Sendable {
    public var reply: Data?
    public var events: [ChannelEvent]
}

/// Per-source handshake limiter with bounded memory: at most
/// `ChannelLimits.maxTrackedSources` buckets. When the table is full, idle
/// buckets are forgotten first (a full bucket carries no information); if it
/// is still full, the least recently seen source that never authenticated
/// is evicted. Sources that recently authenticated a session are pinned and
/// are never evicted, so a sender who spoofs hundreds of source addresses
/// (it needs the Mac's public key to get past mac1) cannot push a real device
/// out of its own bucket. At most `maxPinnedSources` are pinned, each for
/// `sourcePinLifetime`, which keeps room for newcomers (the cap is far below
/// the table size) and keeps memory bounded.
struct SourceRateLimiter {
    private struct Entry {
        var bucket: RateLimiter
        var lastSeen: Double
    }
    private var table: [PeerAddress?: Entry] = [:]
    private var pinned: [PeerAddress?: Double] = [:]
    private var lastPrune = -Double.infinity
    var trackedCount: Int { table.count }
    var pinnedCount: Int { pinned.count }

    private static func bucket() -> RateLimiter {
        RateLimiter(
            perSecond: ChannelLimits.handshakesPerSourcePerSecond,
            burst: ChannelLimits.handshakesPerSourceBurst)
    }

    /// Call when a session from this source authenticated its first packet.
    mutating func markAuthenticated(_ source: PeerAddress?, now: Double) {
        expirePins(now: now)
        pinned[source] = now
        while pinned.count > ChannelLimits.maxPinnedSources,
            let oldest = pinned.min(by: { $0.value < $1.value })?.key
        {
            pinned[oldest] = nil
        }
    }

    func isPinned(_ source: PeerAddress?) -> Bool { pinned[source] != nil }

    private mutating func expirePins(now: Double) {
        pinned = pinned.filter { now >= $0.value && now - $0.value < ChannelLimits.sourcePinLifetime }
    }

    mutating func allow(_ source: PeerAddress?, now: Double) -> Bool {
        if table[source] == nil, table.count >= ChannelLimits.maxTrackedSources {
            expirePins(now: now)
            if now - lastPrune >= 1 || now < lastPrune {
                lastPrune = now
                table = table.filter { pinned[$0.key] != nil || !$0.value.bucket.isFull(now: now) }
            }
            if table.count >= ChannelLimits.maxTrackedSources {
                // Pins are capped far below the table size, so a victim exists.
                let victim = table.filter { pinned[$0.key] == nil }.min { $0.value.lastSeen < $1.value.lastSeen }
                if let v = victim { table[v.key] = nil }
            }
        }
        var e = table[source] ?? Entry(bucket: SourceRateLimiter.bucket(), lastSeen: now)
        let ok = e.bucket.allow(now: now)
        e.lastSeen = max(e.lastSeen, now)
        table[source] = e
        return ok
    }
}

public struct ChannelClock {
    /// Monotonic seconds (rate limits, timeouts).
    public var monotonic: () -> Double
    /// Wall clock (handshake timestamps only).
    public var wall: () -> Date
    public init(monotonic: @escaping () -> Double, wall: @escaping () -> Date) {
        self.monotonic = monotonic; self.wall = wall
    }
}

/// The Mac-side endpoint for IK sessions, as a pure state machine over datagrams.
/// No sockets, no AppKit: the app feeds datagrams in and sends `reply` out.
///
/// Rules: unauthenticated or unpaired traffic never gets a reply. A new session
/// (fresh handshake from a paired key) stays pending and replaces the active one
/// only after its first authenticated transport packet, so a replayed or stolen
/// handshake message cannot kick a live session.
public final class HelperChannel {
    private struct Live {
        let session: TransportSession
        let peerKey: Data
        var lastRx: Double
        var lastStatus: Double = -.infinity
        /// Where the last authenticated packet of this session came from. nil
        /// until the session authenticated a packet; never taken from an
        /// initiation, which does not prove anything about later datagrams.
        var source: PeerAddress?
    }

    private let staticKey: Curve25519.KeyAgreement.PrivateKey
    private let pairedKeys: () -> [Data]
    private let environment: (PeerAddress?, Data) -> GateInputs
    private let clock: ChannelClock
    private var handshakeLimiter = RateLimiter(perSecond: ChannelLimits.handshakesPerSecond)
    private var sourceLimiter = SourceRateLimiter()
    private var inputLimiter = RateLimiter(perSecond: ChannelLimits.inputPacketsPerSecond)
    private var motionBudget = RateLimiter(perSecond: ChannelLimits.motionUnitsPerSecond)
    private var scrollBudget = RateLimiter(perSecond: ChannelLimits.scrollUnitsPerSecond)
    private var lastTimestamp: [Data: Data] = [:]
    private let timestamps: TimestampStore
    /// False when the timestamp store could not be read and could not be
    /// recovered either: handshakes are refused rather than risk a replay.
    public private(set) var timestampsTrusted = true
    /// True when the store was corrupt at start and was moved aside. Until the
    /// wall clock passes the recovery time no initiation is newer than the
    /// floor, so nothing captured earlier can be replayed.
    public private(set) var timestampStoreRecovered = false
    /// True when there was no store at all (first run): same floor rule.
    public private(set) var timestampStoreFirstRun = false
    /// Not a real peer: the entry that holds the recovery floor. Persisted like
    /// any timestamp so a second restart cannot lose it.
    static let timestampFloorKey = Data(count: 32)
    private let mac1Key: SymmetricKey
    private var active: Live?
    private var pending: Live?
    private let ephemeralSource: () -> Curve25519.KeyAgreement.PrivateKey

    /// - Parameters:
    ///   - pairedKeys: current paired static public keys.
    ///   - environment: builds gate inputs for (udp source, peer key). `peerPaired`
    ///     and `rateLimited` are overwritten by the channel, which knows them better.
    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey,
        pairedKeys: @escaping () -> [Data],
        environment: @escaping (PeerAddress?, Data) -> GateInputs,
        clock: ChannelClock,
        timestamps: TimestampStore = MemoryTimestampStore(),
        ephemeralSource: @escaping () -> Curve25519.KeyAgreement.PrivateKey = { .init() }
    ) {
        self.staticKey = staticKey; self.pairedKeys = pairedKeys
        self.environment = environment; self.clock = clock
        self.ephemeralSource = ephemeralSource
        self.timestamps = timestamps
        mac1Key = Mac1.key(macPublic: staticKey.publicKey.rawRepresentation)
        do { lastTimestamp = try timestamps.load() } catch TimestampStoreError.missing {
            startTimestampFloor(firstRun: true)
        } catch { recoverTimestampStore() }
    }

    public var activePeerKey: Data? { active?.peerKey }

    /// The store is unreadable. Failing closed forever would need a human to
    /// find the file, so: move it aside (kept for inspection), start empty, and
    /// record "now" as a floor that every initiation must beat. A captured
    /// initiation is older than the floor, so it stays refused. If the file
    /// cannot be moved or the floor cannot be stored, handshakes stay refused.
    private func recoverTimestampStore() {
        timestampsTrusted = false
        do { try timestamps.moveCorruptAside() } catch {
            Logger(subsystem: "io.github.sharelinux.mousehelper", category: "channel").error(
                "handshake timestamp store is unreadable and could not be recovered: handshakes refused")
            return
        }
        startTimestampFloor(firstRun: false)
    }

    /// Stores "now" as the floor (first run, or after recovery). Until the
    /// floor is stored handshakes stay refused. A first run is not an error, so
    /// it is logged as plain information.
    private func startTimestampFloor(firstRun: Bool) {
        timestampsTrusted = false
        do {
            let floor = TAI64N.encode(clock.wall())
            try timestamps.save(peer: HelperChannel.timestampFloorKey, timestamp: floor)
            lastTimestamp = [HelperChannel.timestampFloorKey: floor]
            timestampsTrusted = true
            let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "channel")
            if firstRun {
                timestampStoreFirstRun = true
                log.info("no handshake timestamp store yet: created one with a floor of now")
            } else {
                timestampStoreRecovered = true
                log.warning("handshake timestamp store was unreadable: moved aside and restarted with a fresh floor")
            }
        } catch {
            Logger(subsystem: "io.github.sharelinux.mousehelper", category: "channel").error(
                "handshake timestamp floor could not be stored: handshakes refused")
        }
    }

    private func isPaired(_ key: Data) -> Bool {
        // Visit every entry: no early exit on a match.
        var found = false
        for k in pairedKeys() where constantTimeEqual(k, key) { found = true }
        return found
    }

    /// Ends the active session immediately (kill switch, key removed, toggle off).
    /// A session that has answered a handshake but not yet authenticated a
    /// packet is dropped too, whether or not another one is active.
    public func endSession(reason: EndReason = .bye) -> ChannelEvent? {
        pending = nil
        guard active != nil else { return nil }
        active = nil
        return .ended(reason)
    }

    /// A sealed status for the live session, sent without being asked: the helper stopped on
    /// purpose (switch off, Stop now) and says so before the session ends, so the Linux side
    /// falls back at once instead of waiting for silence. nil without a live session.
    public func unsolicitedStatus(_ state: HelperState) -> Data? {
        guard let a = active else { return nil }
        return try? a.session.seal(StatusPacket(state: state).encode(), now: clock.monotonic())
    }

    /// Call at least every 100 ms. Ends a session that has been silent too long.
    public func tick() -> ChannelEvent? {
        let now = clock.monotonic()
        guard let a = active else { return nil }
        if now < a.lastRx || now - a.lastRx >= ChannelLimits.silenceTimeout {
            active = nil
            return .ended(.silence)
        }
        if a.session.expired(now: now) {
            active = nil
            return .ended(.expired)
        }
        return nil
    }

    public func receive(_ datagram: Data, from source: PeerAddress?) -> ChannelResult {
        guard let (kind, body) = parseDatagram(datagram) else {
            return ChannelResult(reply: nil, events: [.dropped(.malformed)])
        }
        switch kind {
        case .ikInitiation: return handleInitiation(datagram, body, from: source)
        case .transport: return handleTransport(datagram, from: source)
        default:  // responses and XX belong to other roles; never answered here
            return ChannelResult(reply: nil, events: [.dropped(.malformed)])
        }
    }

    // IK message 1: e(32) + enc(s)(32+16) + enc(timestamp)(12+16) = 108 bytes,
    // followed by mac1(16).
    static let noiseInitiationSize = 32 + 48 + TAI64N.size + aeadTagLength
    static let initiationBodySize = noiseInitiationSize + Mac1.size

    private func handleInitiation(_ datagram: Data, _ body: Data, from source: PeerAddress?) -> ChannelResult {
        let now = clock.monotonic()
        // Cheapest checks first, and none of them may spend the handshake
        // budget: wrong size, then the keyed mac1 (one HMAC, constant time).
        // Only a sender who knows the Mac's public key gets past it.
        guard body.count == HelperChannel.initiationBodySize else {
            return ChannelResult(reply: nil, events: [.dropped(.malformed)])
        }
        guard Mac1.verify(datagram, key: mac1Key) else {
            return ChannelResult(reply: nil, events: [.dropped(.badMac)])
        }
        // Per source first, so one address cannot drain the global budget.
        guard sourceLimiter.allow(source, now: now), handshakeLimiter.allow(now: now) else {
            return ChannelResult(reply: nil, events: [.dropped(.handshakeRateLimit)])
        }
        guard timestampsTrusted else {
            return ChannelResult(reply: nil, events: [.dropped(.handshakeFailed)])
        }
        do {
            let hs = try HandshakeState(
                pattern: .ik, initiator: false, staticKey: staticKey,
                remoteStatic: nil, prologue: nil, ephemeralSource: ephemeralSource)
            let ts = try hs.readMessage(Data(body.prefix(HelperChannel.noiseInitiationSize)))
            guard let peerKey = hs.remoteStatic else { throw NoiseError.invalidState }
            guard isPaired(peerKey) else {
                return ChannelResult(reply: nil, events: [.dropped(.unpaired)])
            }
            guard let seconds = TAI64N.decode(ts),
                abs(seconds - clock.wall().timeIntervalSince1970) <= ChannelLimits.handshakeClockSkew
            else { return ChannelResult(reply: nil, events: [.dropped(.staleTimestamp)]) }
            if let last = lastTimestamp[peerKey], !TAI64N.isNewer(ts, than: last) {
                return ChannelResult(reply: nil, events: [.dropped(.staleTimestamp)])
            }
            if let floor = lastTimestamp[HelperChannel.timestampFloorKey], !TAI64N.isNewer(ts, than: floor) {
                return ChannelResult(reply: nil, events: [.dropped(.staleTimestamp)])
            }
            let response = try hs.writeMessage(payload: Data())
            let keys = try hs.split()
            // Durable before the answer leaves; if it cannot be stored the
            // replay protection would be a lie, so the handshake is refused.
            try timestamps.save(peer: peerKey, timestamp: ts)
            lastTimestamp[peerKey] = ts
            pending = Live(
                session: TransportSession(keys: keys, now: now), peerKey: peerKey, lastRx: now)
            return ChannelResult(
                reply: Data([DatagramKind.ikResponse.rawValue]) + response,
                events: [.handshakeAnswered])
        } catch {
            return ChannelResult(reply: nil, events: [.dropped(.handshakeFailed)])
        }
    }

    private func handleTransport(_ datagram: Data, from source: PeerAddress?) -> ChannelResult {
        let now = clock.monotonic()
        var events: [ChannelEvent] = []

        // Try the pending session first only if it exists and the active one fails.
        var plaintext: Data?
        var fromPending = false
        var failure: SessionError?
        if let a = active {
            do {
                plaintext = try a.session.open(datagram, maxPlaintext: InputPacket.size, now: now)
            } catch let e as SessionError { failure = e } catch { failure = .malformed }
        }
        if plaintext == nil, let p = pending {
            do {
                plaintext = try p.session.open(datagram, maxPlaintext: InputPacket.size, now: now)
                fromPending = true
            } catch let e as SessionError { failure = failure ?? e } catch { failure = failure ?? .malformed }
        }
        guard let pt = plaintext else {
            let reason: DropReason
            switch failure {
            case .none: reason = .noSession
            case .some(.replayOrOld): reason = .replay
            case .some(.authenticationFailed): reason = .authenticationFailed
            case .some(.expired): reason = .expired
            case .some(.malformed): reason = .malformed
            }
            return ChannelResult(reply: nil, events: [.dropped(reason)])
        }

        // Authenticated from here on.
        var live: Live
        if fromPending, let p = pending {
            if active != nil { events.append(.ended(.replaced)) }
            live = p
            live.source = source
            pending = nil
            // A paired device proved itself: keep its handshake bucket safe
            // from address-spoofing floods.
            sourceLimiter.markAuthenticated(source, now: now)
            events.append(.sessionStarted(peerKey: p.peerKey))
        } else if let a = active {
            live = a
        } else {
            return ChannelResult(reply: nil, events: [.dropped(.noSession)])
        }
        live.lastRx = now
        // Only an authenticated packet may move the session's address.
        if let source { live.source = source }

        guard let packet = try? InputPacket.decode(pt) else {
            active = live
            return ChannelResult(reply: nil, events: events + [.dropped(.malformed)])
        }

        if packet.type == .bye {
            active = nil
            return ChannelResult(reply: nil, events: events + [.ended(.bye)])
        }

        var inputs = environment(live.source, live.peerKey)
        inputs.peerPaired = isPaired(live.peerKey)
        if packet.type == .input {
            // All three buckets are charged; any one running dry limits the packet.
            let clamped = packet.clamped()
            let cost = Double(abs(Int(clamped.dx)) + abs(Int(clamped.dy)))
            let scrollCost = Double(abs(Int(clamped.sx)) + abs(Int(clamped.sy)))
            let packetsOK = inputLimiter.allow(now: now)
            let motionOK = motionBudget.allow(now: now, cost: cost)
            let scrollOK = scrollBudget.allow(now: now, cost: scrollCost)
            inputs.rateLimited = !(packetsOK && motionOK && scrollOK)
        } else {
            inputs.rateLimited = false
        }
        let decision = Gate.evaluate(inputs, now: now)
        var reply: Data?

        func status(_ s: HelperState) {
            guard now - live.lastStatus >= ChannelLimits.statusMinInterval else { return }
            if let sealed = try? live.session.seal(StatusPacket(state: s).encode(), now: now) {
                reply = sealed
                live.lastStatus = now
            }
        }

        switch decision {
        case .dropSilently:
            // Key was removed mid-session: end it, say nothing.
            active = nil
            return ChannelResult(reply: nil, events: events + [.ended(.unpaired), .dropped(.unpaired)])
        case .deny(let s):
            status(s)
            events.append(.denied(s))
        case .accept:
            if packet.type == .keepalive { status(.active); events.append(.keepalive) } else {
                events.append(.input(packet.clamped()))
            }
        }
        active = live
        return ChannelResult(reply: reply, events: events)
    }
}
