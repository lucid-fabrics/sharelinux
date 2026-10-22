import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// A mouse button must never stay down on a live desktop. Every way the input
/// can stop (a gate deny, a session ending, the owner pulling the plug) has to
/// post button-up, and nothing may be posted afterwards.
final class StuckButtonTests: XCTestCase {
    var rig: RuntimeRig!
    var client: RuntimeRig.Client!

    override func setUp() async throws {
        rig = RuntimeRig()
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.1)
        client = try rig.connect()
    }

    /// Holds the left button and moves, as a drag would.
    func startDrag() {
        rig.send(client, InputPacket(type: .input, buttons: [.left], dx: 32, dy: 0))
        XCTAssertEqual(rig.poster.downs, 1)
        XCTAssertEqual(rig.runtime.controller.held, [.left])
        XCTAssertTrue(rig.runtime.controller.injecting)
    }

    /// After `stop`, the button is up exactly once and further input posts nothing.
    func assertStopped(_ name: String, _ stop: () -> Void, file: StaticString = #filePath, line: UInt = #line) {
        startDrag()
        rig.poster.log.removeAll()
        stop()
        XCTAssertEqual(rig.poster.ups, 1, "\(name): button-up posted once", file: file, line: line)
        XCTAssertEqual(rig.runtime.controller.held, [], "\(name): nothing held", file: file, line: line)
        XCTAssertFalse(rig.runtime.controller.injecting, "\(name): indicator off", file: file, line: line)
        let after = rig.posted {
            for _ in 0..<5 {
                rig.run(0.05)
                rig.send(client, InputPacket(type: .input, buttons: [.left], dx: 160, dy: 160, sx: 4, sy: 4))
            }
        }
        XCTAssertEqual(after, [], "\(name): nothing posted afterwards", file: file, line: line)
    }

    // MARK: the gate says no

    func testScreenLockedReleasesAndStopsAtOnce() {
        assertStopped("lock") {
            rig.session.locked = true
            rig.runtime.refreshEnvironment()
        }
    }

    func testLockNotificationReleasesBeforeTheNextSample() {
        assertStopped("lock notification") {
            // The notification arrives before the dictionary changed: buttons still come up.
            rig.runtime.environmentInterruption(.locked)
            rig.session.locked = true
        }
    }

    func testUnknownLockStateReleasesAndStops() {
        assertStopped("lock unknown") {
            rig.session.locked = nil
            rig.runtime.refreshEnvironment()
        }
    }

    func testLeavingTheConsoleReleasesAndStops() {
        assertStopped("console lost") {
            rig.session.console = nil
            rig.runtime.refreshEnvironment()
        }
    }

    func testAnotherUsersConsoleReleasesAndStops() {
        assertStopped("fast user switch") {
            rig.session.console = 502
            rig.runtime.refreshEnvironment()
        }
    }

    func testAccessibilityGrantRevokedReleasesAndStops() {
        assertStopped("grant revoked") {
            rig.access.isGranted = false
            rig.runtime.refreshEnvironment()
        }
    }

    func testScreenSharingGoneReleasesAndStops() {
        assertStopped("screen sharing gone") {
            rig.sharing.current = []
            rig.runtime.refreshEnvironment()
        }
    }

    func testUnreadableScreenSharingTableReleasesAndStops() {
        assertStopped("screen sharing unknown") {
            rig.sharing.current = nil
            rig.runtime.refreshEnvironment()
        }
    }

    /// A poller that stalls must not let input keep flowing on old data: after
    /// 500 ms without a fresh sample the gate refuses, and the deny releases.
    func testStalledEnvironmentPollerStopsInputAndReleases() {
        startDrag()
        rig.poster.log.removeAll()
        rig.run(0.4, sampling: false)
        // Still fresh enough: input flows and the button is still down.
        rig.send(client, InputPacket(type: .input, buttons: [.left], dx: 16))
        XCTAssertEqual(rig.poster.log, ["move 1 0 held=1"])
        rig.poster.log.removeAll()
        rig.run(0.2, sampling: false)  // the sample is now older than 500 ms
        XCTAssertEqual(rig.poster.log, ["up 0"], "the watchdog releases when the data goes stale")
        rig.poster.log.removeAll()
        let reply = rig.send(client, InputPacket(type: .input, buttons: [.left], dx: 160))
        XCTAssertEqual(rig.poster.log, [])
        XCTAssertEqual(rig.status(reply, client), .locked)
        XCTAssertEqual(rig.runtime.controller.held, [])
    }

    /// A deny that comes from the packet itself (here the motion budget), with the
    /// environment unchanged, releases too: nothing else would.
    func testAPacketLevelDenyReleasesTheButton() {
        startDrag()
        rig.poster.log.removeAll()
        let huge = InputPacket(type: .input, buttons: [.left], dx: .max, dy: .max)
        for _ in 0..<20 where rig.runtime.state.lastDeny == nil { rig.send(client, huge) }
        XCTAssertEqual(rig.runtime.state.lastDeny, .rateLimited, "the budget must run out")
        XCTAssertEqual(rig.poster.ups, 1)
        XCTAssertEqual(rig.runtime.controller.held, [])
    }

    func testDenyOnAPacketReleasesEvenWithoutAnySampleChange() {
        startDrag()
        rig.runtime.setAllowRawMouse(false)  // ends the session and releases
        XCTAssertEqual(rig.poster.ups, 1)
    }

    // MARK: sessions end

    func testToggleOffReleasesAndEndsTheSession() {
        assertStopped("toggle off") { rig.runtime.setAllowRawMouse(false) }
        XCTAssertNil(rig.runtime.state.device)
    }

    func testKillSwitchReleasesEndsTheSessionAndTurnsTheSwitchOff() {
        assertStopped("kill switch") { rig.runtime.killSwitch() }
        XCTAssertFalse(rig.runtime.allowRawMouse)
        XCTAssertNil(rig.runtime.state.device)
        // A new session does not revive it: the owner has to turn the switch on again.
        let again = try? rig.connect()
        XCTAssertNotNil(again)
        let posted = rig.posted { rig.send(again!, InputPacket(type: .input, dx: 160)) }
        XCTAssertEqual(posted, [])
    }

    func testSleepReleasesAndEndsTheSession() {
        assertStopped("sleep") { rig.runtime.willSleep() }
        XCTAssertNil(rig.runtime.state.device)
    }

    func testByePacketReleases() {
        assertStopped("bye") { rig.send(client, InputPacket(type: .bye)) }
    }

    func testSilenceReleasesWithinTheTimeout() {
        assertStopped("silence") { rig.run(ChannelLimits.silenceTimeout + 0.1) }
    }

    /// The controller's own watchdog, without the channel's help.
    func testControllerWatchdogReleasesWhenTrafficStops() {
        startDrag()
        rig.mono += ChannelLimits.silenceTimeout - 0.1
        rig.runtime.controller.tick()
        XCTAssertEqual(rig.runtime.controller.held, [.left], "still inside the timeout")
        rig.runtime.controller.touch()  // a keepalive does NOT keep a held button down
        rig.mono += 0.2
        rig.runtime.controller.tick()
        XCTAssertEqual(rig.runtime.controller.held, [])
        XCTAssertEqual(rig.poster.ups, 1)
    }

    func testReplacedSessionReleasesTheOldOnesButtons() throws {
        startDrag()
        rig.poster.log.removeAll()
        let second = try rig.connect()  // same paired key, first packet replaces the first session
        XCTAssertEqual(rig.poster.ups, 1)
        // The old session's packets are not accepted any more.
        XCTAssertEqual(rig.posted { rig.send(client, InputPacket(type: .input, buttons: [.left], dx: 160)) }, [])
        XCTAssertEqual(rig.posted { rig.send(second, InputPacket(type: .input, dx: 16)) }, ["move 1 0 held=0"])
    }

    func testRemovingThePairedKeyReleasesAndEndsTheSession() {
        assertStopped("unpaired") {
            rig.paired = []
            rig.runtime.pairedKeysChanged()
        }
    }

    func testKeyRemovedWithoutNotificationStopsOnTheNextPacket() {
        startDrag()
        rig.paired = []
        rig.poster.log.removeAll()
        rig.send(client, InputPacket(type: .input, buttons: [.left], dx: 160))
        XCTAssertEqual(rig.poster.log, ["up 0"])
        XCTAssertNil(rig.runtime.state.device)
    }

    // MARK: Screen Sharing ends the helper session within 500 ms

    func testScreenSharingEndingEndsTheSessionWithinHalfASecond() {
        startDrag()
        rig.run(0.1)  // the watcher has seen the Screen Sharing connection
        rig.sharing.current = []
        var ended = -1.0
        var elapsed = 0.0
        while elapsed < 1.0 {
            rig.run(0.05)
            elapsed += 0.05
            if rig.runtime.state.device == nil { ended = elapsed; break }
        }
        XCTAssertGreaterThan(ended, 0, "the session must end")
        XCTAssertLessThanOrEqual(ended, 0.5)
        XCTAssertEqual(rig.poster.ups, 1)
    }

    /// Connecting the helper channel before Screen Sharing is fine: it waits,
    /// posts nothing, and is not torn down while it waits.
    func testASessionWaitingForScreenSharingStaysAliveAndPostsNothing() throws {
        rig.runtime.killSwitch()
        rig.runtime.setAllowRawMouse(true)
        rig.sharing.current = []
        rig.run(0.1)
        let waiting = try rig.connect()
        for _ in 0..<8 {  // two seconds, with the client's keepalives
            rig.run(0.25)
            rig.send(waiting, InputPacket(type: .keepalive))
        }
        XCTAssertNotNil(rig.runtime.state.device)
        XCTAssertEqual(rig.posted { rig.send(waiting, InputPacket(type: .input, buttons: [.left], dx: 160)) }, [])
        // Screen Sharing starts: input flows.
        rig.sharing.current = [RuntimeRig.addr]
        rig.run(0.2)
        XCTAssertEqual(rig.posted { rig.send(waiting, InputPacket(type: .input, dx: 16)) }, ["move 1 0 held=0"])
    }

    // MARK: recovery and the default

    func testInputResumesAfterTheScreenUnlocks() {
        startDrag()
        rig.session.locked = true
        rig.runtime.refreshEnvironment()
        rig.run(0.1)
        rig.session.locked = false
        rig.run(0.1)
        rig.poster.log.removeAll()
        rig.send(client, InputPacket(type: .input, dx: 16))
        XCTAssertEqual(rig.poster.log, ["move 1 0 held=0"], "the session survived the lock and works again")
        XCTAssertNil(rig.runtime.state.lastDeny)
    }

    func testSwitchIsOffByDefaultOnEveryLaunch() throws {
        let fresh = RuntimeRig()
        XCTAssertFalse(fresh.runtime.allowRawMouse)
        XCTAssertFalse(fresh.runtime.state.allowRawMouse)
        let c = try fresh.connect()
        fresh.run(0.1)
        XCTAssertEqual(fresh.posted { fresh.send(c, InputPacket(type: .input, buttons: [.left], dx: 160)) }, [])
        let reply = fresh.send(c, InputPacket(type: .keepalive))
        _ = reply
        XCTAssertEqual(fresh.runtime.state.lastDeny, .disabled)
    }

    func testUnpairedKeyNeverPostsAndNeverGetsAnAnswer() throws {
        let stranger = PrivKey()
        let hs = try HandshakeState(
            pattern: .ik, initiator: true, staticKey: stranger,
            remoteStatic: rig.macKey.publicKey.rawRepresentation)
        rig.wall = rig.wall.addingTimeInterval(1)
        let m = try hs.writeMessage(payload: TAI64N.encode(rig.wall))
        let dg = Mac1.append(
            to: Data([DatagramKind.ikInitiation.rawValue]) + m, macPublic: rig.macKey.publicKey.rawRepresentation)
        rig.poster.log.removeAll()
        XCTAssertNil(rig.runtime.handle(datagram: dg, from: RuntimeRig.addr))
        XCTAssertEqual(rig.poster.log, [])
    }

    func testReplayedPacketPostsOnce() throws {
        let dg = try client.session.seal(InputPacket(type: .input, dx: 16).encode(), now: rig.mono)
        rig.poster.log.removeAll()
        _ = rig.runtime.handle(datagram: dg, from: RuntimeRig.addr)
        _ = rig.runtime.handle(datagram: dg, from: RuntimeRig.addr)
        XCTAssertEqual(rig.poster.moves, 1)
    }

    /// Input from another address than the Screen Sharing peer is refused even
    /// with a valid key: the address match is part of the gate.
    func testInputFromAnAddressWithoutScreenSharingIsRefused() throws {
        let c = try rig.connect(from: RuntimeRig.otherAddr)
        rig.run(0.1)
        let posted = rig.posted { rig.send(c, InputPacket(type: .input, buttons: [.left], dx: 160), from: RuntimeRig.otherAddr) }
        XCTAssertEqual(posted, [])
    }
}
