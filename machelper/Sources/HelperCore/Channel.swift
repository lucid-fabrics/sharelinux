import CryptoKit
import Foundation

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

public enum EndReason: Equatable, Sendable { case bye, silence, expired, replaced, unpaired }

public struct ChannelResult: Equatable, Sendable {
    public var reply: Data?
    public var events: [ChannelEvent]
}

/// Per-source handshake limiter with bounded memory: at most
/// `ChannelLimits.maxTrackedSources` buckets; once full, idle buckets are
/// forgotten (a full bucket carries no information) and sources that still do
/// not fit share one bucket, so a flood of spoofed addresses can neither grow
/// the table nor starve the global budget.
struct SourceRateLimiter {
    private var table: [PeerAddress?: RateLimiter] = [:]
    private var overflow = SourceRateLimiter.bucket()
    private var lastPrune = -Double.infinity
    var trackedCount: Int { table.count }

    private static func bucket() -> RateLimiter {
        RateLimiter(
            perSecond: ChannelLimits.handshakesPerSourcePerSecond,
            burst: ChannelLimits.handshakesPerSourceBurst)
    }

    mutating func allow(_ source: PeerAddress?, now: Double) -> Bool {
        if table[source] == nil, table.count >= ChannelLimits.maxTrackedSources, now - lastPrune >= 1 || now < lastPrune {
            lastPrune = now
            table = table.filter { !$0.value.isFull(now: now) }
        }
        if table[source] == nil, table.count >= ChannelLimits.maxTrackedSources {
            return overflow.allow(now: now)
        }
        var b = table[source] ?? SourceRateLimiter.bucket()
        let ok = b.allow(now: now)
        table[source] = b
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
    private var lastTimestamp: [Data: Data] = [:]
    private let timestamps: TimestampStore
    /// False when the timestamp store could not be read: handshakes are refused
    /// rather than risk accepting a replay.
    public private(set) var timestampsTrusted = true
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
        if let loaded = try? timestamps.load() { lastTimestamp = loaded } else { timestampsTrusted = false }
    }

    public var activePeerKey: Data? { active?.peerKey }

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
            // Both buckets are charged; either one running dry limits the packet.
            let motion = packet.clamped()
            let cost = Double(abs(Int(motion.dx)) + abs(Int(motion.dy)))
            let packetsOK = inputLimiter.allow(now: now)
            let motionOK = motionBudget.allow(now: now, cost: cost)
            inputs.rateLimited = !(packetsOK && motionOK)
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
