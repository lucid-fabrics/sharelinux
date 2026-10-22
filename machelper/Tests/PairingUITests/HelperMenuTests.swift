import AppKit
import XCTest

@testable import HelperRuntime
@testable import PairingUI

/// The menu and the key alerts: what they say, and when Reset is on offer.
final class HelperMenuTests: XCTestCase {
    private func entries(
        _ state: RuntimeState = RuntimeState(allowRawMouse: false, device: nil, injecting: false, lastDeny: nil),
        cannotEnable: String? = nil
    ) -> [HelperMenuEntry] {
        HelperMenuModel.entries(
            state: state, cannotEnable: cannotEnable, accessGranted: true, identity: "1a2b 3c4d 5e6f 7a8b",
            pairedList: "0011 2233 4455 6677")
    }
    private func entry(_ id: HelperMenuEntry.ID, in list: [HelperMenuEntry]) -> HelperMenuEntry {
        list.first { $0.id == id }!
    }

    func testTheMenuShowsBothFingerprintsAndTheResetItem() {
        let list = entries()
        XCTAssertEqual(entry(.keyMine, in: list).title, "This Mac's key: 1a2b 3c4d 5e6f 7a8b")
        XCTAssertEqual(entry(.keyList, in: list).title, "Paired list: 0011 2233 4455 6677")
        XCTAssertFalse(entry(.keyMine, in: list).enabled)
        XCTAssertEqual(entry(.reset, in: list).title, "Reset helper keys...")
        XCTAssertTrue(entry(.reset, in: list).enabled)
    }

    func testResetIsOffWhileASessionIsLive() {
        let live = entries(RuntimeState(allowRawMouse: true, device: "Desk PC", injecting: false, lastDeny: nil))
        XCTAssertFalse(entry(.reset, in: live).enabled)
        XCTAssertFalse(entry(.pair, in: live).enabled)
        XCTAssertTrue(entry(.reset, in: live).title.contains("turn off Allow raw mouse first"))
    }

    /// A Linux client sits in Exclusive mode for as long as it likes. With the switch off nothing it
    /// sends is acted on, so pairing and reset must stay open: the owner's way out is the switch.
    func testPairingAndResetAreOpenWhileASessionIsLiveButTheSwitchIsOff() {
        let idle = entries(RuntimeState(allowRawMouse: false, device: "Desk PC", injecting: false, lastDeny: .disabled))
        XCTAssertTrue(entry(.pair, in: idle).enabled)
        XCTAssertTrue(entry(.reset, in: idle).enabled)
        XCTAssertEqual(entry(.pair, in: idle).title, PairingText.pairMenuItem)
    }

    func testTheReviewItemShowsOnlyWhileTheLaunchReviewIsPending() {
        let pending = entries(RuntimeState(
            allowRawMouse: false, device: nil, injecting: false, lastDeny: nil, keysChanged: true, keysReviewPending: true))
        XCTAssertFalse(entry(.review, in: pending).hidden)
        XCTAssertEqual(entry(.state, in: pending).title, PairingText.keysReviewState)
        XCTAssertFalse(entry(.toggle, in: pending).enabled)
        XCTAssertTrue(entry(.review, in: entries()).hidden)
    }

    func testAfterKeysChangedTheStateSaysSoAndTheSwitchCannotBeTurnedOn() {
        let list = entries(RuntimeState(allowRawMouse: false, device: nil, injecting: false, lastDeny: nil, keysChanged: true))
        XCTAssertEqual(entry(.state, in: list).title, PairingText.keysChangedState)
        XCTAssertFalse(entry(.toggle, in: list).enabled)
        XCTAssertTrue(entry(.reset, in: list).enabled, "Reset is how the owner starts over")
    }

    func testTheRealMenuFollowsTheModel() {
        let list = entries()
        let menu = HelperMenu(target: self, actions: [:], entries: list)
        XCTAssertTrue(menu.visibleTitles.contains("Reset helper keys..."))
        XCTAssertTrue(menu.visibleTitles.contains("This Mac's key: 1a2b 3c4d 5e6f 7a8b"))
        menu.update(entries(cannotEnable: "the keys were reset"))
        XCTAssertTrue(menu.visibleTitles.first!.hasPrefix("Cannot switch on"))
    }

    func testTheAlertsUseTheFixedWordsAndResetDefaultsToCancel() {
        let changed = HelperDialogs.keysChanged()
        XCTAssertEqual(
            changed.informativeText,
            "The helper's keys changed outside the app. Paired devices were turned off. Pair again only if you did this yourself.")
        let reset = HelperDialogs.resetConfirm()
        XCTAssertEqual(reset.buttons.map(\.title), ["Cancel", "Reset keys"])
        XCTAssertTrue(reset.informativeText.contains("All Linux devices must pair again"))
        XCTAssertTrue(reset.buttons[1].hasDestructiveAction)
        XCTAssertEqual(reset.buttons[0].title, "Cancel", "the first button is the one Return presses")
    }

    func testEverythingRendersOffscreen() {
        for alert in [HelperDialogs.keysChanged(), HelperDialogs.resetConfirm(), HelperDialogs.resetRefused(), HelperDialogs.resetDone()] {
            XCTAssertNotNil(HelperDialogs.renderPNG(alert))
        }
        XCTAssertNotNil(HelperMenu.renderPNG(entries()))
    }
}
