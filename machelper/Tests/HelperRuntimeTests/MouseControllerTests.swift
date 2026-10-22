import XCTest

@testable import HelperCore
@testable import HelperRuntime

final class MouseControllerTests: XCTestCase {
    var now = 100.0
    var poster = RecordingPoster()
    var controller: MouseController!
    var changes: [Bool] = []

    override func setUp() {
        now = 100
        poster = RecordingPoster()
        changes = []
        controller = MouseController(poster: poster, clock: { [unowned self] in self.now }, doubleClickInterval: { 0.5 })
        controller.onInjectingChange = { [unowned self] in self.changes.append($0) }
    }

    func input(_ buttons: ButtonMask = [], dx: Int16 = 0, dy: Int16 = 0, sx: Int16 = 0, sy: Int16 = 0) -> InputPacket {
        InputPacket(type: .input, buttons: buttons, dx: dx, dy: dy, sx: sx, sy: sy)
    }

    // The latch: after a release the Linux side may still be holding, and that is no new press.

    func testAHeldMaskAfterAReleaseIsNotAPressUntilAllUpIsSeen() {
        controller.apply(input([.left]))
        XCTAssertEqual(poster.downs, 1)
        controller.releaseAll(.denied)
        poster.log.removeAll()
        for _ in 0..<5 { controller.apply(input([.left], dx: 16)) }  // the repeat of the held mask
        XCTAssertEqual(poster.downs, 0, "a held mask after a deny must not press again")
        XCTAssertEqual(controller.held, [])
        controller.apply(input())  // all up seen
        controller.apply(input([.left]))
        XCTAssertEqual(poster.downs, 1, "a fresh press after all-up is a real one")
    }

    func testANewSessionNeedsAllUpBeforeItsFirstPress() {
        controller.requireUpFirst()
        controller.apply(input([.left]))
        XCTAssertEqual(poster.downs, 0)
        controller.apply(input())
        controller.apply(input([.left]))
        XCTAssertEqual(poster.downs, 1)
    }

    func testSubPixelRemainderCarriesBetweenPackets() {
        for _ in 0..<16 { controller.apply(input(dx: 1)) }
        XCTAssertEqual(poster.log, ["move 1 0 held=0"], "sixteen sixteenths make one point, once")
        poster.log.removeAll()
        // Negative values truncate toward zero and keep their remainder.
        for _ in 0..<31 { controller.apply(input(dx: -1, dy: 1)) }
        XCTAssertEqual(poster.log, ["move -1 1 held=0"], "31 sixteenths: one point, 15 left over")
    }

    func testWholePointsAndRemainderTogether() {
        controller.apply(input(dx: 40, dy: -40))  // 2.5 and -2.5 points
        XCTAssertEqual(poster.log, ["move 2 -2 held=0"])
        controller.apply(input(dx: 8, dy: -8))  // the halves meet
        XCTAssertEqual(poster.log, ["move 2 -2 held=0", "move 1 -1 held=0"])
    }

    func testMotionComesBeforeButtonChangesAndScrollLast() {
        controller.apply(input([.left], dx: 16, sy: 5))
        XCTAssertEqual(poster.log, ["move 1 0 held=0", "down 0 click=1", "scroll 0 5"])
        poster.log.removeAll()
        controller.apply(input([], dx: 16))
        XCTAssertEqual(poster.log, ["move 1 0 held=1", "up 0"], "the drag ends after its last move")
    }

    func testButtonsFollowTheFullMask() {
        controller.apply(input([.left, .right]))
        XCTAssertEqual(poster.log, ["down 0 click=1", "down 1 click=1"])
        poster.log.removeAll()
        controller.apply(input([.left, .right]))  // same state again: nothing
        XCTAssertEqual(poster.log, [])
        controller.apply(input([.right, .middle, .back, .forward]))
        XCTAssertEqual(poster.log, ["up 0", "down 2 click=1", "down 3 click=1", "down 4 click=1"])
        XCTAssertEqual(controller.held, [.right, .middle, .back, .forward])
    }

    func testDoubleClickCountsWithinTheIntervalAndResetsAfterwards() {
        func click() {
            controller.apply(input([.left]))
            controller.apply(input([]))
        }
        click()
        now += 0.3
        click()
        now += 0.3
        click()
        XCTAssertEqual(poster.log.filter { $0.hasPrefix("down") }, ["down 0 click=1", "down 0 click=2", "down 0 click=3"])
        now += 0.6
        poster.log.removeAll()
        click()
        XCTAssertEqual(poster.log.first, "down 0 click=1", "too slow")
        // Moving far between presses starts over.
        now += 0.1
        controller.apply(input([], dx: 16 * 10))
        poster.log.removeAll()
        click()
        XCTAssertEqual(poster.log.first, "down 0 click=1", "moved more than the slop")
        // Another button starts over too.
        now += 0.1
        poster.log.removeAll()
        controller.apply(input([.right]))
        XCTAssertEqual(poster.log, ["down 1 click=1"])
    }

