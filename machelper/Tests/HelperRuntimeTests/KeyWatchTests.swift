import CryptoKit
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// The tripwire on the helper's own keys (design 2.10, 3.7): when the Keychain items change outside
/// the app, all sessions end, the toggle goes off and the owner is told. A tripwire, not a
/// guarantee: the tests pin what it does catch.
final class KeyWatchTests: XCTestCase {
    private func liveRig() throws -> (RuntimeRig, RuntimeRig.Client) {
        let rig = RuntimeRig(watchKeys: true)
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        let c = try rig.connect()
        rig.run(0.2)
        XCTAssertNotNil(rig.runtime.state.device, "a session is live")
        XCTAssertTrue(rig.runtime.allowRawMouse)
        return (rig, c)
    }

    private func assertOffAndTold(_ rig: RuntimeRig, file: StaticString = #filePath, line: UInt = #line) {
        XCTAssertTrue(rig.runtime.state.keysChanged, file: file, line: line)
        XCTAssertFalse(rig.runtime.allowRawMouse, "the toggle is off", file: file, line: line)
        XCTAssertNil(rig.runtime.state.device, "no session", file: file, line: line)
        XCTAssertEqual(rig.keysChangedAlerts, 1, "told exactly once", file: file, line: line)
    }

    // MARK: the three triggers, each at a new session and on wake

    func testAMissingIdentityEndsEverythingBeforeTheNextSession() throws {
        let (rig, _) = try liveRig()
        rig.keyStore!.outsideDeleteIdentity = true
        rig.run(2)  // past the check interval
        rig.poster.log.removeAll()
        XCTAssertNil(try rig.connectIfAnswered(), "the handshake is not even answered")
        assertOffAndTold(rig)
        XCTAssertTrue(rig.poster.log.isEmpty)
    }

