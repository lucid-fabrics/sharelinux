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
        controller.onConfirm = { [unowned self] c, k in self.confirmed.append((c, k)) }
        controller.onReject = { [unowned self] in self.rejected += 1 }
    }

    func confirmStatus(code: String = "123456", device: String = "Desk PC", key: Data? = nil) -> PairingStatus {
        var s = PairingStatus()
        s.phase = .confirm(code: code, device: device)
        s.pendingKey = key ?? self.key
        return s
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
        XCTAssertEqual(controller.noticeText, PairingText.refusal(.windowCovered))
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
        XCTAssertEqual(controller.noticeText, PairingText.refusal(.windowCovered))
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
