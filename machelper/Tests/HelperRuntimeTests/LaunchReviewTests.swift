import CryptoKit
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// W3 leftovers and the W5 low findings about the helper's keys: a launch that finds them changed
/// keeps everything off until the owner decides, the app's own half-finished writes do not raise a
/// false alarm, and nothing in the same datagram as a failed key check is acted on.
final class LaunchReviewTests: XCTestCase {
    private func defaults() -> (UserDefaults, () -> Void) {
        let name = "oss-test-" + UUID().uuidString
        let d = UserDefaults(suiteName: name)!
        return (d, { d.removePersistentDomain(forName: name) })
    }

    /// A rig whose last run left different fingerprints behind.
    private func changedRig(_ d: UserDefaults, pairing: Bool = false) -> RuntimeRig {
        d.set("0000 0000 0000 0000", forKey: "keys.lastSeen.identity")
        d.set("1111 1111 1111 1111", forKey: "keys.lastSeen.trust")
        return RuntimeRig(pairing: pairing, watchKeys: true, defaults: d)
    }

    // MARK: the launch alert latches

    func testAChangeFoundAtLaunchAcceptsNoSessionAndKeepsTheSwitchOff() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let rig = changedRig(d)
        XCTAssertTrue(rig.keyWatch!.changedSinceLastLaunch)
        XCTAssertTrue(rig.runtime.state.keysReviewPending)
        XCTAssertTrue(rig.runtime.state.keysChanged)
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.setAllowRawMouse(true)) }
        XCTAssertFalse(rig.runtime.allowRawMouse)
        XCTAssertNil(try rig.connectIfAnswered(), "the handshake is not even answered")
        XCTAssertNil(rig.runtime.state.device)
        XCTAssertTrue(rig.poster.log.isEmpty)
    }

    func testKeepingThePairedDevicesLetsSessionsInButTheSwitchStaysOffUntilTheOwnerTurnsItOn() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let rig = changedRig(d)
        rig.runtime.queue.sync { rig.runtime.keepPairedDevices() }
        XCTAssertFalse(rig.runtime.state.keysReviewPending)
        XCTAssertFalse(rig.runtime.state.keysChanged)
        XCTAssertFalse(rig.runtime.allowRawMouse)
        let c = try XCTUnwrap(try rig.connectIfAnswered())
        XCTAssertEqual(rig.posted { rig.send(c, InputPacket(type: .input, dx: 160)) }, [], "switch still off")
        rig.runtime.queue.sync { XCTAssertTrue(rig.runtime.setAllowRawMouse(true)) }
        XCTAssertEqual(rig.posted { rig.send(c, InputPacket(type: .input, dx: 160)) }, ["move 10 0 held=0"])
    }

    /// "Leave the helper off", quit, reopen: the same review comes back, nothing is accepted.
    func testLeavingTheHelperOffAndRelaunchingAsksAgain() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let first = changedRig(d)
        XCTAssertTrue(first.runtime.state.keysReviewPending)
        // The owner chose "Leave the helper off": nothing else happens, the app quits.
        let second = RuntimeRig(watchKeys: true, defaults: d)
        XCTAssertTrue(second.keyWatch!.changedSinceLastLaunch, "the old baseline is still what the review compares against")
        XCTAssertEqual(second.keyWatch!.previousIdentityFingerprint, "0000 0000 0000 0000")
        XCTAssertTrue(second.runtime.state.keysReviewPending)
        XCTAssertNil(try second.connectIfAnswered(), "no session is accepted without the review")
    }

    func testKeepingThePairedDevicesIsRememberedForTheNextLaunch() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let first = changedRig(d)
        first.runtime.queue.sync { first.runtime.keepPairedDevices() }
        let second = try KeyWatch(
            store: first.keyStore!, ownKey: first.macKey.publicKey.rawRepresentation, defaults: d)
        XCTAssertFalse(second.changedSinceLastLaunch)
    }

    func testAResetAfterTheLaunchChangeDoesNotLiftTheHoldBeforeTheAppRestarts() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let rig = changedRig(d, pairing: true)
        var outcome: KeyResetOutcome?
        rig.runtime.queue.sync { outcome = rig.runtime.resetHelperKeys() }
        XCTAssertEqual(outcome, .done)
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.setAllowRawMouse(true)) }
        XCTAssertTrue(rig.runtime.state.keysReviewPending)
    }

    func testNoChangeAtLaunchHoldsNothing() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let rig = RuntimeRig(watchKeys: true, defaults: d)
        XCTAssertFalse(rig.runtime.state.keysReviewPending)
        XCTAssertNotNil(try rig.connectIfAnswered())
    }

    func testTheReviewKeepsWhatTheLastRunLeftBehindForTheOwnerToCompare() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let rig = changedRig(d)
        XCTAssertEqual(rig.keyWatch!.previousIdentityFingerprint, "0000 0000 0000 0000")
        XCTAssertEqual(rig.keyWatch!.previousTrustFingerprint, "1111 1111 1111 1111")
        let text = PairingText.launchChangeText(
            identityBefore: rig.keyWatch!.previousIdentityFingerprint, identityNow: rig.keyWatch!.identityFingerprint,
            pairedListBefore: rig.keyWatch!.previousTrustFingerprint, pairedListNow: rig.keyWatch!.trustFingerprint)
        XCTAssertTrue(text.contains("was 0000 0000 0000 0000, now " + rig.keyWatch!.identityFingerprint))
        XCTAssertTrue(text.contains("No Linux device can use the helper until you decide"), "and that is true now")
        XCTAssertTrue(text.contains("Keep paired devices") && text.contains("Reset helper keys..."))
        XCTAssertFalse(text.contains("\u{2014}"))
        XCTAssertFalse(text.contains("were turned off"), "the old claim that the runtime did not make true")
    }

    // MARK: rebase after the app's own half-finished write

    func testAHalfFinishedPairingWriteIsNotAFalseAlarm() throws {
        let rig = RuntimeRig(pairing: true, watchKeys: true)
        rig.keyStore!.failWrites = true
        rig.keyStore!.failHalfWay = true  // the removed-devices list moved, then the record could not be added
        rig.coordinator!.open()
        try pairDevice(rig)
        XCTAssertEqual(rig.coordinator!.status.notice, .storeFailed("The Keychain did not accept the key."))
        rig.runtime.queue.sync { XCTAssertTrue(rig.runtime.checkKeys(), "the app's own write is the baseline") }
        XCTAssertFalse(rig.runtime.state.keysChanged)
        XCTAssertEqual(rig.keysChangedAlerts, 0)
    }

    func testAHalfFinishedRemoveWriteIsNotAFalseAlarm() throws {
        let rig = RuntimeRig(pairing: true, watchKeys: true, prepairedName: "Desk PC")
        rig.keyStore!.failWrites = true
        rig.keyStore!.failHalfWay = true
        rig.runtime.queue.sync { rig.coordinator!.remove(key: rig.linuxKey.publicKey.rawRepresentation) }
        XCTAssertEqual(rig.coordinator!.status.notice, .storeFailed("The Keychain did not remove the key."))
        rig.runtime.queue.sync { XCTAssertTrue(rig.runtime.checkKeys()) }
        XCTAssertEqual(rig.keysChangedAlerts, 0)
    }

    func testAWriteFromOutsideTheAppStillTrips() throws {
        let rig = RuntimeRig(pairing: true, watchKeys: true)
        rig.keyStore!.outsideTrustSalt = Data([5])
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.checkKeys()) }
    }

    /// Drives a pairing to the Confirm click through the runtime's datagram entry point.
    private func pairDevice(_ rig: RuntimeRig) throws {
        let l = try LinuxPairer(key: rig.linuxKey, name: "Desk PC")
        let src = PeerAddress(bytes: [192, 0, 2, 7])!
        let m2 = try XCTUnwrap(rig.runtime.handle(datagram: try l.message1(), from: src))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = rig.runtime.handle(datagram: m3, from: src)
        let rm = try XCTUnwrap(rig.runtime.handle(datagram: commit, from: src))
        _ = rig.runtime.handle(datagram: try l.afterResponderNonce(rm), from: src)
        rig.mono += 1.1  // the person looks at the code for a moment
        rig.runtime.tick()
        let s = rig.coordinator!.status
        _ = rig.coordinator!.confirm(code: try XCTUnwrap(s.phaseCode), peerKey: try XCTUnwrap(s.pendingKey))
    }

    // MARK: the rebase lock

    /// `rebase()` changes what `check()` compares with. With the lock missing it ran in the middle of
    /// a check.
    func testRebaseWaitsForACheckInProgress() throws {
        let key = PrivKey()
        let store = SlowSnapshotStore(identity: key)
        let watch = try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation)
        let finishedInside = expectation(description: "rebase finished while the check was running")
        finishedInside.isInverted = true
        let rebased = DispatchSemaphore(value: 0)
        store.onFirstSnapshotInCheck = {
            DispatchQueue.global().async {
                watch.rebase()
                finishedInside.fulfill()
                rebased.signal()
            }
            self.wait(for: [finishedInside], timeout: 0.3)
        }
        store.armed = true
        _ = watch.check()
        XCTAssertEqual(rebased.wait(timeout: .now() + 2), .success, "and it runs as soon as the check is over")
    }
}

