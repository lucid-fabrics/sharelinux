import AppKit
import XCTest

@testable import HelperCore
@testable import HelperRuntime
@testable import PairingUI

/// The window's safety rules, driven without showing anything on screen.
@MainActor
final class PairingWindowTests: XCTestCase {
    var clock = 100.0
    var confirmed: [(String, Data)] = []
    var rejected = 0
    var controller: PairingWindowController!
    let key = Data(repeating: 7, count: 32)

    override func setUp() async throws {
        _ = NSApplication.shared
        clock = 100
        confirmed = []
        rejected = 0
        controller = PairingWindowController()
        controller.now = { [unowned self] in self.clock }
        controller.isFullyVisible = { _ in true }
        controller.coveringOwners = { _ in [] }
        controller.onConfirm = { [unowned self] c, k in self.confirmed.append((c, k)) }
        controller.onReject = { [unowned self] in self.rejected += 1 }
    }

    func confirmStatus(code: String = "123456", device: String = "Desk PC", key: Data? = nil) -> PairingStatus {
        var s = PairingStatus()
        s.phase = .confirm(code: code, device: device)
        s.pendingKey = key ?? self.key
        return s
    }

    func testTheWindowShowsThisMacsKeyAndTheComputersKey() {
        var s = confirmStatus(key: Data(repeating: 2, count: 32))
        s.ownKey = Data(repeating: 1, count: 32)
        controller.update(s)
        controller.renderPNG()
        XCTAssertEqual(controller.fingerprintText, "This Mac: 72cd 6e84 22c4 07fb\nThis computer: 7587 7bb4 1d39 3b5f")
    }

    // MARK: Enter must never confirm

    func testConfirmIsNotTheDefaultButtonAndHasNoKeyEquivalent() throws {
        controller.update(confirmStatus())
        let window = try XCTUnwrap(controller.window)
        XCTAssertEqual(controller.confirm.keyEquivalent, "", "Return would press it")
        XCTAssertNil(window.defaultButtonCell, "no default button at all")
        XCTAssertTrue(controller.confirm.refusesFirstResponder, "Space must not press it either")
        XCTAssertEqual(controller.reject.keyEquivalent, "\u{1b}", "Esc rejects, the safe direction")
    }

    func testPressingReturnWhileACodeIsShownConfirmsNothing() throws {
        controller.update(confirmStatus())
        clock += 5  // long past the one-second guard
        let window = try XCTUnwrap(controller.window)
        let event = try XCTUnwrap(
            NSEvent.keyEvent(
                with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: window.windowNumber,
                context: nil, characters: "\r", charactersIgnoringModifiers: "\r", isARepeat: false, keyCode: 36))
        _ = window.performKeyEquivalent(with: event)
        XCTAssertTrue(confirmed.isEmpty, "Return does not confirm a pairing")
    }

    func testEscRejects() throws {
        controller.update(confirmStatus())
        let window = try XCTUnwrap(controller.window)
        let event = try XCTUnwrap(
            NSEvent.keyEvent(
                with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: window.windowNumber,
                context: nil, characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53))
        _ = window.performKeyEquivalent(with: event)
        XCTAssertEqual(rejected, 1)
        XCTAssertTrue(confirmed.isEmpty)
    }

    func testEscCancelsAWaitingPairingAndOnlyThen() throws {
        var closed = 0
        controller.onClose = { closed += 1 }
        var waiting = PairingStatus()
        waiting.phase = .waiting
        controller.update(waiting)
        XCTAssertEqual(controller.reject.keyEquivalent, "", "no code, nothing to reject")
        let window = try XCTUnwrap(controller.window)
        let esc = try XCTUnwrap(
            NSEvent.keyEvent(
                with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: window.windowNumber,
                context: nil, characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53))
        _ = window.performKeyEquivalent(with: esc)
        XCTAssertGreaterThanOrEqual(closed, 1, "Esc ended the wait")
        XCTAssertEqual(rejected, 0)
    }

    // MARK: no focus stealing

