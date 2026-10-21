import CryptoKit
import XCTest

@testable import HelperCore

final class ChannelTests: XCTestCase {
    final class Env {
        var mono = 1000.0
        var wall = Date(timeIntervalSince1970: 1_800_000_000)
        var toggle = true
        var locked: Bool? = false
        var console: UInt32? = 501
        var grant = true
        var sharing: Set<PeerAddress> = [addr]
        var paired: [Data] = []
    }
    static let addr = PeerAddress(bytes: [192, 0, 2, 1])!

    let macKey = PrivKey()
    let linuxKey = PrivKey()
    var env = Env()
    var channel: HelperChannel!

    override func setUp() {
        env = Env()
        env.paired = [linuxKey.publicKey.rawRepresentation]
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired },
            environment: { peer, _ in
                GateInputs(
                    toggleOn: e.toggle, peerPaired: false, udpPeer: peer, screenSharingPeers: e.sharing,
                    consoleUID: e.console, ownUID: 501, screenLocked: e.locked,
                    accessibilityGranted: e.grant, rateLimited: false)
            },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }))
    }

    /// Initiator side, using only the public building blocks.
    struct Client {
        var hs: HandshakeState
        var datagram: Data
        var session: TransportSession?
    }

    func initiate(key: PrivKey? = nil, at wall: Date? = nil) throws -> Client {
        let hs = try HandshakeState(
            pattern: .ik, initiator: true, staticKey: key ?? linuxKey,
            remoteStatic: macKey.publicKey.rawRepresentation)
        let m = try hs.writeMessage(payload: TAI64N.encode(wall ?? env.wall))
        return Client(hs: hs, datagram: Data([DatagramKind.ikInitiation.rawValue]) + m, session: nil)
    }

    func finish(_ c: Client, reply: Data) throws -> TransportSession {
        XCTAssertEqual(reply[0], DatagramKind.ikResponse.rawValue)
        _ = try c.hs.readMessage(reply.dropFirst())
        return TransportSession(keys: try c.hs.split(), now: env.mono)
    }

    func connect() throws -> TransportSession {
        let c = try initiate()
        let r = channel.receive(c.datagram, from: Self.addr)
        XCTAssertEqual(r.events, [.handshakeAnswered])
        let s = try finish(c, reply: try XCTUnwrap(r.reply))
        return s
    }

    func send(_ s: TransportSession, _ p: InputPacket) throws -> ChannelResult {
        channel.receive(try s.seal(p.encode(), now: env.mono), from: Self.addr)
    }

    func testFullFlowInputKeepaliveAndStatus() throws {
        let s = try connect()
        let first = try send(s, InputPacket(type: .input, buttons: .left, dx: 5, dy: -5))
        XCTAssertEqual(
            first.events,
            [.sessionStarted(peerKey: linuxKey.publicKey.rawRepresentation), .input(InputPacket(type: .input, buttons: .left, dx: 5, dy: -5))])
        XCTAssertNil(first.reply)
        env.mono += 0.25
        let ka = try send(s, InputPacket(type: .keepalive))
        XCTAssertEqual(ka.events, [.keepalive])
        let status = try s.open(try XCTUnwrap(ka.reply), maxPlaintext: 2, now: env.mono)
        XCTAssertEqual(try StatusPacket.decode(status), StatusPacket(state: .active))
    }

    func testUnpairedKeyGetsNoReply() throws {
        let stranger = PrivKey()
        let r = channel.receive(try initiate(key: stranger).datagram, from: Self.addr)
        XCTAssertNil(r.reply)
        XCTAssertEqual(r.events, [.dropped(.unpaired)])
    }

    func testReplayedInitiationIsRefused() throws {
        let c = try initiate()
        XCTAssertNotNil(channel.receive(c.datagram, from: Self.addr).reply)
        env.mono += 1
        let again = channel.receive(c.datagram, from: Self.addr)
        XCTAssertNil(again.reply)
        XCTAssertEqual(again.events, [.dropped(.staleTimestamp)])
    }

    func testOlderTimestampAndClockSkewRefused() throws {
        _ = try connect()
        env.mono += 1
        let old = try initiate(at: env.wall.addingTimeInterval(-10))
        XCTAssertEqual(channel.receive(old.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        let far = try initiate(at: env.wall.addingTimeInterval(ChannelLimits.handshakeClockSkew + 5))
        XCTAssertEqual(channel.receive(far.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        let ok = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertNotNil(channel.receive(ok.datagram, from: Self.addr).reply)
    }

    func testReplayedTransportPacketDropped() throws {
        let s = try connect()
        let dg = try s.seal(InputPacket(type: .input, dx: 1).encode(), now: env.mono)
        XCTAssertEqual(channel.receive(dg, from: Self.addr).events.last, .input(InputPacket(type: .input, dx: 1)))
        XCTAssertEqual(channel.receive(dg, from: Self.addr).events, [.dropped(.replay)])
    }

    func testTamperedTransportPacketDroppedEverywhere() throws {
        let s = try connect()
        _ = try send(s, InputPacket(type: .keepalive))
        let dg = try s.seal(InputPacket(type: .input, dx: 1).encode(), now: env.mono)
        for i in 0..<dg.count {
            var t = dg
            t[i] ^= 0x01
            let r = channel.receive(t, from: Self.addr)
            XCTAssertNil(r.reply)
            XCTAssertFalse(r.events.contains { if case .input = $0 { return true } else { return false } }, "byte \(i)")
        }
        // The untouched one still works afterwards (failed packets did not advance the window).
        XCTAssertEqual(channel.receive(dg, from: Self.addr).events, [.input(InputPacket(type: .input, dx: 1))])
    }

    func testNoSessionTransportIsDropped() throws {
        let s = try connect()
        let other = HelperChannel(
            staticKey: PrivKey(), pairedKeys: { [] },
            environment: { _, _ in fatalError() },
            clock: ChannelClock(monotonic: { 0 }, wall: { Date() }))
        let r = other.receive(try s.seal(InputPacket(type: .input).encode(), now: env.mono), from: Self.addr)
        XCTAssertEqual(r.events, [.dropped(.noSession)])
    }

    func testPendingSessionDoesNotReplaceActiveUntilFirstPacket() throws {
        let s1 = try connect()
        _ = try send(s1, InputPacket(type: .keepalive))
        env.mono += 0.1
        let c2 = try initiate(at: env.wall.addingTimeInterval(1))
        let r2 = channel.receive(c2.datagram, from: Self.addr)
        XCTAssertEqual(r2.events, [.handshakeAnswered])
        // The old session keeps working even though a handshake was answered.
        XCTAssertEqual(try send(s1, InputPacket(type: .keepalive)).events, [.keepalive])
        let s2 = try finish(c2, reply: try XCTUnwrap(r2.reply))
        let r = try send(s2, InputPacket(type: .keepalive))
        XCTAssertEqual(r.events.first, .ended(.replaced))
        // Old keys are dead now.
        XCTAssertEqual(try send(s1, InputPacket(type: .keepalive)).events, [.dropped(.authenticationFailed)])
    }

    func testSilenceEndsSession() throws {
        let s = try connect()
        _ = try send(s, InputPacket(type: .keepalive))
        env.mono += 0.9
        XCTAssertNil(channel.tick())
        env.mono += 0.2
        XCTAssertEqual(channel.tick(), .ended(.silence))
        XCTAssertEqual(try send(s, InputPacket(type: .keepalive)).events, [.dropped(.noSession)])
    }

    func testByeEndsSession() throws {
        let s = try connect()
        _ = try send(s, InputPacket(type: .keepalive))
        XCTAssertEqual(try send(s, InputPacket(type: .bye)).events, [.ended(.bye)])
        XCTAssertNil(channel.activePeerKey)
    }

    func testGateDenialsReportStatusAndNeverDeliverInput() throws {
        let s = try connect()
        let cases: [(() -> Void, HelperState)] = [
            ({ self.env.toggle = false }, .disabled),
            ({ self.env.toggle = true; self.env.locked = true }, .locked),
            ({ self.env.locked = nil }, .locked),
            ({ self.env.locked = false; self.env.console = 502 }, .notConsole),
            ({ self.env.console = nil }, .notConsole),
            ({ self.env.console = 501; self.env.sharing = [] }, .noSession),
            ({ self.env.sharing = [Self.addr]; self.env.grant = false }, .needsGrant),
        ]
        for (mutate, want) in cases {
            mutate()
            env.mono += 0.3
            let r = try send(s, InputPacket(type: .input, dx: 9))
            XCTAssertEqual(r.events.last, .denied(want))
            let st = try StatusPacket.decode(try s.open(try XCTUnwrap(r.reply), maxPlaintext: 2, now: env.mono))
            XCTAssertEqual(st.state, want)
        }
    }

    func testStatusRepliesAreThrottled() throws {
        let s = try connect()
        env.toggle = false
        XCTAssertNotNil(try send(s, InputPacket(type: .input)).reply)
        env.mono += 0.05
        XCTAssertNil(try send(s, InputPacket(type: .input)).reply)
    }

    func testInputRateLimit() throws {
        let s = try connect()
        var delivered = 0, limited = 0
        for _ in 0..<1500 {
            let r = try send(s, InputPacket(type: .input, dx: 1))
            if r.events.contains(.input(InputPacket(type: .input, dx: 1))) { delivered += 1 }
            if r.events.contains(.denied(.rateLimited)) { limited += 1 }
        }
        XCTAssertEqual(delivered, 1000)
        XCTAssertEqual(limited, 500)
        env.mono += 1
        XCTAssertTrue(try send(s, InputPacket(type: .input, dx: 1)).events.contains(.input(InputPacket(type: .input, dx: 1))))
    }

    func testHandshakeRateLimit() throws {
        var answered = 0
        for i in 0..<20 {
            let c = try initiate(at: env.wall.addingTimeInterval(Double(i) + 1))
            if channel.receive(c.datagram, from: Self.addr).reply != nil { answered += 1 }
        }
        XCTAssertEqual(answered, 5)
    }

    func testWrongSizeInitiationsDoNotConsumeHandshakeBudget() throws {
        let good = try initiate()
        for n in 0..<50 {
            let junk = Data([DatagramKind.ikInitiation.rawValue]) + Data(count: n)
            XCTAssertEqual(channel.receive(junk, from: Self.addr).events, [.dropped(.malformed)])
        }
        XCTAssertNotNil(channel.receive(good.datagram, from: Self.addr).reply)
    }

    func testRemovingPairedKeyEndsSessionSilently() throws {
        let s = try connect()
        _ = try send(s, InputPacket(type: .keepalive))
        env.paired = []
        env.mono += 0.3
        let r = try send(s, InputPacket(type: .input, dx: 4))
        XCTAssertNil(r.reply)
        XCTAssertEqual(r.events, [.ended(.unpaired), .dropped(.unpaired)])
        XCTAssertNil(channel.activePeerKey)
    }

    func testSessionExpiresAndNeedsRekey() throws {
        let s = try connect()
        _ = try send(s, InputPacket(type: .keepalive))
        env.mono += ChannelLimits.maxSessionAge + 1
        XCTAssertEqual(channel.tick(), .ended(.silence))  // silent far longer than 1 s anyway
        XCTAssertThrowsError(try s.seal(Data([1]), now: env.mono)) { XCTAssertEqual($0 as? SessionError, .expired) }
    }

    func testMessageLimit() throws {
        let c = try initiate()
        let r = channel.receive(c.datagram, from: Self.addr)
        let s = try finish(c, reply: try XCTUnwrap(r.reply))
        for _ in 0..<5 { _ = try s.seal(Data([1]), now: env.mono) }
        // A counter at the limit is refused on receive.
        var dg = Data([DatagramKind.transport.rawValue])
        dg.appendLE64(ChannelLimits.maxMessages)
        dg += Data(count: 20 + 16)
        XCTAssertEqual(channel.receive(dg, from: Self.addr).events, [.dropped(.expired)])
    }

    func testMalformedInputPlaintextIsDroppedAfterAuth() throws {
        let s = try connect()
        let dg = try s.seal(Data(count: 19), now: env.mono)
        XCTAssertEqual(channel.receive(dg, from: Self.addr).events.last, .dropped(.malformed))
        XCTAssertEqual(try send(s, InputPacket(type: .keepalive)).events.last, .keepalive)
    }

    func testClampAppliedToDeliveredInput() throws {
        let s = try connect()
        let r = try send(s, InputPacket(type: .input, dx: .min, sx: 9999))
        XCTAssertEqual(r.events.last, .input(InputPacket(type: .input, dx: -32767, sx: 2000)))
    }
}