/// A store whose first snapshot after arming calls a hook, standing in for a slow Keychain read.
final class SlowSnapshotStore: KeyStore {
    private let inner: MemoryKeyStore
    var armed = false
    var onFirstSnapshotInCheck: (() -> Void)?
    init(identity: PrivKey) { inner = MemoryKeyStore(identity: identity) }
    func loadOrCreateIdentity() throws -> Curve25519.KeyAgreement.PrivateKey { try inner.loadOrCreateIdentity() }
    func loadPaired() throws -> [PairedDevice] { try inner.loadPaired() }
    func addPaired(_ device: PairedDevice) throws { try inner.addPaired(device) }
    func removePaired(key: Data) throws { try inner.removePaired(key: key) }
    var ignoredRecords: Int { inner.ignoredRecords }
    var revocationDamaged: Bool { inner.revocationDamaged }
    func resetAll() throws { try inner.resetAll() }
    func snapshot() throws -> KeySnapshot {
        if armed {
            armed = false
            onFirstSnapshotInCheck?()
        }
        return try inner.snapshot()
    }
}

final class SameDatagramTests: XCTestCase {
    /// LOW: the first packet of a new session can arrive in the datagram that starts it. When the key
    /// check at the session start fails, that packet must not be acted on.
    func testAnInputPacketInTheDatagramThatFailsTheKeyCheckIsNotPosted() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        _ = try rig.connect()  // a check just ran, so the next handshake skips its own
        rig.keyStore!.outsideTrustSalt = Data([3])
        rig.poster.log.removeAll()
        let c = try rig.connectIfAnswered(first: InputPacket(type: .input, buttons: [.left], dx: 160))
        XCTAssertNotNil(c, "the handshake itself was answered (rate limited check)")
        XCTAssertTrue(rig.runtime.state.keysChanged)
        XCTAssertEqual(rig.poster.log, [], "not a move, not a press")
        XCTAssertEqual(rig.runtime.controller.held, [])
        XCTAssertNil(rig.runtime.state.device)
        XCTAssertEqual(rig.keysChangedAlerts, 1)
    }

    func testTheDatagramThatFailsTheKeyCheckGetsNoAnswerEither() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        _ = try rig.connect()
        rig.keyStore!.outsideTrustSalt = Data([4])
        let c = try XCTUnwrap(try rig.connectIfAnswered())
        rig.notices.removeAll()
        let reply = rig.send(c, InputPacket(type: .keepalive))
        XCTAssertNil(reply)
        XCTAssertTrue(rig.notices.isEmpty)
    }
}
