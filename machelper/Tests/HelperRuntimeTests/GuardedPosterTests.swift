import XCTest

@testable import HelperCore
@testable import HelperRuntime

final class GuardedPosterTests: XCTestCase {
    func make() -> (GuardedPoster, RecordingPoster) {
        let rec = RecordingPoster()
        return (GuardedPoster(rec), rec)
    }

    func testPostsPassThroughUntilStopped() {
        let (g, rec) = make()
        g.postMove(dx: 1, dy: 2, held: [])
        g.postButton(.left, down: true, clickState: 1)
        g.postScroll(dx: 0, dy: 3)
        XCTAssertEqual(rec.log, ["move 1 2 held=0", "down 0 click=1", "scroll 0 3"])
        XCTAssertEqual(g.buttonsDown, [.left])
    }

    func testBeginStopReleasesEveryButtonItPutDownAndBlocksTheRest() {
        let (g, rec) = make()
        g.postButton(.left, down: true, clickState: 2)
        g.postButton(.right, down: true, clickState: 1)
        rec.log.removeAll()
        g.beginStop()
        XCTAssertEqual(rec.ups, 2)
        XCTAssertTrue(rec.log.contains("up 0") && rec.log.contains("up 1"))
        XCTAssertEqual(g.buttonsDown, [])
        rec.log.removeAll()
        g.postMove(dx: 5, dy: 5, held: [])
        g.postButton(.left, down: true, clickState: 1)
        g.postScroll(dx: 1, dy: 1)
        XCTAssertEqual(rec.log, [], "moves, presses and scrolls are dropped while stopped")
        XCTAssertEqual(g.buttonsDown, [], "a dropped press leaves nothing down")
    }

    func testTheControllersOwnReleaseAfterAnEmergencyOneIsNotPostedTwice() {
        let (g, rec) = make()
        g.postButton(.left, down: true, clickState: 1)
        g.beginStop()
        rec.log.removeAll()
        g.postButton(.left, down: false, clickState: 1)
        XCTAssertEqual(rec.log, [])
    }

    func testEveryBeginStopNeedsItsOwnEndStop() {
        let (g, rec) = make()
        g.beginStop(); g.beginStop()
        g.endStop()
        XCTAssertTrue(g.isStopped)
        g.postMove(dx: 1, dy: 1, held: [])
        XCTAssertEqual(rec.log, [])
        g.endStop()
        XCTAssertFalse(g.isStopped)
        g.endStop()  // surplus end does nothing
        g.postMove(dx: 1, dy: 1, held: [])
        XCTAssertEqual(rec.moves, 1)
    }

    /// A button-up of a button the poster never put down is not forwarded.
    func testAReleaseWithoutAPressIsDropped() {
        let (g, rec) = make()
        g.postButton(.middle, down: false, clickState: 1)
        XCTAssertEqual(rec.log, [])
    }

    func testTheHotkeyCannotBeEnabledWithoutAKillSwitchPath() {
        let rig = RuntimeRig()
        rig.runtime.cannotEnableReason = "The kill hotkey could not be registered."
        XCTAssertFalse(rig.runtime.setAllowRawMouse(true))
        XCTAssertFalse(rig.runtime.allowRawMouse)
        rig.runtime.cannotEnableReason = nil
        XCTAssertTrue(rig.runtime.setAllowRawMouse(true))
        XCTAssertTrue(rig.runtime.allowRawMouse)
    }

    func testTheBadgeShowsExactlyWhileEventsArePosted() {
        let rig = RuntimeRig()
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.1)
        XCTAssertNil(rig.runtime.state.badgeText)
        let c = try! rig.connect()
        XCTAssertNil(rig.runtime.state.badgeText, "connected but nothing posted yet")
        rig.send(c, InputPacket(type: .input, dx: 32, dy: 0))
        XCTAssertEqual(rig.runtime.state.badgeText, "Raw mouse active: " + HelperRuntime.fingerprintName(rig.linuxKey.publicKey.rawRepresentation))
        rig.runtime.killSwitch()
        XCTAssertNil(rig.runtime.state.badgeText)
        XCTAssertNil(RuntimeState(allowRawMouse: true, device: "x", injecting: false, lastDeny: nil).badgeText)
        XCTAssertEqual(RuntimeState(allowRawMouse: true, device: nil, injecting: true, lastDeny: nil).badgeText, "Raw mouse active: a device")
    }
}
