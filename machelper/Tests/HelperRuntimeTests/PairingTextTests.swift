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
