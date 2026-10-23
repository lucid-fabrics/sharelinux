import XCTest

@testable import HelperRuntime

final class PairingTextTests: XCTestCase {
    /// Follow-up audit, finding 5: both sides show both keys under the same two labels. The Go test
    /// (TestFingerprintLinesMatchTheMacWindow) pins the same string for the same two keys.
    func testBothFingerprintsAreShownUnderTheSameTwoLabelsAsOnTheLinuxDialog() {
        let mac = Data(repeating: 1, count: 32), computer = Data(repeating: 2, count: 32)
        XCTAssertEqual(
            PairingText.fingerprintLines(mac: mac, computer: computer),
            "This Mac: 72cd 6e84 22c4 07fb\nThis computer: 7587 7bb4 1d39 3b5f")
    }

    func allStrings() -> [String] {
        var out = [
            PairingText.windowTitle, PairingText.confirmButton, PairingText.rejectButton, PairingText.removeButton,
            PairingText.unlockButton, PairingText.devicesHeader, PairingText.noDevices,
            PairingText.removeQuestion("Desk PC"), PairingText.removeExplanation("Desk PC"),
            PairingText.unlockQuestion, PairingText.unlockExplanation,
        ]
        let phases: [PairingStatus.Phase] = [.closed, .waiting, .confirm(code: "123456", device: "Desk PC"), .lockedOut(seconds: 80_000)]
        let notices: [PairingStatus.Notice?] = [
            nil, .paired("Desk PC"), .rejected, .expired, .closedAfterFailures, .storeFailed("The Keychain did not accept the key."),
        ]
        for p in phases {
            for n in notices {
                for other in [false, true] {
                    var s = PairingStatus()
                    s.phase = p; s.notice = n; s.anotherDeviceTrying = other
                    out.append(PairingText.headline(s))
                    out += [PairingText.detail(s), PairingText.notice(s)].compactMap { $0 }
                }
            }
        }
        return out
    }

    func testCoveringAppNamesAreSanitisedAndQuoted() {
        let hostile = "Safe\u{202E}gnp.exe\u{2066}\u{2069}\u{200B}\n  App\t\u{7}"
        XCTAssertEqual(PairingText.namesList([hostile]), "\"Safegnp.exe App\"")
        let long = String(repeating: "x", count: 80)
        let capped = PairingText.cleanName(long)
        XCTAssertEqual(capped.count, 32)
        XCTAssertTrue(capped.hasSuffix("\u{2026}"))
        XCTAssertEqual(PairingText.cleanName(String(repeating: "y", count: 32)).count, 32)
        XCTAssertEqual(PairingText.cleanName("\u{202A}\u{202B}\u{202C}\u{202D}\u{202E}"), "")
        XCTAssertNil(PairingText.namesList(["\u{202E}", " "]))
    }

    func testCombiningMarkStacksAreCappedByScalars() {
        let stack = "e" + String(repeating: "\u{0301}", count: 500)
        XCTAssertLessThanOrEqual(PairingText.cleanName(stack).unicodeScalars.count, 32)
        XCTAssertLessThanOrEqual(PairingText.cleanName("a" + String(repeating: "\u{20DD}", count: 500)).unicodeScalars.count, 32)
    }

    func testQuoteMarksAreRemovedFromNames() {
        XCTAssertEqual(PairingText.cleanName("Ev\"il\u{201C}A\u{201D}\u{00AB}B\u{00BB}'C\u{2019}"), "EvilABC")
        XCTAssertEqual(PairingText.namesList(["a\" and \"b"]), "\"a and b\"")
    }

    func testTheCoveredNoticeNamesWhatCoversAndFallsBack() {
        XCTAssertEqual(
            PairingText.refusal(.windowCovered(by: ["Google Chrome"])),
            "\"Google Chrome\" is covering this window, so Confirm is off. Move it away and check the code again.")
        XCTAssertEqual(
            PairingText.refusal(.windowCovered(by: ["Slack", "Zoom"])),
            "\"Slack\" and \"Zoom\" are covering this window, so Confirm is off. Move them away and check the code again.")
        XCTAssertEqual(
            PairingText.refusal(.windowCovered(by: [])),
            "Another window is covering this one, so Confirm is off. Move the other window away and check the code again.")
        XCTAssertEqual(PairingText.refusal(.windowCovered(by: ["\n\u{7}"])), PairingText.refusal(.windowCovered(by: [])))
        XCTAssertFalse(PairingText.refusal(.windowCovered(by: ["A", "B", "C"])).contains("\u{2014}"))
    }

    func testNoEmDashesAndNoJargon() {
        for s in allStrings() {
            XCTAssertFalse(s.contains("\u{2014}"), s)
            XCTAssertFalse(s.lowercased().contains("noise") || s.lowercased().contains("curve") || s.lowercased().contains("handshake"), s)
            XCTAssertFalse(s.isEmpty)
        }
    }

    func testTheWordsMatchTheSituation() {
        var s = PairingStatus()
        s.phase = .confirm(code: "123456", device: "Desk PC")
        XCTAssertTrue(PairingText.detail(s)!.contains("Desk PC"))
        XCTAssertTrue(PairingText.detail(s)!.contains("Confirm") && PairingText.detail(s)!.contains("Reject"))
        s.anotherDeviceTrying = true
        XCTAssertEqual(PairingText.notice(s), "Another device is trying to pair. Wait for the first one to finish.")
        s.phase = .lockedOut(seconds: 23 * 3600)
        XCTAssertTrue(PairingText.detail(s)!.contains("about 23 hours"))
        XCTAssertEqual(PairingText.duration(3600 * 1.0), "about 60 minutes")
        XCTAssertEqual(PairingText.duration(30), "less than 2 minutes")
    }
}
