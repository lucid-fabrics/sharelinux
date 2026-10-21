import CryptoKit
import XCTest

@testable import HelperCore

/// Findings of the W1 security audit, each pinned by a test written from the
/// intended behaviour (extension of ChannelTests to share its harness).
extension ChannelTests {
    static func source(_ n: UInt8) -> PeerAddress { PeerAddress(bytes: [10, 0, 0, n])! }

    // MARK: handshake DoS (mac1, per-source limits)

    func testJunkWithoutValidMac1NeverSpendsTheHandshakeBudget() throws {
        let good = try initiate()
        var rng = SplitMix(seed: 21)
        for i in 0..<500 {
            var junk = Data([DatagramKind.ikInitiation.rawValue])
            junk += Data((0..<(HelperChannel.initiationBodySize)).map { _ in UInt8(truncatingIfNeeded: rng.next()) })
            let r = channel.receive(junk, from: Self.source(UInt8(i % 200)))
            XCTAssertEqual(r.events, [.dropped(.badMac)])
            XCTAssertNil(r.reply)
        }
        XCTAssertEqual(channel.receive(good.datagram, from: Self.addr).events, [.handshakeAnswered])
    }

    func testInitiationWithoutOrWithWrongMac1IsRefused() throws {
        let c = try initiate()
        // The old 109-byte shape (no mac1) is the wrong size now.
        let bare = c.datagram.dropLast(Mac1.size)
        XCTAssertEqual(channel.receive(Data(bare), from: Self.addr).events, [.dropped(.malformed)])
        // Any single flipped bit anywhere, mac1 included, fails before the handshake runs.
        for bit in 0..<(c.datagram.count * 8) {
            var t = c.datagram
            t[bit / 8] ^= 1 << UInt8(bit % 8)
            let ev = channel.receive(t, from: Self.addr).events
            XCTAssertFalse(ev.contains(.handshakeAnswered), "bit \(bit)")
        }
        // A mac1 computed for another Mac's key is refused.
        let wrongKey = Mac1.append(to: Data(c.datagram.dropLast(Mac1.size)), macPublic: PrivKey().publicKey.rawRepresentation)
        XCTAssertEqual(channel.receive(wrongKey, from: Self.addr).events, [.dropped(.badMac)])
        XCTAssertEqual(channel.receive(c.datagram, from: Self.addr).events, [.handshakeAnswered])
    }

    func testOneNoisySourceCannotDrainTheGlobalHandshakeBudget() throws {
        // Valid mac1 but an unpaired key: passes the cheap filter, fails in Noise.
        let stranger = PrivKey()
        var answered = 0
        for i in 0..<100 {
            let c = try initiate(key: stranger, at: env.wall.addingTimeInterval(Double(i)))
            if channel.receive(c.datagram, from: Self.source(1)).events == [.dropped(.handshakeRateLimit)] { continue }
            answered += 1
        }
        // Only the per-source burst got through to the handshake code.
        XCTAssertEqual(answered, Int(ChannelLimits.handshakesPerSourceBurst))
        let legit = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertEqual(channel.receive(legit.datagram, from: Self.source(2)).events, [.handshakeAnswered])
    }

    func testGlobalHandshakeBudgetStillAppliesAcrossSources() throws {
        var answered = 0
        for i in 0..<20 {
            let c = try initiate(at: env.wall.addingTimeInterval(Double(i) + 1))
            if channel.receive(c.datagram, from: Self.source(UInt8(i))).reply != nil { answered += 1 }
        }
        XCTAssertEqual(answered, 5)
    }

    func testSameSourceHandshakesAreLimitedAndRefill() throws {
        var answered = 0
        for i in 0..<10 {
            let c = try initiate(at: env.wall.addingTimeInterval(Double(i) + 1))
            if channel.receive(c.datagram, from: Self.addr).reply != nil { answered += 1 }
        }
        XCTAssertEqual(answered, 2)
        env.mono += 1
        let c = try initiate(at: env.wall.addingTimeInterval(50))
        XCTAssertNotNil(channel.receive(c.datagram, from: Self.addr).reply)
    }

    func testSourceTableIsBounded() {
        var limiter = SourceRateLimiter()
        var now = 0.0
        for i in 0..<20_000 {
            let a = PeerAddress(bytes: [10, UInt8(i >> 16 & 0xff), UInt8(i >> 8 & 0xff), UInt8(i & 0xff)])
            _ = limiter.allow(a, now: now)
            now += 0.0001
            XCTAssertLessThanOrEqual(limiter.trackedCount, ChannelLimits.maxTrackedSources)
        }
        // Idle sources are forgotten again, so legitimate new ones fit after a quiet second.
        now += 10
        _ = limiter.allow(PeerAddress(bytes: [192, 0, 2, 9]), now: now)
        XCTAssertLessThan(limiter.trackedCount, 5)
    }

