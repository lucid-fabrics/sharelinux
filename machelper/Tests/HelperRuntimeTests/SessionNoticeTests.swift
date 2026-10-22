import CryptoKit
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// W5 findings on the Mac: the helper says so when it stops (so the Linux side falls back at once),
/// the owner is told about a session once and only when the helper acts on it, and pairing stays
/// possible while a client sits in Exclusive mode with the switch off.
final class SessionNoticeTests: XCTestCase {
    private func live() throws -> (RuntimeRig, RuntimeRig.Client) {
        let rig = RuntimeRig()
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        let c = try rig.connect()
        rig.run(0.3)
        return (rig, c)
    }

    private func decode(_ n: (Data, PeerAddress), _ c: RuntimeRig.Client, _ rig: RuntimeRig) -> HelperState? {
        rig.status(n.0, c)
    }

    // MARK: M3, the explicit "stopped" reply

    func testTurningTheSwitchOffTellsTheLinuxSideAtOnce() throws {
        let (rig, c) = try live()
        rig.notices.removeAll()
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(false) }
        XCTAssertFalse(rig.notices.isEmpty, "silence is not how the Linux side hears about it")
        for n in rig.notices {
            XCTAssertEqual(n.1, RuntimeRig.addr, "only to the live session's address")
            XCTAssertEqual(decode(n, c, rig), .disabled)
        }
        XCTAssertEqual(rig.notices.count, 2, "twice, because a datagram can be lost")
    }

    func testStopNowAndTheHotkeyTellTheLinuxSideToo() throws {
        let (rig, c) = try live()
        rig.notices.removeAll()
        rig.runtime.queue.sync { rig.runtime.killSwitch() }
        XCTAssertFalse(rig.notices.isEmpty)
        XCTAssertEqual(decode(rig.notices[0], c, rig), .disabled)
    }

    func testNothingIsVolunteeredWithoutALiveSession() throws {
        let rig = RuntimeRig()
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(false) }
        rig.runtime.queue.sync { rig.runtime.killSwitch() }
        XCTAssertTrue(rig.notices.isEmpty)
    }

    func testTheNoticeIsAMacAuthenticatedStatusThatCannotBeReplayed() throws {
        let (rig, c) = try live()
        rig.notices.removeAll()
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(false) }
        XCTAssertEqual(rig.notices.count, 2)
        XCTAssertNotEqual(rig.notices[0].0, rig.notices[1].0, "each copy has its own counter")
        XCTAssertEqual(decode(rig.notices[0], c, rig), .disabled)
        XCTAssertNil(decode(rig.notices[0], c, rig), "the same datagram a second time is a replay")
    }

    // MARK: M4, one notification per session that the helper acts on

    func testASessionWhileTheSwitchIsOffAnnouncesNothing() throws {
        let rig = RuntimeRig()
        let c = try rig.connect()
        rig.run(0.5)
        rig.send(c, InputPacket(type: .input, dx: 16))
        rig.send(c, InputPacket(type: .keepalive))
        XCTAssertNotNil(rig.runtime.state.device)
        XCTAssertEqual(rig.announced, [], "the helper does not act on anything, so it must not say it can")
    }

    func testTurningTheSwitchOnAnnouncesOnceTheHelperActs() throws {
        let rig = RuntimeRig()
        let c = try rig.connect()
        rig.run(0.3)
        XCTAssertEqual(rig.announced, [])
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        rig.run(0.3)
        rig.send(c, InputPacket(type: .keepalive))
        rig.send(c, InputPacket(type: .input, dx: 16))
        rig.send(c, InputPacket(type: .keepalive))
        XCTAssertEqual(rig.announced.count, 1)
    }

    func testARekeyOfTheSameDeviceDoesNotAnnounceAgain() throws {
        let (rig, first) = try live()
        XCTAssertEqual(rig.announced.count, 1)
        var current = first
        for _ in 0..<3 {
            for _ in 0..<4 {  // the old session stays alive on keepalives until the new one takes over
                rig.run(0.4)
                rig.send(current, InputPacket(type: .keepalive))
            }
            current = try rig.connect()  // same paired key: a new session replaces the old one
            rig.send(current, InputPacket(type: .input, dx: 16))
        }
        XCTAssertEqual(rig.announced.count, 1, "rekeys are not new sessions")
    }

    func testAfterStopNowANewSessionIsAnnouncedWhenTheOwnerSwitchesBackOn() throws {
        let (rig, _) = try live()
        rig.runtime.queue.sync { rig.runtime.killSwitch() }
        let c = try rig.connect()  // the Linux side reconnects, the switch is off
        rig.send(c, InputPacket(type: .keepalive))
        XCTAssertEqual(rig.announced.count, 1, "nothing new while the switch is off")
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        rig.run(0.3)
        rig.send(c, InputPacket(type: .keepalive))
        XCTAssertEqual(rig.announced.count, 2, "a fresh session that is now acted on")
    }

    // MARK: pairing with a client in Exclusive mode

    func testPairingAndResetStayOpenWhileASessionIsLiveButTheSwitchIsOff() throws {
        let rig = RuntimeRig(pairing: true, prepairedName: "Desk PC")
        let c = try rig.connect()
        rig.run(0.3)
        XCTAssertNotNil(rig.runtime.state.device, "a client sits in Exclusive mode")
        XCTAssertFalse(rig.runtime.state.pairingBlocked)
        XCTAssertTrue(rig.coordinator!.open(), "nothing it sends is acted on")
        rig.coordinator!.close()
        rig.send(c, InputPacket(type: .input, dx: 16))
        XCTAssertEqual(rig.poster.log, [], "and nothing was")
        var outcome: KeyResetOutcome?
        rig.runtime.queue.sync { outcome = rig.runtime.resetHelperKeys() }
        XCTAssertEqual(outcome, .done)
    }

    func testPairingAndResetAreRefusedWhileASessionCanInject() throws {
        let rig = RuntimeRig(pairing: true, prepairedName: "Desk PC")
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        _ = try rig.connect()
        rig.run(0.3)
        XCTAssertTrue(rig.runtime.state.pairingBlocked)
        XCTAssertFalse(rig.coordinator!.open())
        XCTAssertEqual(rig.coordinator!.status.notice, .refused(.sessionActive))
        var outcome: KeyResetOutcome?
        rig.runtime.queue.sync { outcome = rig.runtime.resetHelperKeys() }
        XCTAssertEqual(outcome, .refusedSessionActive)
    }

    func testSwitchingOnWhileAPairingWindowIsOpenClosesIt() throws {
        let rig = RuntimeRig(pairing: true)
        XCTAssertTrue(rig.coordinator!.open())
        rig.runtime.queue.sync { _ = rig.runtime.setAllowRawMouse(true) }
        XCTAssertEqual(rig.coordinator!.status.phase, .closed)
    }
}