    func testDragTypeDependsOnTheButtonsHeldBeforeThePacket() {
        controller.apply(input([.right], dx: 16))
        controller.apply(input([.right], dx: 16))
        XCTAssertEqual(poster.log, ["move 1 0 held=0", "down 1 click=1", "move 1 0 held=2"])
    }

    func testReleaseAllIsOncePerButtonAndIdempotent() {
        controller.apply(input([.left, .middle], dx: 7))
        poster.log.removeAll()
        controller.releaseAll(.killSwitch)
        XCTAssertEqual(poster.log, ["up 0", "up 2"])
        controller.releaseAll(.denied)
        controller.releaseAll(.ended)
        XCTAssertEqual(poster.log, ["up 0", "up 2"])
        XCTAssertEqual(controller.held, [])
        // The motion remainder is forgotten: a new session starts clean.
        controller.apply(input(dx: 9))
        XCTAssertEqual(poster.moves, 0, "the 7/16 from before must not add up with the 9/16 of the new session")
    }

    func testIndicatorFollowsActivity() {
        XCTAssertFalse(controller.injecting)
        controller.apply(input(dx: 1))
        XCTAssertTrue(controller.injecting)
        controller.apply(input(dx: 1))
        XCTAssertEqual(changes, [true], "only changes are reported")
        controller.releaseAll(.ended)
        XCTAssertFalse(controller.injecting)
        XCTAssertEqual(changes, [true, false])
    }

    /// M2: a link that dropped its input (the "all up" was lost) still carries keepalives. They keep
    /// the session alive but must never keep a button down: only input packets do.
    func testKeepalivesDoNotKeepAHeldButtonDown() {
        controller.apply(input([.left]))
        for _ in 0..<12 {  // a keepalive every 100 ms for 1.2 s
            now += 0.1
            controller.touch()
            controller.tick()
        }
        XCTAssertEqual(controller.held, [], "released within the silence timeout despite the keepalives")
        XCTAssertEqual(poster.log.last, "up 0")
        XCTAssertEqual(controller.releaseCount, 1)
    }

    func testRepeatedInputPacketsKeepAHeldButtonDownForAsLongAsTheyComeAndKeepalivesKeepTheIndicator() {
        controller.apply(input([.left]))
        for _ in 0..<20 {  // the Linux side repeats the held state every 250 ms
            now += 0.25
            controller.apply(input([.left]))
            controller.touch()
            controller.tick()
        }
        XCTAssertEqual(controller.held, [.left])
        // A keepalive alone still keeps an idle session's indicator for the silence timeout.
        controller.releaseAll(.ended)
        controller.apply(input([]))
        now += ChannelLimits.silenceTimeout - 0.1
        controller.touch()
        controller.tick()
        XCTAssertTrue(controller.injecting)
    }

    func testWatchdogTreatsAClockThatWentBackwardsAsSilence() {
        controller.apply(input([.left]))
        now -= 5
        controller.tick()
        XCTAssertEqual(controller.held, [])
    }
}

final class PostingGeometryTests: XCTestCase {
    typealias P = PostingGeometry.Point
    typealias R = PostingGeometry.Rect
    let main = R(x: 0, y: 0, width: 1920, height: 1080)

    func testInsidePointIsUnchanged() {
        XCTAssertEqual(PostingGeometry.clamp(P(x: 10.5, y: 20), to: [main]), P(x: 10.5, y: 20))
    }

    func testOutsidePointStopsAtTheLastPixel() {
        XCTAssertEqual(PostingGeometry.clamp(P(x: 5000, y: 5000), to: [main]), P(x: 1919, y: 1079))
        XCTAssertEqual(PostingGeometry.clamp(P(x: -3, y: -9), to: [main]), P(x: 0, y: 0))
    }

    func testNearestOfSeveralDisplaysWins() {
        let right = R(x: 1920, y: 0, width: 1280, height: 720)
        let displays = [main, right]
        XCTAssertEqual(PostingGeometry.clamp(P(x: 2000, y: 750), to: displays), P(x: 2000, y: 719))
        XCTAssertEqual(PostingGeometry.clamp(P(x: 1925, y: 100), to: displays), P(x: 1925, y: 100))
        // In the gap below the smaller display, the nearest edge is the small one's bottom.
        XCTAssertEqual(PostingGeometry.clamp(P(x: 3000, y: 1000), to: displays), P(x: 3000, y: 719))
    }

    func testNegativeOriginDisplays() {
        let left = R(x: -1440, y: -200, width: 1440, height: 900)
        XCTAssertEqual(PostingGeometry.clamp(P(x: -2000, y: 0), to: [left, main]), P(x: -1440, y: 0))
    }

    func testNoDisplaysLeavesThePointAlone() {
        XCTAssertEqual(PostingGeometry.clamp(P(x: 7, y: 8), to: []), P(x: 7, y: 8))
    }
}