    func testADifferentIdentityEndsEverythingBeforeTheNextSession() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.keyStore!.outsideIdentity = PrivKey()
        XCTAssertNil(try rig.connectIfAnswered())
        assertOffAndTold(rig)
    }

    func testChangedRecordsOutsideTheAppEndEverythingBeforeTheNextSession() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.keyStore!.outsideTrustSalt = Data([1])
        XCTAssertNil(try rig.connectIfAnswered())
        assertOffAndTold(rig)
    }

    func testEachTriggerAlsoWorksOnWakeAndEndsALiveSession() throws {
        for tamper in [
            { (s: MemoryKeyStore) in s.outsideDeleteIdentity = true },
            { (s: MemoryKeyStore) in s.outsideIdentity = PrivKey() },
            { (s: MemoryKeyStore) in s.outsideTrustSalt = Data([7]) },
        ] {
            let (rig, c) = try liveRig()
            tamper(rig.keyStore!)
            rig.runtime.queue.sync { rig.runtime.didWake() }
            assertOffAndTold(rig)
            rig.poster.log.removeAll()
            rig.send(c, InputPacket(type: .input, dx: 5, dy: 5))
            XCTAssertTrue(rig.poster.log.isEmpty, "nothing is posted after the trip")
        }
    }

    /// The handshake check is rate limited; the session start itself is always checked, before anything
    /// is accepted.
    func testASessionStartedWithinTheRateLimitIsStillCheckedAndEndedAtOnce() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        _ = try rig.connect()  // checks, then a second connect inside the same second skips the handshake check
        rig.keyStore!.outsideTrustSalt = Data([3])
        let calls = rig.keyStore!.snapshotCalls
        let c = try rig.connectIfAnswered()
        XCTAssertGreaterThan(rig.keyStore!.snapshotCalls, calls, "the session start looked again")
        XCTAssertNotNil(c, "the handshake was answered (rate limited)")
        assertOffAndTold(rig)
        rig.poster.log.removeAll()
        if let c { rig.send(c, InputPacket(type: .input, dx: 5, dy: 5)) }
        XCTAssertTrue(rig.poster.log.isEmpty)
    }

    // MARK: what it must not do

    func testTheAppsOwnWritesAreNotAChangeOutsideTheApp() throws {
        let rig = RuntimeRig(pairing: true, watchKeys: true)
        try rig.keyStore!.addPaired(PairedDevice(name: "Desk PC", key: rig.linuxKey.publicKey.rawRepresentation))
        rig.runtime.queue.sync { rig.runtime.pairedKeysChanged() }  // what the coordinator calls after its own write
        rig.runtime.queue.sync { XCTAssertTrue(rig.runtime.checkKeys()) }
        XCTAssertFalse(rig.runtime.state.keysChanged)
        XCTAssertEqual(rig.keysChangedAlerts, 0)
        // The same write WITHOUT the app announcing it is the outside change.
        try rig.keyStore!.addPaired(PairedDevice(name: "Sneaky", key: PrivKey().publicKey.rawRepresentation))
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.checkKeys()) }
        XCTAssertTrue(rig.runtime.state.keysChanged)
    }

    func testAnUnreadableStoreConcludesNothing() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.keyStore!.failSnapshot = true
        rig.runtime.queue.sync { XCTAssertTrue(rig.runtime.checkKeys(), "a locked Keychain is not an attack") }
        XCTAssertFalse(rig.runtime.state.keysChanged)
        XCTAssertEqual(rig.keysChangedAlerts, 0)
    }

    func testAfterATripTheSwitchStaysOffAndHandshakesAreDroppedEvenIfTheKeychainLooksFineAgain() throws {
        let (rig, _) = try liveRig()
        rig.keyStore!.outsideTrustSalt = Data([9])
        rig.runtime.queue.sync { rig.runtime.didWake() }
        rig.keyStore!.outsideTrustSalt = Data()  // put back: still latched, the key in memory is not trusted
        rig.run(2)
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.setAllowRawMouse(true)) }
        XCTAssertFalse(rig.runtime.allowRawMouse)
        XCTAssertNil(try rig.connectIfAnswered())
        XCTAssertEqual(rig.keysChangedAlerts, 1, "one alert, not one per attempt")
    }

    func testTurningTheSwitchOnLooksAtTheKeysFirst() throws {
        let rig = RuntimeRig(watchKeys: true)
        rig.keyStore!.outsideDeleteIdentity = true
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.setAllowRawMouse(true)) }
        XCTAssertFalse(rig.runtime.allowRawMouse)
        XCTAssertEqual(rig.keysChangedAlerts, 1)
    }

    func testAFloodOfHandshakeInitiationsCausesAtMostOneKeychainLookPerInterval() throws {
        let rig = RuntimeRig(watchKeys: true)
        let before = rig.keyStore!.snapshotCalls
        for _ in 0..<200 {
            _ = rig.runtime.handle(datagram: Data([DatagramKind.ikInitiation.rawValue]) + Data(repeating: 1, count: 100), from: RuntimeRig.addr)
        }
        XCTAssertLessThanOrEqual(rig.keyStore!.snapshotCalls - before, 1)
        rig.run(1.5)
        _ = rig.runtime.handle(datagram: Data([DatagramKind.ikInitiation.rawValue]) + Data(repeating: 1, count: 100), from: RuntimeRig.addr)
        XCTAssertLessThanOrEqual(rig.keyStore!.snapshotCalls - before, 2)
    }

    func testPairingWritesAreRefusedOnceTheKeysChangedOutside() throws {
        let rig = RuntimeRig(pairing: true, watchKeys: true, prepairedName: "Desk PC")
        let k = rig.linuxKey.publicKey.rawRepresentation
        rig.keyStore!.outsideDeleteIdentity = true
        rig.runtime.queue.sync { rig.coordinator!.remove(key: k) }
        XCTAssertEqual(rig.coordinator!.status.notice, .keysChangedOutside)
        XCTAssertEqual(try rig.keyStore!.loadPaired().map(\.key), [k], "nothing was written")
        XCTAssertTrue(rig.runtime.state.keysChanged)
    }

    // MARK: launch tripwire (UserDefaults)

    private func defaults() -> (UserDefaults, () -> Void) {
        let name = "oss-test-" + UUID().uuidString
        let d = UserDefaults(suiteName: name)!
        return (d, { d.removePersistentDomain(forName: name) })
    }

    func testAChangedFingerprintAtLaunchIsReportedOnceAndAnUnchangedOneIsNot() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let key = PrivKey()
        let store = MemoryKeyStore(identity: key)
        let first = try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation, defaults: d)
        XCTAssertFalse(first.changedSinceLastLaunch, "nothing to compare with on the first launch")
        let same = try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation, defaults: d)
        XCTAssertFalse(same.changedSinceLastLaunch)
        // Between launches the identity was replaced.
        let other = PrivKey()
        let replaced = try KeyWatch(store: MemoryKeyStore(identity: other), ownKey: other.publicKey.rawRepresentation, defaults: d)
        XCTAssertTrue(replaced.changedSinceLastLaunch)
        // The alert stays until the owner keeps the paired devices: a relaunch asks again.
        let again = try KeyWatch(store: MemoryKeyStore(identity: other), ownKey: other.publicKey.rawRepresentation, defaults: d)
        XCTAssertTrue(again.changedSinceLastLaunch)
        again.acceptCurrent()
        let after = try KeyWatch(store: MemoryKeyStore(identity: other), ownKey: other.publicKey.rawRepresentation, defaults: d)
        XCTAssertFalse(after.changedSinceLastLaunch, "kept: the new fingerprint is what is remembered now")
    }

    func testRecordsChangedBetweenLaunchesAreReportedButTheAppsOwnPairingIsNot() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let key = PrivKey()
        let store = MemoryKeyStore(identity: key)
        let w1 = try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation, defaults: d)
        try store.addPaired(PairedDevice(name: "Desk PC", key: PrivKey().publicKey.rawRepresentation))
        w1.rebase()  // the app paired a device
        XCTAssertFalse(try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation, defaults: d).changedSinceLastLaunch)
        store.outsideTrustSalt = Data([4])  // a program edited the records while the helper was not running
        XCTAssertTrue(try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation, defaults: d).changedSinceLastLaunch)
    }

    func testBothFingerprintsAreExposedForTheMenuAndMoveWithTheirParts() throws {
        let key = PrivKey()
        let store = MemoryKeyStore(identity: key)
        let w = try KeyWatch(store: store, ownKey: key.publicKey.rawRepresentation)
        XCTAssertEqual(w.identityFingerprint, DeviceIdentity.fingerprint(key.publicKey.rawRepresentation))
        let before = w.trustFingerprint
        try store.addPaired(PairedDevice(name: "Desk PC", key: PrivKey().publicKey.rawRepresentation))
        w.rebase()
        XCTAssertNotEqual(w.trustFingerprint, before)
        let lines = PairingText.keyMenuLines(identity: w.identityFingerprint, pairedList: w.trustFingerprint)
        XCTAssertEqual(lines.0, "This Mac's key: " + w.identityFingerprint)
        XCTAssertEqual(lines.1, "Paired list: " + w.trustFingerprint)
    }

    // MARK: reset

    func testResetIsRefusedWhileASessionIsLiveAndChangesNothing() throws {
        let rig = RuntimeRig(pairing: true, watchKeys: true, prepairedName: "Desk PC")
        let k = rig.linuxKey.publicKey.rawRepresentation
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        _ = try rig.connect()
        XCTAssertNotNil(rig.runtime.state.device)
        var outcome: KeyResetOutcome?
        rig.runtime.queue.sync { outcome = rig.runtime.resetHelperKeys() }
        XCTAssertEqual(outcome, .refusedSessionActive)
        XCTAssertEqual(try rig.keyStore!.loadPaired().map(\.key), [k], "the records are still there")
        XCTAssertNotNil(try rig.keyStore!.snapshot().identity)
        XCTAssertNotNil(rig.runtime.state.device, "the session was not touched")
        XCTAssertEqual(rig.coordinator!.status.notice, .resetRefusedSessionActive)
    }

    func testResetStartsFreshAndKeepsTheHelperOffUntilItIsStartedAgainWithoutAnAlert() throws {
        let (d, cleanup) = defaults()
        defer { cleanup() }
        let rig = RuntimeRig(pairing: true, watchKeys: true, defaults: d, prepairedName: "Desk PC")
        rig.keyStore!.revocationDamaged = true
        var outcome: KeyResetOutcome?
        rig.runtime.queue.sync { outcome = rig.runtime.resetHelperKeys() }
        XCTAssertEqual(outcome, .done)
        XCTAssertEqual(try rig.keyStore!.loadPaired(), [])
        XCTAssertNil(try rig.keyStore!.snapshot().identity)
        XCTAssertFalse(rig.keyStore!.revocationDamaged)
        XCTAssertEqual(rig.coordinator!.status.notice, .keysReset)
        XCTAssertEqual(rig.coordinator!.pairedKeys, [])
        // The old identity is gone from the Keychain on purpose: that is not an attack.
        rig.runtime.queue.sync { XCTAssertTrue(rig.runtime.checkKeys()) }
        XCTAssertEqual(rig.keysChangedAlerts, 0)
        XCTAssertFalse(rig.runtime.state.keysChanged)
        // The key in memory is the old one, so the switch stays off until the app is started again.
        rig.runtime.queue.sync { XCTAssertFalse(rig.runtime.setAllowRawMouse(true)) }
        XCTAssertNotNil(rig.runtime.cannotEnableReason)
        // And the next launch is not accused of a change.
        let fresh = PrivKey()
        let next = try KeyWatch(store: MemoryKeyStore(identity: fresh), ownKey: fresh.publicKey.rawRepresentation, defaults: d)
        XCTAssertFalse(next.changedSinceLastLaunch)
    }

    func testTheWordsAreFixedAndPlain() {
        XCTAssertEqual(
            PairingText.keysChangedAlert,
            "The helper's keys changed outside the app. Paired devices were turned off. Pair again only if you did this yourself.")
        for s in [
            PairingText.keysChangedTitle, PairingText.keysChangedAlert, PairingText.resetMenuItem, PairingText.resetQuestion,
            PairingText.resetExplanation, PairingText.resetConfirmButton, PairingText.resetRefused, PairingText.resetDone,
            PairingText.resetFailed, PairingText.damagedNotice, PairingText.damagedShort, PairingText.keysChangedNotice,
            PairingText.keysChangedState, PairingText.resetBlocksSwitch,
        ] {
            XCTAssertFalse(s.contains("\u{2014}"), s)
            XCTAssertFalse(s.contains("Curve25519") || s.contains("HMAC") || s.contains("Keychain Access"), s)
        }
        XCTAssertTrue(PairingText.resetExplanation.contains("All Linux devices must pair again"))
        XCTAssertTrue(PairingText.resetDone.contains("All Linux devices must pair again"))
    }

    /// MEDIUM: with a damaged removed-devices list the old text said "unrecognised entries ... do not
    /// give anyone access", which hid that nothing works and how to recover.
    func testADamagedRevocationSetExplainsItselfAndPointsToResetInsteadOfSayingNothingIsWrong() throws {
        let rig = RuntimeRig(pairing: true)
        rig.keyStore!.revocationDamaged = true
        let s = rig.coordinator!.status
        XCTAssertTrue(s.keyStoreDamaged)
        let text = try XCTUnwrap(PairingText.notice(s))
        XCTAssertEqual(text, PairingText.damagedNotice)
        XCTAssertTrue(text.contains("Reset helper keys..."))
        XCTAssertTrue(text.contains("All Linux devices must pair again"))
        XCTAssertFalse(text.contains("do not give anyone access"))
        XCTAssertFalse(text.contains("unrecognised"))
        XCTAssertTrue(PairingText.isWarning(s))
        // Without the damage and without ignored items there is nothing to say.
        rig.keyStore!.revocationDamaged = false
        XCTAssertNil(PairingText.notice(rig.coordinator!.status))
    }
}