    func testSourceTableOverflowSharesOneBucketInsteadOfGrowing() {
        var limiter = SourceRateLimiter()
        for i in 0..<ChannelLimits.maxTrackedSources {
            XCTAssertTrue(limiter.allow(PeerAddress(bytes: [10, 1, UInt8(i >> 8), UInt8(i & 0xff)]), now: 5))
        }
        var passed = 0
        for i in 0..<100 where limiter.allow(PeerAddress(bytes: [10, 2, 0, UInt8(i)]), now: 5) { passed += 1 }
        XCTAssertEqual(passed, Int(ChannelLimits.handshakesPerSourceBurst))
        XCTAssertEqual(limiter.trackedCount, ChannelLimits.maxTrackedSources)
    }

    // MARK: session and source bookkeeping

    func testEndSessionAlsoDropsAPendingSessionWhenNothingIsActive() throws {
        let c = try initiate()
        let r = channel.receive(c.datagram, from: Self.addr)
        let s = try finish(c, reply: try XCTUnwrap(r.reply))
        XCTAssertNil(channel.endSession(), "nothing was active")
        // The answered-but-unauthenticated session must not come alive afterwards.
        XCTAssertEqual(try send(s, InputPacket(type: .keepalive)).events, [.dropped(.noSession)])
        XCTAssertNil(channel.activePeerKey)
    }

    func testEndSessionDropsPendingAndActive() throws {
        let s1 = try connect()
        _ = try send(s1, InputPacket(type: .keepalive))
        env.mono += 0.1
        let c2 = try initiate(at: env.wall.addingTimeInterval(1))
        let s2 = try finish(c2, reply: try XCTUnwrap(channel.receive(c2.datagram, from: Self.addr).reply))
        XCTAssertEqual(channel.endSession(), .ended(.bye))
        XCTAssertEqual(try send(s2, InputPacket(type: .keepalive)).events, [.dropped(.noSession)])
        XCTAssertEqual(try send(s1, InputPacket(type: .keepalive)).events, [.dropped(.noSession)])
    }

