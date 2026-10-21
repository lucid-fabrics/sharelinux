import XCTest

@testable import HelperCore

/// Third-audit leftovers on pairing and the timestamp store. Each test is
/// written from the intended behaviour and fails with the fix reverted.
final class PairingSquatTests: XCTestCase {
    let macKey = PrivKey()
    var now = 100.0
    var window: PairingWindow!
    var events: [PairingWindowEvent] = []
    static let hostA = PeerAddress(bytes: [10, 0, 0, 1])!
    static let hostB = PeerAddress(bytes: [10, 0, 0, 2])!

    override func setUp() {
        now = 100
        events = []
        window = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now })
        window.onEvent = { [unowned self] in self.events.append($0) }
    }

    func toNonce(_ l: LinuxPairer, from src: PeerAddress?) throws -> Data {
        let m2 = try XCTUnwrap(try window.receive(try l.message1(), from: src))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = try window.receive(m3, from: src)
        return try XCTUnwrap(try window.receive(commit, from: src))
    }

    // MARK: free squat

    /// A LAN host on its own address used to restart message 1 every second for
    /// free, so the honest device always met "another source" for the whole
    /// window. Now it gets a few starts, then it is refused for the window.
    func testASourceCannotSquatTheWindowByRestartingForFree() throws {
        for _ in 0..<ChannelLimits.pairingMaxStartsPerSource {
            now += ChannelLimits.pairingMinStartSpacing
            XCTAssertNoThrow(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA))
        }
        // The fourth restart, and every later one, is refused.
        for _ in 0..<5 {
            now += ChannelLimits.pairingMinStartSpacing
            XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)) {
                XCTAssertEqual($0 as? PairingError, .sourceExhausted)
            }
        }
        XCTAssertEqual(window.failures, 0, "free for the honest side, but the squatter gets nowhere")
        // Its last attempt stalls and times out for free; the honest device gets in.
        now += ChannelLimits.pairingAttemptTimeout
        let honest = try LinuxPairer(key: PrivKey())
        XCTAssertNoThrow(try toNonce(honest, from: Self.hostB))
        // The squatter cannot push the honest device out either.
        XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)) {
            XCTAssertEqual($0 as? PairingError, .otherSource)
        }
    }

    func testARefusedSourceStaysRefusedWhenTheFieldIsClear() throws {
        for _ in 0..<ChannelLimits.pairingMaxStartsPerSource {
            now += ChannelLimits.pairingMinStartSpacing
            _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)
        }
        now += ChannelLimits.pairingAttemptTimeout + 1  // its attempt is gone, nobody else is trying
        XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)) {
            XCTAssertEqual($0 as? PairingError, .sourceExhausted)
        }
        XCTAssertNoThrow(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostB))
    }

    func testTheAppHearsAboutAnotherDeviceAndARefusedSource() throws {
        _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)
        XCTAssertEqual(events, [])
        now += 0.1
        _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostB)  // runs beside A
        XCTAssertEqual(events, [.anotherDeviceTrying])
        // Junk from a third address that belongs to no attempt is reported too.
        XCTAssertThrowsError(try window.receive(Data([DatagramKind.xxMessage3.rawValue]) + Data(count: 80), from: PeerAddress(bytes: [10, 0, 0, 3]))) {
            XCTAssertEqual($0 as? PairingError, .otherSource)
        }
        XCTAssertEqual(events, [.anotherDeviceTrying, .anotherDeviceTrying])
        for _ in 0..<ChannelLimits.pairingMaxStartsPerSource {
            now += ChannelLimits.pairingMinStartSpacing
            _ = try? window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)
        }
        XCTAssertEqual(events.last, .sourceRefused)
        // Also when the other one shows up while the person is deciding.
        let w = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now })
        var seen: [PairingWindowEvent] = []
        w.onEvent = { seen.append($0) }
        let l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try w.receive(try l.message1(), from: Self.hostA))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = try w.receive(m3, from: Self.hostA)
        let rm = try XCTUnwrap(try w.receive(commit, from: Self.hostA))
        _ = try w.receive(try l.afterResponderNonce(rm), from: Self.hostA)
        XCTAssertThrowsError(try w.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostB)) {
            XCTAssertEqual($0 as? PairingError, .busy)
        }
        XCTAssertEqual(seen, [.anotherDeviceTrying])
    }

    // MARK: spoofed-source stalls before r_M

    func spoofed(_ i: Int) -> PeerAddress { PeerAddress(bytes: [172, 16, UInt8(i / 250), UInt8(i % 250) + 1])! }

    /// Off-path spoofers send message 1 from forged addresses and never see
    /// message 2. Each used to hold the single attempt slot for the full 20 s
    /// timeout, so one packet every 20 s kept an honest device out of the whole
    /// two-minute window. They now occupy slots that a real device pushes out.
    func testSpoofedSourcesCannotHoldTheWindowFromAnHonestDevice() throws {
        for i in 0..<(ChannelLimits.pairingMaxConcurrentAttempts * 3) {
            now += ChannelLimits.pairingMinGlobalStartSpacing * 2
            XCTAssertNoThrow(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: spoofed(i)), "spoof \(i)")
        }
        XCTAssertLessThanOrEqual(window.attemptsInFlight, ChannelLimits.pairingMaxConcurrentAttempts)
        now += ChannelLimits.pairingMinGlobalStartSpacing * 2
        let honest = try LinuxPairer(key: PrivKey())
        let rm = try toNonce(honest, from: Self.hostA)
        _ = try window.receive(try honest.afterResponderNonce(rm), from: Self.hostA)
        XCTAssertEqual(window.pending?.sas, honest.code)
        XCTAssertEqual(window.failures, 0, "spoofers cost the honest device nothing")
        XCTAssertEqual(window.attemptsInFlight, 1, "the spoofed attempts died when the first one reached r_M")
        XCTAssertNotNil(window.confirm())
        XCTAssertTrue(events.contains(.anotherDeviceTrying), "the owner still hears that others are trying")
    }

    /// Pushing an honest attempt out needs a full set of fresh starts inside
    /// its round trip to message 3, not one stray packet.
    func testOneOrFewSpoofedStartsDoNotDisturbAnHonestAttemptInFlight() throws {
        let honest = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try window.receive(try honest.message1(), from: Self.hostA))
        for i in 0..<(ChannelLimits.pairingMaxConcurrentAttempts - 2) {
            now += ChannelLimits.pairingMinGlobalStartSpacing * 2
            _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: spoofed(i))
        }
        let (m3, commit) = try honest.afterMessage2(m2)
        _ = try window.receive(m3, from: Self.hostA)
        let rm = try XCTUnwrap(try window.receive(commit, from: Self.hostA))
        _ = try window.receive(try honest.afterResponderNonce(rm), from: Self.hostA)
        XCTAssertNotNil(window.pending)
        XCTAssertEqual(window.failures, 0)
    }

    /// An attempt whose source answered message 2 cannot be pushed out by newcomers.
    func testSlotsHeldByConfirmedSourcesAreNotEvicted() throws {
        var pairers: [LinuxPairer] = []
        var commits: [Data] = []
        for i in 0..<ChannelLimits.pairingMaxConcurrentAttempts {
            now += ChannelLimits.pairingMinGlobalStartSpacing * 2
            let l = try LinuxPairer(key: PrivKey())
            let m2 = try XCTUnwrap(try window.receive(try l.message1(), from: spoofed(i)))
            let (m3, commit) = try l.afterMessage2(m2)
            _ = try window.receive(m3, from: spoofed(i))
            pairers.append(l); commits.append(commit)
        }
        now += ChannelLimits.pairingMinGlobalStartSpacing * 2
        XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: spoofed(100))) {
            XCTAssertEqual($0 as? PairingError, .tooManyAttempts)
        }
        XCTAssertEqual(events.last, .anotherDeviceTrying)
        XCTAssertEqual(window.attemptsInFlight, ChannelLimits.pairingMaxConcurrentAttempts)
        // The first of them still reaches r_M; the rest are dropped for free.
        XCTAssertNotNil(try window.receive(commits[0], from: spoofed(0)))
        XCTAssertEqual(window.attemptsInFlight, 1)
        XCTAssertEqual(window.failures, 0)
    }

    func testAnAttemptStalledBeforeTheNonceLetsGoAfterTheShortTimeout() throws {
        _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)
        let junk = Data([DatagramKind.xxMessage3.rawValue]) + Data(count: 80)
        now += ChannelLimits.pairingUnchargedTimeout - 0.1
        XCTAssertThrowsError(try window.receive(junk, from: Self.hostB)) { XCTAssertEqual($0 as? PairingError, .otherSource) }
        now += 0.2
        XCTAssertThrowsError(try window.receive(junk, from: Self.hostB)) { XCTAssertEqual($0 as? PairingError, .malformed) }
        XCTAssertEqual(window.attemptsInFlight, 0)
        XCTAssertEqual(window.failures, 0, "free: nobody saw a code")
        XCTAssertLessThan(ChannelLimits.pairingUnchargedTimeout, ChannelLimits.pairingAttemptTimeout)
    }

    func testStartSpacingIsPerSourceAndTheGlobalOneIsSmall() throws {
        _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)
        // Another source right behind it is fine (a global 1 s spacing locked it out).
        now += ChannelLimits.pairingMinGlobalStartSpacing * 2
        XCTAssertNoThrow(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostB))
        // The same source again too soon is not.
        now += ChannelLimits.pairingMinGlobalStartSpacing * 2
        XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)) {
            XCTAssertEqual($0 as? PairingError, .tooSoon)
        }
        // And nobody can start twice inside the global spacing.
        now += 1
        _ = try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: spoofed(1))
        now += ChannelLimits.pairingMinGlobalStartSpacing / 2
        XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: spoofed(2))) {
            XCTAssertEqual($0 as? PairingError, .tooSoon)
        }
    }

    func testSourceBookkeepingStaysBoundedUnderASpoofedFlood() throws {
        for i in 0..<(ChannelLimits.pairingMaxTrackedSources * 2) {
            now += ChannelLimits.pairingMinGlobalStartSpacing * 1.1
            _ = try? window.receive(try LinuxPairer(key: PrivKey()).message1(), from: spoofed(i))
        }
        XCTAssertLessThanOrEqual(window.trackedSourceCount, ChannelLimits.pairingMaxTrackedSources + 1)
        XCTAssertLessThanOrEqual(window.attemptsInFlight, ChannelLimits.pairingMaxConcurrentAttempts)
    }

    // MARK: spoofed message 1 after r_M

    /// A forger copying the victim's address sends message 1 after r_M left.
    /// That used to end the attempt early and charge the honest device.
    func testMessage1AfterTheNonceDoesNotEndOrChargeTheAttempt() throws {
        let honest = try LinuxPairer(key: PrivKey())
        let rm = try toNonce(honest, from: Self.hostA)
        for _ in 0..<5 {
            now += ChannelLimits.pairingMinStartSpacing
            XCTAssertThrowsError(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)) {
                XCTAssertEqual($0 as? PairingError, .unauthenticated)
            }
        }
        XCTAssertEqual(window.failures, 0)
        // The honest device still finishes its attempt.
        _ = try window.receive(try honest.afterResponderNonce(rm), from: Self.hostA)
        XCTAssertNotNil(window.pending)
        XCTAssertNotNil(window.confirm())
    }

    func testMessage1AfterTheNonceIsAcceptedOnceTheAttemptTimedOut() throws {
        _ = try toNonce(try LinuxPairer(key: PrivKey()), from: Self.hostA)
        now += ChannelLimits.pairingAttemptTimeout
        XCTAssertNoThrow(try window.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA))
        XCTAssertEqual(window.failures, 1, "the stale charged attempt counted when it timed out")
    }

    // MARK: cross-window failure cap

    func failWindow(ledger: PairingFailureLedger) throws -> PairingWindow {
        let w = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now }, ledger: ledger)
        for _ in 0..<ChannelLimits.pairingMaxFailures {
            now += ChannelLimits.pairingMinStartSpacing
            let l = try LinuxPairer(key: PrivKey())
            let src = PeerAddress(bytes: [10, 0, 1, UInt8(w.failures)])
            let rm = try XCTUnwrap(try w.receive(try l.message1(), from: src).flatMap { m2 -> Data? in
                let (m3, commit) = try l.afterMessage2(m2)
                _ = try w.receive(m3, from: src)
                return try w.receive(commit, from: src)
            })
            _ = try w.receive(try l.afterResponderNonce(rm), from: src)
            w.reject()
        }
        return w
    }

    func testNineFailuresInADayLockPairingWithAVisibleCooldown() throws {
        var wall = 1_800_000_000.0
        let ledger = PairingFailureLedger(store: MemoryPairingFailureStore(), wall: { wall })
        for i in 0..<3 {
            let w = try failWindow(ledger: ledger)
            XCTAssertFalse(w.isOpen)
            if i < 2 { XCTAssertNil(ledger.lockoutRemaining, "window \(i + 1)") }
            wall += 3600
            now += ChannelLimits.pairingWindow
        }
        let remaining = try XCTUnwrap(ledger.lockoutRemaining)
        // Locked until the oldest of the nine ages past 24 h: 21 h after the loop.
        XCTAssertEqual(remaining, 24 * 3600 - 3 * 3600, accuracy: 60)
        // A new window is born shut and says why.
        var seen: [PairingWindowEvent] = []
        let w = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now }, ledger: ledger)
        w.onEvent = { seen.append($0) }
        XCTAssertFalse(w.isOpen)
        XCTAssertEqual(w.lockoutRemaining, ledger.lockoutRemaining)
        XCTAssertThrowsError(try w.receive(try LinuxPairer(key: PrivKey()).message1(), from: Self.hostA)) {
            XCTAssertEqual($0 as? PairingError, .lockedOut)
        }
        // After the cooldown pairing works again.
        wall += 24 * 3600
        XCTAssertNil(ledger.lockoutRemaining)
        let w2 = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now }, ledger: ledger)
        XCTAssertTrue(w2.isOpen)
    }

    func testEightFailuresDoNotLockAndOldOnesAgeOut() {
        var wall = 1_800_000_000.0
        let ledger = PairingFailureLedger(store: MemoryPairingFailureStore(), wall: { wall })
        for _ in 0..<(ChannelLimits.pairingFailureCap - 1) { ledger.recordFailure() }
        XCTAssertNil(ledger.lockoutRemaining)
        // Failures spread over several days never add up.
        for _ in 0..<30 {
            wall += 12 * 3600
            ledger.recordFailure()
        }
        XCTAssertNil(ledger.lockoutRemaining)
    }

    func testTheCooldownSurvivesARestartAndAnUnreadableStoreFailsClosed() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("pairing-failures.json")
        let wall = 1_800_000_000.0
        let first = PairingFailureLedger(store: FilePairingFailureStore(url: file), wall: { wall })
        for _ in 0..<ChannelLimits.pairingFailureCap { first.recordFailure() }
        XCTAssertNotNil(first.lockoutRemaining)
        let attrs = try FileManager.default.attributesOfItem(atPath: file.path)
        XCTAssertEqual(attrs[.posixPermissions] as? Int, 0o600)
        // Restart: same file, same cooldown.
        let second = PairingFailureLedger(store: FilePairingFailureStore(url: file), wall: { wall })
        XCTAssertNotNil(second.lockoutRemaining)
        // Garbage in the file: locked, not open.
        try Data("{".utf8).write(to: file)
        let third = PairingFailureLedger(store: FilePairingFailureStore(url: file), wall: { wall })
        XCTAssertNotNil(third.lockoutRemaining)
        // The owner's deliberate reset clears it, durably.
        third.reset()
        XCTAssertNil(third.lockoutRemaining)
        XCTAssertNil(PairingFailureLedger(store: FilePairingFailureStore(url: file), wall: { wall }).lockoutRemaining)
    }
}