    func testANewAttemptNeverShowsOrFocusesTheWindow() throws {
        let window = try XCTUnwrap(controller.window)
        XCTAssertFalse(window.isVisible)
        controller.update(confirmStatus())
        controller.update(confirmStatus(code: "654321", device: "Other"))
        XCTAssertFalse(window.isVisible, "a device arriving does not pop the window up")
        XCTAssertFalse(window.isKeyWindow)
    }

    // MARK: click guard

    func testAClickWithinOneSecondOfTheCodeAppearingIsIgnored() {
        controller.update(confirmStatus())
        clock += 0.4
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "0.4 s after the code appeared")
        clock += 0.7  // 1.1 s in total
        controller.confirmClicked()
        XCTAssertEqual(confirmed.count, 1)
        XCTAssertEqual(confirmed.first?.0, "123456")
        XCTAssertEqual(confirmed.first?.1, key)
    }

    func testTheOneSecondRestartsWhenTheCodeChanges() {
        controller.update(confirmStatus())
        clock += 30
        controller.update(confirmStatus(code: "999999", device: "Other", key: Data(repeating: 9, count: 32)))
        clock += 0.5
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "the new code has been up for half a second")
        clock += 0.6
        controller.confirmClicked()
        XCTAssertEqual(confirmed.first?.0, "999999")
        XCTAssertEqual(confirmed.first?.1, Data(repeating: 9, count: 32), "the key on screen goes with the click")
    }

    func testAnUnchangedStatusDoesNotRestartTheClock() {
        controller.update(confirmStatus())
        clock += 0.7
        controller.update(confirmStatus())  // same code, same device: an unrelated redraw
        clock += 0.4
        controller.confirmClicked()
        XCTAssertEqual(confirmed.count, 1)
    }

    // MARK: covered window

    func testConfirmIsRefusedWithAReasonWhileAnotherWindowCoversTheWindow() {
        controller.isFullyVisible = { _ in false }
        controller.update(confirmStatus())
        clock += 5
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty)
        XCTAssertEqual(controller.noticeText, PairingText.refusal(.windowCovered(by: [])))
        XCTAssertTrue(controller.noticeText.lowercased().contains("covering"))
        controller.isFullyVisible = { _ in true }
        controller.confirmClicked()
        XCTAssertEqual(confirmed.count, 1, "once it is uncovered, Confirm works")
    }

    /// Follow-up audit, finding 9: the check ran only at mouse-up and passed when the window list was unavailable.
    func testAnUnavailableWindowListIsNotAPass() {
        let frame = CGRect(x: 0, y: 0, width: 400, height: 300)
        XCTAssertFalse(WindowCoverage.verdict(onScreen: true, others: nil, topLeftFrame: frame), "fail closed")
        XCTAssertFalse(WindowCoverage.verdict(onScreen: false, others: [], topLeftFrame: frame))
        XCTAssertTrue(WindowCoverage.verdict(onScreen: true, others: [], topLeftFrame: frame))
        XCTAssertFalse(
            WindowCoverage.verdict(onScreen: true, others: [.init(frame: CGRect(x: 100, y: 100, width: 50, height: 50), alpha: 1)], topLeftFrame: frame))
    }

    func testAWindowThatCoveredConfirmAtMouseDownBlocksTheClickEvenIfItMovedAwayBeforeMouseUp() {
        controller.update(confirmStatus())
        clock += 5
        controller.isFullyVisible = { _ in false }
        controller.confirmMouseDown()
        controller.isFullyVisible = { _ in true }  // uncovered by mouse-up
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "covered at mouse-down")
        XCTAssertEqual(controller.noticeText, PairingText.refusal(.windowCovered(by: [])))
    }

    func testAWindowThatCoversConfirmAtMouseUpBlocksTheClickEvenIfClearAtMouseDown() {
        controller.update(confirmStatus())
        clock += 5
        controller.confirmMouseDown()  // clear
        controller.isFullyVisible = { _ in false }
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "covered at mouse-up")
    }

    func testAClearPressConfirmsOnceAndAStalePressIsForgotten() {
        controller.update(confirmStatus())
        clock += 5
        controller.isFullyVisible = { _ in false }
        controller.confirmMouseDown()
        controller.confirmClicked()  // refused, press consumed
        controller.isFullyVisible = { _ in true }
        controller.confirmMouseDown()
        controller.confirmClicked()
        XCTAssertEqual(confirmed.count, 1)
    }

    // MARK: Apple system overlays

    private let full = CGRect(x: 0, y: 0, width: 5120, height: 1440)
    private let mine = CGRect(x: 100, y: 100, width: 420, height: 300)

    private func verifier(_ trusted: Set<Int32>) -> (AppleOverlayVerifier, () -> Int) {
        var calls = 0
        let v = AppleOverlayVerifier(check: { pid in calls += 1; return trusted.contains(pid) }, startTime: { _ in 1 })
        return (v, { calls })
    }

    func testAVerifiedAppleOverlayDoesNotCover() {
        let (v, _) = verifier([10, 11])
        let others = [
            WindowCoverage.Other(frame: full, alpha: 1, pid: 10, ownerName: "Dock", layer: 20),
            WindowCoverage.Other(frame: full, alpha: 1, pid: 11, ownerName: "Notification Center", layer: 21),
        ]
        XCTAssertFalse(WindowCoverage.isCovered(mine, by: others, verifier: v, screens: [full]))
        XCTAssertTrue(WindowCoverage.verdict(onScreen: true, others: others, topLeftFrame: mine, verifier: v, screens: [full]))
    }

    func testTheSameNameFromAnUnverifiedPidStillCovers() {
        let (v, _) = verifier([10])
        let fake = WindowCoverage.Other(frame: full, alpha: 1, pid: 999, ownerName: "Dock", layer: 20)
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: [fake], verifier: v, screens: [full]))
        XCTAssertEqual(WindowCoverage.ownerNames(of: WindowCoverage.covering(mine, by: [fake], verifier: v, screens: [full])), ["Dock"])
    }

    func testAnAppleAppThatIsNotAnOverlayStillCoversEvenWhenItsPidIsVerified() {
        let (v, _) = verifier([20])
        let other = WindowCoverage.Other(frame: mine, alpha: 1, pid: 20, ownerName: "Safari")
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: [other], verifier: v), "only the overlay processes are ignorable")
    }

    func testANormalAppWindowOverlappingStillCoversAndIsNamed() {
        let (v, _) = verifier([10])
        let others = [
            WindowCoverage.Other(frame: full, alpha: 1, pid: 10, ownerName: "Dock", layer: 20),
            WindowCoverage.Other(frame: CGRect(x: 300, y: 200, width: 800, height: 600), alpha: 1, pid: 50, ownerName: "Google Chrome"),
            WindowCoverage.Other(frame: CGRect(x: 320, y: 220, width: 100, height: 100), alpha: 1, pid: 50, ownerName: "Google Chrome"),
        ]
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: others, verifier: v, screens: [full]))
        XCTAssertEqual(WindowCoverage.ownerNames(of: WindowCoverage.covering(mine, by: others, verifier: v, screens: [full])), ["Google Chrome"])
    }

    func testAnUnidentifiedWindowCoversAndTheVerdictIsCachedPerPid() {
        let (v, calls) = verifier([10])
        let unknown = WindowCoverage.Other(frame: full, alpha: 1)
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: [unknown], verifier: v, screens: [full]), "no pid, fail closed")
        let dock = WindowCoverage.Other(frame: full, alpha: 1, pid: 10, ownerName: "Dock", layer: 20)
        for _ in 0..<5 { XCTAssertFalse(WindowCoverage.isCovered(mine, by: [dock], verifier: v, screens: [full])) }
        XCTAssertEqual(calls(), 1)
    }

    /// The real signature check on real processes: the Dock (when running) is Apple's, this test process is not.
    func testTheSystemCheckKnowsTheRealDockAndNotThisProcess() throws {
        XCTAssertFalse(AppleOverlayVerifier.systemCheck(ProcessInfo.processInfo.processIdentifier))
        XCTAssertFalse(AppleOverlayVerifier.systemCheck(0))
        let dock = try XCTUnwrap(
            NSWorkspace.shared.runningApplications.first { $0.bundleIdentifier == "com.apple.dock" },
            "no Dock running on this machine")
        XCTAssertTrue(AppleOverlayVerifier.systemCheck(dock.processIdentifier))
    }

    // MARK: audit findings

    /// Finding 3: only a full-display overlay is ignorable, whoever owns it.
    func testAVerifiedNotificationBannerSizedWindowOverTheCodeStillCovers() {
        let (v, _) = verifier([10, 11])
        let banner = WindowCoverage.Other(
            frame: CGRect(x: 80, y: 90, width: 360, height: 100), alpha: 1, pid: 11, ownerName: "Notification Center", layer: 23)
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: [banner], verifier: v, screens: [full]))
        let tile = WindowCoverage.Other(frame: mine, alpha: 1, pid: 10, ownerName: "Dock", layer: 20)
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: [tile], verifier: v, screens: [full]))
        // Full size but at an ordinary layer is not the overlay either.
        let flat = WindowCoverage.Other(frame: full, alpha: 1, pid: 10, ownerName: "Dock", layer: 0)
        XCTAssertTrue(WindowCoverage.isCovered(mine, by: [flat], verifier: v, screens: [full]))
    }

    func testAnOverlayCoveringTheUnionOfTwoDisplaysIsIgnorable() {
        let (v, _) = verifier([10])
        let left = CGRect(x: 0, y: 0, width: 2560, height: 1440), right = CGRect(x: 2560, y: 0, width: 2560, height: 1440)
        let both = WindowCoverage.Other(frame: full, alpha: 1, pid: 10, ownerName: "Dock", layer: 20)
        XCTAssertFalse(WindowCoverage.isCovered(mine, by: [both], verifier: v, screens: [left, right]))
        let oneScreen = WindowCoverage.Other(frame: left, alpha: 1, pid: 10, ownerName: "Dock", layer: 20)
        XCTAssertFalse(WindowCoverage.isCovered(mine, by: [oneScreen], verifier: v, screens: [left, right]))
    }

    /// Finding 2: a reused pid does not inherit the earlier process's verdict.
    func testAReusedPidIsVerifiedAgain() {
        var start: UInt64 = 100, calls = 0, trusted = true
        let v = AppleOverlayVerifier(check: { _ in calls += 1; return trusted }, startTime: { _ in start })
        XCTAssertTrue(v.isVerifiedOverlay(pid: 10, ownerName: "Dock"))
        XCTAssertTrue(v.isVerifiedOverlay(pid: 10, ownerName: "Dock"))
        XCTAssertEqual(calls, 1)
        start = 200  // the pid now belongs to a different process
        trusted = false
        XCTAssertFalse(v.isVerifiedOverlay(pid: 10, ownerName: "Dock"))
        XCTAssertEqual(calls, 2)
    }

    func testAnUnknownStartTimeOrOneThatMovesDuringTheCheckIsUnverified() {
        let none = AppleOverlayVerifier(check: { _ in true }, startTime: { _ in nil })
        XCTAssertFalse(none.isVerifiedOverlay(pid: 10, ownerName: "Dock"))
        var start: UInt64 = 1
        let moving = AppleOverlayVerifier(check: { _ in start += 1; return true }, startTime: { _ in start })
        XCTAssertFalse(moving.isVerifiedOverlay(pid: 10, ownerName: "Dock"))
    }

    func testTheRealStartTimeOfThisProcessIsKnown() {
        XCTAssertNotNil(AppleOverlayVerifier.systemStartTime(ProcessInfo.processInfo.processIdentifier))
        XCTAssertNil(AppleOverlayVerifier.systemStartTime(0x7ffffff0))
    }

    /// Finding 1: the press-time verdict is taken before the press raises the window.
    func testCoverageIsJudgedBeforeThePressRaisesTheWindow() throws {
        let window = try XCTUnwrap(controller.window as? FrontOnClickWindow)
        controller.update(confirmStatus())
        clock += 5
        var covered = true
        controller.isFullyVisible = { _ in !covered }
        window.sourcePid = { _ in 0 }
        window.onPress = { covered = false }  // the raise happens, and uncovers it
        let down = try XCTUnwrap(
            NSEvent.mouseEvent(
                with: .leftMouseDown, location: NSPoint(x: 10, y: 10), modifierFlags: [], timestamp: 0,
                windowNumber: window.windowNumber, context: nil, eventNumber: 1, clickCount: 1, pressure: 1))
        window.sendEvent(down)
        XCTAssertFalse(covered)
        controller.confirmMouseDown()  // the button's mouse-down, after the raise
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "covered at the press, so Confirm is refused")
        // A clear press afterwards works.
        window.sendEvent(down)
        controller.confirmMouseDown()
        controller.confirmClicked()
        XCTAssertEqual(confirmed.count, 1)
    }

    /// A press skipped by sendEvent must fail closed, not reuse an older clear verdict.
    func testASkippedPressFailsClosedInsteadOfReusingAnOlderClearVerdict() throws {
        let window = try XCTUnwrap(controller.window as? FrontOnClickWindow)
        controller.update(confirmStatus())
        clock += 5
        var covered = false
        controller.isFullyVisible = { _ in !covered }
        let down = try XCTUnwrap(
            NSEvent.mouseEvent(
                with: .leftMouseDown, location: NSPoint(x: 10, y: 10), modifierFlags: [], timestamp: 0,
                windowNumber: window.windowNumber, context: nil, eventNumber: 1, clickCount: 1, pressure: 1))
        window.sourcePid = { _ in 0 }
        window.sendEvent(down)  // clear press on the background
        covered = true
        controller.isSessionLive = { true }
        window.sendEvent(down)  // skipped path
        covered = false  // even if it looks clear by the time Confirm sees it
        controller.confirmMouseDown()
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "the skipped press never vouched for the window")
    }

    func testMouseUpPhaseChangeAndCloseClearTheStoredVerdict() throws {
        let window = try XCTUnwrap(controller.window as? FrontOnClickWindow)
        controller.update(confirmStatus())
        clock += 5
        var covered = false
        controller.isFullyVisible = { _ in !covered }
        window.sourcePid = { _ in 0 }
        func press() throws {
            covered = false
            window.sendEvent(try XCTUnwrap(NSEvent.mouseEvent(
                with: .leftMouseDown, location: NSPoint(x: 10, y: 10), modifierFlags: [], timestamp: 0,
                windowNumber: window.windowNumber, context: nil, eventNumber: 1, clickCount: 1, pressure: 1)))
            covered = true  // a stale verdict would say clear; a fresh check says covered
        }
        try press()
        window.sendEvent(try XCTUnwrap(NSEvent.mouseEvent(
            with: .leftMouseUp, location: NSPoint(x: 10, y: 10), modifierFlags: [], timestamp: 0,
            windowNumber: window.windowNumber, context: nil, eventNumber: 2, clickCount: 1, pressure: 1)))
        controller.confirmMouseDown()
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "mouse-up cleared the press, so the fresh check sees the cover")
        try press()
        controller.update(PairingStatus())  // phase change
        controller.update(confirmStatus())
        clock += 5
        controller.confirmMouseDown()
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "phase change cleared the press")
        try press()
        controller.windowWillClose(Notification(name: NSWindow.willCloseNotification))
        controller.confirmMouseDown()
        controller.confirmClicked()
        XCTAssertTrue(confirmed.isEmpty, "close cleared the press")
    }

    /// Finding 6: the helper's own injected clicks, or any click while a session is live, never raise it.
    func testAnInjectedClickNeverRaisesTheWindow() throws {
        let window = try XCTUnwrap(controller.window as? FrontOnClickWindow)
        var presses = 0
        window.onPress = { presses += 1 }
        let down = try XCTUnwrap(
            NSEvent.mouseEvent(
                with: .leftMouseDown, location: NSPoint(x: 10, y: 10), modifierFlags: [], timestamp: 0,
                windowNumber: window.windowNumber, context: nil, eventNumber: 1, clickCount: 1, pressure: 1))
        window.sourcePid = { _ in ProcessInfo.processInfo.processIdentifier }
        window.sendEvent(down)
        XCTAssertEqual(presses, 0, "injected by this process")
        window.sourcePid = { _ in 0 }
        controller.isSessionLive = { true }
        window.sendEvent(down)
        XCTAssertEqual(presses, 0, "a session is live")
        controller.isSessionLive = { false }
        window.sendEvent(down)
        XCTAssertEqual(presses, 1, "the person's own click still raises it")
    }

    func testTheCoveredNoticeNamesTheApp() {
        controller.isFullyVisible = { _ in false }
        controller.coveringOwners = { _ in ["Google Chrome"] }
        controller.update(confirmStatus())
        clock += 5
        controller.confirmClicked()
        XCTAssertEqual(
            controller.noticeText,
            "\"Google Chrome\" is covering this window, so Confirm is off. Move it away and check the code again.")
    }

    // MARK: coming to the front

    func testAMousePressInTheWindowBringsItToTheFrontButAnUpdateDoesNot() throws {
        let window = try XCTUnwrap(controller.window as? FrontOnClickWindow)
        var presses = 0
        window.onPress = { presses += 1 }
        window.sourcePid = { _ in 0 }  // real hardware reports pid 0; a synthetic event carries this process
        controller.update(confirmStatus())
        XCTAssertEqual(presses, 0, "a device arriving never pulls focus")
        let down = try XCTUnwrap(
            NSEvent.mouseEvent(
                with: .leftMouseDown, location: NSPoint(x: 10, y: 10), modifierFlags: [], timestamp: 0,
                windowNumber: window.windowNumber, context: nil, eventNumber: 1, clickCount: 1, pressure: 1))
        window.sendEvent(down)
        XCTAssertEqual(presses, 1)
    }

    func testCoverageGeometry() {
        let me = CGRect(x: 100, y: 100, width: 420, height: 300)
        typealias O = WindowCoverage.Other
        XCTAssertTrue(WindowCoverage.isCovered(me, by: [O(frame: CGRect(x: 0, y: 0, width: 1000, height: 1000), alpha: 1)]))
        XCTAssertTrue(WindowCoverage.isCovered(me, by: [O(frame: CGRect(x: 500, y: 390, width: 50, height: 50), alpha: 0.5)]), "a corner counts")
        XCTAssertFalse(WindowCoverage.isCovered(me, by: [O(frame: CGRect(x: 600, y: 0, width: 50, height: 50), alpha: 1)]))
        XCTAssertFalse(WindowCoverage.isCovered(me, by: [O(frame: me, alpha: 0)]), "an invisible window shows nothing")
        XCTAssertFalse(WindowCoverage.isCovered(me, by: []))
    }

    // MARK: layout

    func testWidthIsFixedHeightFitsAndTheWindowIsNotResizable() throws {
        let window = try XCTUnwrap(controller.window)
        XCTAssertFalse(window.styleMask.contains(.resizable))
        XCTAssertTrue(window.styleMask.contains(.closable))
        XCTAssertEqual(window.title, "Pair a Linux device")
        var closed = PairingStatus()
        controller.update(closed)
        let small = window.contentLayoutRect.height
        XCTAssertEqual(window.contentLayoutRect.width, 420)
        closed.devices = (0..<3).map { PairedDevice(name: "Device \($0)", key: Data(repeating: UInt8($0), count: 32)) }
        controller.update(closed)
        XCTAssertGreaterThan(window.contentLayoutRect.height, small, "more devices, taller window")
        XCTAssertEqual(window.contentLayoutRect.width, 420)
    }
}