    func testAnInitiationNeverMovesTheAddressOfALiveSession() throws {
        var seen: [PeerAddress?] = []
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired },
            environment: { peer, _ in
                seen.append(peer)
                return GateInputs(
                    toggleOn: true, peerPaired: false, udpPeer: peer, screenSharingPeers: [Self.addr],
                    consoleUID: 501, ownUID: 501, screenLocked: false, accessibilityGranted: true,
                    rateLimited: false, snapshotAt: e.mono)
            },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }))
        let s1 = try connect()
        _ = try send(s1, InputPacket(type: .keepalive))
        XCTAssertEqual(seen.last, Self.addr)
        env.mono += 0.2
        // A valid handshake from another address (a replayed-and-forwarded or
        // spoofed one) is answered but must not change where the live session is.
        let c2 = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertNotNil(channel.receive(c2.datagram, from: Self.source(66)).reply)
        let r = try send(s1, InputPacket(type: .input, dx: 1))
        XCTAssertEqual(seen.last, Self.addr)
        XCTAssertEqual(r.events.last, .input(InputPacket(type: .input, dx: 1)))
    }

    func testSessionAddressComesFromItsFirstAuthenticatedPacket() throws {
        var seen: [PeerAddress?] = []
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired },
            environment: { peer, _ in
                seen.append(peer)
                return GateInputs(
                    toggleOn: true, peerPaired: false, udpPeer: peer, screenSharingPeers: [Self.source(7)],
                    consoleUID: 501, ownUID: 501, screenLocked: false, accessibilityGranted: true,
                    rateLimited: false, snapshotAt: e.mono)
            },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }))
        let c = try initiate()
        let reply = try XCTUnwrap(channel.receive(c.datagram, from: Self.source(66)).reply)  // handshake from 66
        let s = try finish(c, reply: reply)
        // First authenticated packet comes from 7: that is the session's address.
        let r = channel.receive(try s.seal(InputPacket(type: .input, dx: 2).encode(), now: env.mono), from: Self.source(7))
        XCTAssertEqual(seen, [Self.source(7)])
        XCTAssertEqual(r.events.last, .input(InputPacket(type: .input, dx: 2)))
    }

    // MARK: environment freshness

    func testStaleOrMissingEnvironmentSnapshotDeniesInput() throws {
        let s = try connect()
        XCTAssertEqual(try send(s, InputPacket(type: .input, dx: 1)).events.last, .input(InputPacket(type: .input, dx: 1)))
        for age in [ChannelLimits.environmentMaxAge + 0.01, 5, 3600] {
            env.snapshotAge = age
            env.mono += 0.3
            let r = try send(s, InputPacket(type: .input, dx: 1))
            XCTAssertEqual(r.events.last, .denied(.locked), "age \(age)")
        }
        env.snapshotAge = nil
        env.mono += 0.3
        XCTAssertEqual(try send(s, InputPacket(type: .input, dx: 1)).events.last, .denied(.locked))
        env.snapshotAge = ChannelLimits.environmentMaxAge
        env.mono += 0.3
        XCTAssertEqual(try send(s, InputPacket(type: .input, dx: 1)).events.last, .input(InputPacket(type: .input, dx: 1)))
    }

    // MARK: replay across restarts

    func testReplayedInitiationIsRefusedAfterARestart() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("timestamps.json")
        func restart() {
            let e = env
            channel = HelperChannel(
                staticKey: macKey, pairedKeys: { e.paired },
                environment: { peer, _ in
                    GateInputs(
                        toggleOn: true, peerPaired: false, udpPeer: peer, screenSharingPeers: [Self.addr],
                        consoleUID: 501, ownUID: 501, screenLocked: false, accessibilityGranted: true,
                        rateLimited: false, snapshotAt: e.mono)
                },
                clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }),
                timestamps: FileTimestampStore(url: file))
        }
        restart()
        let c = try initiate()
        XCTAssertNotNil(channel.receive(c.datagram, from: Self.addr).reply)
        let attrs = try FileManager.default.attributesOfItem(atPath: file.path)
        XCTAssertEqual(attrs[.posixPermissions] as? Int, 0o600)
        // The helper restarts inside the 300 s clock window; the captured datagram comes back.
        restart()
        env.mono += 30
        let again = channel.receive(c.datagram, from: Self.addr)
        XCTAssertNil(again.reply)
        XCTAssertEqual(again.events, [.dropped(.staleTimestamp)])
        // A genuinely newer initiation is still welcome.
        let fresh = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertNotNil(channel.receive(fresh.datagram, from: Self.addr).reply)
    }

    func testUnreadableTimestampFileRefusesHandshakesInsteadOfGuessing() throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try Data("not json".utf8).write(to: file)
        defer { try? FileManager.default.removeItem(at: file) }
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired }, environment: { _, _ in fatalError() },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }), timestamps: FileTimestampStore(url: file))
        XCTAssertFalse(channel.timestampsTrusted)
        let r = channel.receive(try initiate().datagram, from: Self.addr)
        XCTAssertNil(r.reply)
        XCTAssertEqual(r.events, [.dropped(.handshakeFailed)])
    }

    func testHandshakeIsRefusedWhenTheTimestampCannotBeStored() throws {
        final class Failing: TimestampStore {
            func load() throws -> [Data: Data] { [:] }
            func save(peer: Data, timestamp: Data) throws { throw CocoaError(.fileWriteOutOfSpace) }
        }
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired }, environment: { _, _ in fatalError() },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }), timestamps: Failing())
        let r = channel.receive(try initiate().datagram, from: Self.addr)
        XCTAssertNil(r.reply)
        XCTAssertEqual(r.events, [.dropped(.handshakeFailed)])
    }

    // MARK: protocol binding

    func testHandshakeWithAnotherPrologueIsRefused() throws {
        for prologue in [
            "OSS-mousehelper v1 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=linux responder=mac",
            "OSS-mousehelper v2 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=mac responder=linux",
            "x",
        ] {
            let hs = try HandshakeState(
                pattern: .ik, initiator: true, staticKey: linuxKey,
                remoteStatic: macKey.publicKey.rawRepresentation, prologue: Data(prologue.utf8))
            let m = Data([DatagramKind.ikInitiation.rawValue]) + (try hs.writeMessage(payload: TAI64N.encode(env.wall)))
            let dg = Mac1.append(to: m, macPublic: macKey.publicKey.rawRepresentation)
            let r = channel.receive(dg, from: Self.addr)
            XCTAssertNil(r.reply)
            XCTAssertEqual(r.events, [.dropped(.handshakeFailed)], prologue)
            env.mono += 1
        }
    }

    // MARK: motion limits

    func testMotionBudgetPerSecond() throws {
        let s = try connect()
        let big = InputPacket(type: .input, dx: .max, dy: .max)  // clamps to 2 * 32752 units
        var delivered = 0, limited = 0
        for _ in 0..<10 {
            let r = try send(s, big)
            if r.events.contains(.input(big.clamped())) { delivered += 1 }
            if r.events.contains(.denied(.rateLimited)) { limited += 1 }
        }
        let perPacket = Double(2 * Int(WireLimits.maxMotion))
        XCTAssertEqual(delivered, Int(ChannelLimits.motionUnitsPerSecond / perPacket))
        XCTAssertEqual(delivered + limited, 10)
        XCTAssertGreaterThan(limited, 0)
        env.mono += 1
        XCTAssertTrue(try send(s, big).events.contains(.input(big.clamped())))
        // Ordinary pointer speed is nowhere near the budget.
        env.mono += 1
        for _ in 0..<1000 {
            XCTAssertTrue(try send(s, InputPacket(type: .input, dx: 100, dy: -100)).events.contains(.input(InputPacket(type: .input, dx: 100, dy: -100))))
            env.mono += 0.001
        }
    }

    func testPerPacketMotionClampIsTwoThousandFortySevenPoints() {
        XCTAssertEqual(WireLimits.maxMotion, 32752)
        XCTAssertEqual(Int(WireLimits.maxMotion), 2047 * 16)
        let p = InputPacket(type: .input, dx: .min, dy: .max).clamped()
        XCTAssertEqual(p.dx, -32752)
        XCTAssertEqual(p.dy, 32752)
        XCTAssertEqual(-p.dx, 32752, "negating the clamped value cannot overflow")
    }
}