final class TimestampStoreFileTests: XCTestCase {
    func tempDir() throws -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        return dir
    }

    func testAMissingFileIsReportedAsMissingNotAsEmpty() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = FileTimestampStore(url: dir.appendingPathComponent("t.json"))
        XCTAssertThrowsError(try store.load()) { XCTAssertEqual($0 as? TimestampStoreError, .missing) }
    }

    /// The temp file is created with mode 0600 in the first place: with a
    /// permissive umask the old write-then-chmod left it readable for a moment.
    func testFileIsCreatedPrivateFromTheStartEvenWithAPermissiveUmask() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let old = umask(0)
        defer { umask(old) }
        let target = dir.appendingPathComponent("t.json")
        var tempMode: Int?
        try AtomicFile.write(Data("x".utf8), to: target) { path in
            tempMode = (try? FileManager.default.attributesOfItem(atPath: path))?[.posixPermissions] as? Int
        }
        XCTAssertEqual(tempMode, 0o600)
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: target.path)[.posixPermissions] as? Int, 0o600)
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), ["t.json"], "no temp left behind")
    }

    func testAnExistingWideFileIsReplacedByAPrivateOne() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let target = dir.appendingPathComponent("t.json")
        try Data("old".utf8).write(to: target)
        try FileManager.default.setAttributes([.posixPermissions: 0o644], ofItemAtPath: target.path)
        try AtomicFile.write(Data("new".utf8), to: target)
        XCTAssertEqual(try Data(contentsOf: target), Data("new".utf8))
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: target.path)[.posixPermissions] as? Int, 0o600)
    }

    /// A symlink planted at the target is replaced, never written through.
    func testASymlinkAtTheTargetIsReplacedNotFollowed() throws {
        let dir = try tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        let secret = dir.appendingPathComponent("elsewhere")
        try Data("keep".utf8).write(to: secret)
        let target = dir.appendingPathComponent("t.json")
        try FileManager.default.createSymbolicLink(at: target, withDestinationURL: secret)
        try AtomicFile.write(Data("new".utf8), to: target)
        XCTAssertEqual(try Data(contentsOf: secret), Data("keep".utf8))
        XCTAssertEqual(try Data(contentsOf: target), Data("new".utf8))
        XCTAssertNil(try? FileManager.default.destinationOfSymbolicLink(atPath: target.path))
    }
}

