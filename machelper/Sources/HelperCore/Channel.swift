import CryptoKit
import Foundation

public enum DropReason: Equatable, Sendable {
    case malformed
    case handshakeRateLimit
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
    }

    private let staticKey: Curve25519.KeyAgreement.PrivateKey
    private let pairedKeys: () -> [Data]
    private let environment: (PeerAddress?, Data) -> GateInputs
    private let clock: ChannelClock
    private var handshakeLimiter = RateLimiter(perSecond: ChannelLimits.handshakesPerSecond)
    private var inputLimiter = RateLimiter(perSecond: ChannelLimits.inputPacketsPerSecond)
    private var lastTimestamp: [Data: Data] = [:]
    private var active: Live?
    private var pending: Live?
    private var lastSource: PeerAddress?
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
        ephemeralSource: @escaping () -> Curve25519.KeyAgreement.PrivateKey = { .init() }
    ) {
        self.staticKey = staticKey; self.pairedKeys = pairedKeys
        self.environment = environment; self.clock = clock
        self.ephemeralSource = ephemeralSource
    }

    public var activePeerKey: Data? { active?.peerKey }

    private func isPaired(_ key: Data) -> Bool {
        // Visit every entry: no early exit on a match.
        var found = false
        for k in pairedKeys() where constantTimeEqual(k, key) { found = true }
        return found
    }

    /// Ends the active session immediately (kill switch, key removed, toggle off).
    public func endSession(reason: EndReason = .bye) -> ChannelEvent? {
        guard active != nil else { return nil }
        active = nil
        pending = nil
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
        case .ikInitiation: return handleInitiation(body, from: source)
        case .transport: return handleTransport(datagram, from: source)
        default:  // responses and XX belong to other roles; never answered here
            return ChannelResult(reply: nil, events: [.dropped(.malformed)])
        }
    }

    // IK message 1: e(32) + enc(s)(32+16) + enc(timestamp)(12+16) = 108 bytes.
    static let initiationBodySize = 32 + 48 + TAI64N.size + aeadTagLength

    private func handleInitiation(_ body: Data, from source: PeerAddress?) -> ChannelResult {
        let now = clock.monotonic()
        guard handshakeLimiter.allow(now: now) else {
            return ChannelResult(reply: nil, events: [.dropped(.handshakeRateLimit)])
        }
        guard body.count == HelperChannel.initiationBodySize else {
            return ChannelResult(reply: nil, events: [.dropped(.malformed)])
        }
        do {
            let hs = try HandshakeState(
                pattern: .ik, initiator: false, staticKey: staticKey,
                remoteStatic: nil, prologue: Data(), ephemeralSource: ephemeralSource)
            let ts = try hs.readMessage(body)
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
            lastTimestamp[peerKey] = ts
            pending = Live(
                session: TransportSession(keys: keys, now: now), peerKey: peerKey, lastRx: now)
            lastSource = source
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
            pending = nil
            events.append(.sessionStarted(peerKey: p.peerKey))
        } else if let a = active {
            live = a
        } else {
            return ChannelResult(reply: nil, events: [.dropped(.noSession)])
        }
        live.lastRx = now
        lastSource = source ?? lastSource

        guard let packet = try? InputPacket.decode(pt) else {
            active = live
            return ChannelResult(reply: nil, events: events + [.dropped(.malformed)])
        }

        if packet.type == .bye {
            active = nil
            return ChannelResult(reply: nil, events: events + [.ended(.bye)])
        }

        var inputs = environment(lastSource, live.peerKey)
        inputs.peerPaired = isPaired(live.peerKey)
        inputs.rateLimited = packet.type == .input && !inputLimiter.allow(now: now)
        let decision = Gate.evaluate(inputs)
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
