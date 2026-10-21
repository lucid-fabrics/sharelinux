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

    static func spoof(_ i: Int) -> PeerAddress {
        PeerAddress(bytes: [172, UInt8(i >> 16 & 0xff), UInt8(i >> 8 & 0xff), UInt8(i & 0xff)])!
    }

    /// A full table makes room by evicting a source that never authenticated, so
    /// a newcomer gets a bucket of its own instead of a shared, drained one.
    func testFullSourceTableEvictsInsteadOfSharingAnOverflowBucket() {
        var limiter = SourceRateLimiter()
        for i in 0..<ChannelLimits.maxTrackedSources {
            XCTAssertTrue(limiter.allow(Self.spoof(i), now: 5))
        }
        for i in 0..<100 { _ = limiter.allow(Self.spoof(10_000 + i), now: 5) }
        let newcomer = PeerAddress(bytes: [192, 0, 2, 77])
        XCTAssertTrue(limiter.allow(newcomer, now: 5))
        XCTAssertEqual(limiter.trackedCount, ChannelLimits.maxTrackedSources)
    }

    /// Finding 3: spoofing more than 256 sources used to push everyone new into
    /// one shared bucket the spoofers had drained.
    func testAuthenticatedSourceKeepsItsOwnBucketUnderASpoofFlood() {
        var limiter = SourceRateLimiter()
        let real = PeerAddress(bytes: [192, 0, 2, 50])
        limiter.markAuthenticated(real, now: 0)
        for i in 0..<1000 { _ = limiter.allow(Self.spoof(i), now: 1) }
        XCTAssertTrue(limiter.allow(real, now: 1))
        XCTAssertTrue(limiter.allow(real, now: 1))
        XCTAssertFalse(limiter.allow(real, now: 1), "its own burst of 2, not more")
        XCTAssertLessThanOrEqual(limiter.trackedCount, ChannelLimits.maxTrackedSources)
    }

    func testPinnedSourceIsNeverEvictedEvenWhenItIsTheOldestEntry() {
        var limiter = SourceRateLimiter()
        let real = PeerAddress(bytes: [192, 0, 2, 50])
        XCTAssertTrue(limiter.allow(real, now: 0))
        XCTAssertTrue(limiter.allow(real, now: 0))
        limiter.markAuthenticated(real, now: 0)
        for i in 0..<1000 { _ = limiter.allow(Self.spoof(i), now: 0.001 * Double(i + 1) / 10) }
        // Had it been evicted it would come back with a full bucket.
        XCTAssertFalse(limiter.allow(real, now: 0.2))
    }

    func testPinsAreBoundedAndExpire() {
        var limiter = SourceRateLimiter()
        for i in 0..<(ChannelLimits.maxPinnedSources * 3) {
            limiter.markAuthenticated(Self.spoof(i), now: Double(i))
        }
        XCTAssertEqual(limiter.pinnedCount, ChannelLimits.maxPinnedSources)
        XCTAssertFalse(limiter.isPinned(Self.spoof(0)), "the oldest pin makes way")
        XCTAssertTrue(limiter.isPinned(Self.spoof(ChannelLimits.maxPinnedSources * 3 - 1)))
        limiter.markAuthenticated(Self.spoof(9999), now: 100_000)
        XCTAssertEqual(limiter.pinnedCount, 1, "old pins expire")
    }

    /// The same through the channel: a paired device that authenticated, went
    /// quiet, and reconnects after a flood of spoofed senders is still answered.
    func testPairedDeviceStillGetsAnswersAfterASpoofedSourceFlood() throws {
        let real = PeerAddress(bytes: [192, 0, 2, 50])!
        let c = try initiate()
        let r = channel.receive(c.datagram, from: real)
        let s = try finish(c, reply: try XCTUnwrap(r.reply))
        _ = channel.receive(try s.seal(InputPacket(type: .keepalive).encode(), now: env.mono), from: real)
        env.mono += 10
        let stranger = PrivKey()
        for i in 0..<(ChannelLimits.maxTrackedSources + 50) {
            let f = try initiate(key: stranger, at: env.wall.addingTimeInterval(Double(i)))
            _ = channel.receive(f.datagram, from: Self.spoof(i))
        }
        env.mono += 1
        let again = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertEqual(channel.receive(again.datagram, from: real).events, [.handshakeAnswered])
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
        // The next authenticated packet arrives without a known source address:
        // the session keeps the address of its own earlier packets, not the handshake's.
        let r = channel.receive(try s1.seal(InputPacket(type: .input, dx: 1).encode(), now: env.mono), from: nil)
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

    func testPendingSessionHasNoAddressUntilItAuthenticatesAPacket() throws {
        var seen: [PeerAddress?] = []
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired },
            environment: { peer, _ in
                seen.append(peer)
                return GateInputs(
                    toggleOn: true, peerPaired: false, udpPeer: peer, screenSharingPeers: [Self.source(66)],
                    consoleUID: 501, ownUID: 501, screenLocked: false, accessibilityGranted: true,
                    rateLimited: false, snapshotAt: e.mono)
            },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }))
        let c = try initiate()
        let s = try finish(c, reply: try XCTUnwrap(channel.receive(c.datagram, from: Self.source(66)).reply))
        // Its first authenticated packet carries no address: the handshake's address is not inherited.
        let r = channel.receive(try s.seal(InputPacket(type: .input, dx: 2).encode(), now: env.mono), from: nil)
        XCTAssertEqual(seen.count, 1)
        XCTAssertNil(seen[0] ?? nil)
        XCTAssertEqual(r.events.last, .denied(.noSession))
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
        // First run: the new file starts with a floor of "now", so the first
        // initiation has to be newer than the helper's start.
        XCTAssertTrue(channel.timestampStoreFirstRun)
        let c = try initiate(at: env.wall.addingTimeInterval(1))
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
        let fresh = try initiate(at: env.wall.addingTimeInterval(2))
        XCTAssertNotNil(channel.receive(fresh.datagram, from: Self.addr).reply)
    }

    /// Finding 4: a corrupt store used to block every handshake until someone
    /// deleted the file by hand.
    func testCorruptTimestampFileIsMovedAsideAndAFreshFloorApplies() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("timestamps.json")
        try Data("not json".utf8).write(to: file)
        func open() {
            let e = env
            channel = HelperChannel(
                staticKey: macKey, pairedKeys: { e.paired },
                environment: { _, _ in fatalError() },
                clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }), timestamps: FileTimestampStore(url: file))
        }
        // Captured before the corruption was noticed: older than the recovery time.
        let captured = try initiate(at: env.wall.addingTimeInterval(-5))
        let atRecovery = try initiate(at: env.wall)
        open()
        XCTAssertTrue(channel.timestampsTrusted)
        XCTAssertTrue(channel.timestampStoreRecovered)
        let aside = try FileManager.default.contentsOfDirectory(atPath: dir.path).filter { $0.hasPrefix("timestamps.json.corrupt-") }
        XCTAssertEqual(aside.count, 1)
        XCTAssertEqual(try Data(contentsOf: dir.appendingPathComponent(aside[0])), Data("not json".utf8))
        // Anything not newer than the recovery time is refused, replays included.
        XCTAssertEqual(channel.receive(captured.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        XCTAssertEqual(channel.receive(atRecovery.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        let fresh = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertEqual(channel.receive(fresh.datagram, from: Self.addr).events, [.handshakeAnswered])
        // The floor is stored, so a restart right after cannot reopen the window.
        env.mono += 1
        open()
        XCTAssertFalse(channel.timestampStoreRecovered, "the new file is healthy")
        XCTAssertEqual(channel.receive(captured.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        XCTAssertEqual(channel.receive(atRecovery.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        let attrs = try FileManager.default.attributesOfItem(atPath: file.path)
        XCTAssertEqual(attrs[.posixPermissions] as? Int, 0o600)
    }

    func testUnrecoverableTimestampStoreStillRefusesHandshakes() throws {
        final class Stuck: TimestampStore {
            func load() throws -> [Data: Data] { throw CocoaError(.fileReadCorruptFile) }
            func save(peer: Data, timestamp: Data) throws {}
        }
        let e = env
        channel = HelperChannel(
            staticKey: macKey, pairedKeys: { e.paired }, environment: { _, _ in fatalError() },
            clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }), timestamps: Stuck())
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

    /// Scroll had no per-second budget: 2000 pixels per axis at 1000 packets
    /// per second let a paired device spin the wheel without limit.
    func testScrollBudgetPerSecond() throws {
        let s = try connect()
        let big = InputPacket(type: .input, sx: .max, sy: .max)  // clamps to 2 * 2000 pixels
        var delivered = 0, limited = 0
        for _ in 0..<40 {
            let r = try send(s, big)
            if r.events.contains(.input(big.clamped())) { delivered += 1 }
            if r.events.contains(.denied(.rateLimited)) { limited += 1 }
        }
        XCTAssertEqual(delivered, Int(ChannelLimits.scrollUnitsPerSecond / Double(2 * Int(WireLimits.maxScroll))))
        XCTAssertEqual(delivered + limited, 40)
        env.mono += 1
        XCTAssertTrue(try send(s, big).events.contains(.input(big.clamped())), "the budget refills")
        // An ordinary fling (100 px at 250 Hz) stays inside the budget.
        env.mono += 1
        let flick = InputPacket(type: .input, sy: 100)
        for _ in 0..<500 {
            XCTAssertTrue(try send(s, flick).events.contains(.input(flick)))
            env.mono += 0.004
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