extension ChannelTests {
    /// A missing file used to mean "empty table, trust everything": a captured
    /// initiation inside the 300 s window worked after the file was removed.
    func testMissingTimestampFileStartsAFloorAtNowAndPersistsIt() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let file = dir.appendingPathComponent("timestamps.json")
        func open() {
            let e = env
            channel = HelperChannel(
                staticKey: macKey, pairedKeys: { e.paired }, environment: { _, _ in fatalError() },
                clock: ChannelClock(monotonic: { e.mono }, wall: { e.wall }), timestamps: FileTimestampStore(url: file))
        }
        let captured = try initiate(at: env.wall.addingTimeInterval(-60))
        let atStart = try initiate(at: env.wall)
        open()
        XCTAssertTrue(channel.timestampStoreFirstRun)
        XCTAssertFalse(channel.timestampStoreRecovered)
        XCTAssertTrue(channel.timestampsTrusted)
        XCTAssertTrue(FileManager.default.fileExists(atPath: file.path), "the floor is persisted at once")
        XCTAssertEqual(try FileManager.default.attributesOfItem(atPath: file.path)[.posixPermissions] as? Int, 0o600)
        XCTAssertEqual(channel.receive(captured.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        XCTAssertEqual(channel.receive(atStart.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        // Restart: not a first run any more, and the floor still holds.
        open()
        XCTAssertFalse(channel.timestampStoreFirstRun)
        XCTAssertEqual(channel.receive(captured.datagram, from: Self.addr).events, [.dropped(.staleTimestamp)])
        env.mono += 1
        let fresh = try initiate(at: env.wall.addingTimeInterval(1))
        XCTAssertEqual(channel.receive(fresh.datagram, from: Self.addr).events, [.handshakeAnswered])
    }
}
