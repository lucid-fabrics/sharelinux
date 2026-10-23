import Foundation
import HelperCore

/// The words of the pairing window, kept apart from AppKit so they can be
/// tested (plain language, no em-dashes, no key material).
public enum PairingText {
    public static let windowTitle = "Pair a Linux device"
    public static let confirmButton = "Confirm"
    public static let rejectButton = "Reject"
    public static let removeButton = "Remove"
    public static let unlockButton = "Unlock pairing..."
    public static let devicesHeader = "Paired devices"
    public static let noDevices = "No paired devices yet"
    public static let waitingLine = "Waiting for a Linux device..."
    public static let cancelButton = "Cancel"
    public static let pairMenuItem = "Pair a Linux device..."
    public static let pairMenuItemBusy = "Pair a Linux device... (turn off Allow raw mouse first)"

    /// "1a2b 3c4d 5e6f 7a8b": the same string the Linux side shows for the key.
    public static func fingerprint(_ key: Data) -> String { DeviceIdentity.fingerprint(key) }

    /// Both keys, worded the same on both screens ("This Mac" is the Mac, "This computer" is the
    /// Linux machine), so the person can read the two lines on the Mac and on the Linux dialog
    /// and see that they are the same.
    public static func fingerprintLines(mac: Data, computer: Data) -> String {
        "This Mac: \(fingerprint(mac))\nThis computer: \(fingerprint(computer))"
    }

    // MARK: keys changed outside the app, fingerprints, reset (design 2.10, 3.7)

    public static let keysChangedTitle = "The helper's keys changed"
    public static let keysChangedAlert =
        "The helper's keys changed outside the app. Paired devices were turned off. Pair again only if you did this yourself."
    public static let resetMenuItem = "Reset helper keys..."

    // MARK: keys that changed while the helper was not running (checked at launch)

    public static let launchChangeTitle = "The helper's keys changed since it last ran"
    public static let keepPairedButton = "Keep paired devices"
    public static let leaveOffButton = "Leave the helper off"
    public static let reviewMenuItem = "Review changed keys..."
    public static let keysReviewState = "Keys changed since the last run: review needed"
    public static let unknownFingerprint = "not known"

    /// The review dialog's text. It lists what the last run left behind next to what is there now,
    /// and says what each choice does. Until a choice is made, no Linux device can use the helper.
    public static func launchChangeText(
        identityBefore: String?, identityNow: String, pairedListBefore: String?, pairedListNow: String
    ) -> String {
        let a = identityBefore ?? unknownFingerprint, b = pairedListBefore ?? unknownFingerprint
        return """
            While the helper was not running, its keys were not left as the last run saw them. No Linux device can use the helper until you decide.

            This Mac's key: was \(a), now \(identityNow)
            Paired list: was \(b), now \(pairedListNow)

            If you paired, removed or reset devices yourself, choose Keep paired devices. If you did not, choose Reset helper keys... and pair your devices again. Leave the helper off keeps everything stopped for now.
            """
    }
    public static let resetMenuItemBusy = "Reset helper keys... (turn off Allow raw mouse first)"
    public static let resetQuestion = "Reset the helper's keys?"
    public static let resetExplanation =
        "This deletes the helper's key and the list of paired devices, and starts fresh. All Linux devices must pair again. Do this if the helper says its keys are damaged, or if you did not expect them to change."
    public static let resetConfirmButton = "Reset keys"
    public static let resetRefusedTitle = "Keys not reset"
    public static let resetRefused =
        "A Linux device could move the mouse right now, so the keys cannot be reset. Turn off Allow raw mouse in the menu bar menu, then try again."
    public static let resetDoneTitle = "Helper keys reset"
    public static let resetDone =
        "The helper's keys were deleted. All Linux devices must pair again. The helper will now quit. Open it again to start fresh."
    public static let resetFailed = "The Keychain did not let go of the keys, so nothing was reset."
    public static let resetBlocksSwitch = "the keys were reset, quit and open the helper again"
    /// The pairing window and the menu, when the list of removed devices cannot be trusted.
    public static let damagedNotice =
        "The helper's list of removed devices is damaged, so no paired device is trusted and no new one can be added. Choose Reset helper keys... in the menu bar menu to start fresh. All Linux devices must pair again."
    public static let damagedShort =
        "The list of removed devices is damaged. Choose Reset helper keys... in the menu bar menu."
    public static let keysChangedNotice =
        "Nothing was saved: the helper's keys changed outside the app. Restart the helper. Pair again only if you did this yourself."
    public static let keysChangedState = "Keys changed outside the app"

    /// The two lines in the menu, so a change shows from one launch to the next. The first is this
    /// Mac's key (the same string the Linux side shows for it); the second changes whenever the
    /// paired list or the removed-devices list changes, which the app itself does when you pair or
    /// remove a device.
    public static func keyMenuLines(identity: String, pairedList: String) -> (String, String) {
        ("This Mac's key: \(identity)", "Paired list: \(pairedList)")
    }

    /// One app name from another process, made safe to show: control and format characters
    /// (newlines, bidi overrides U+202A-202E and U+2066-2069, zero-width marks) dropped,
    /// whitespace collapsed, capped at 32 characters with an ellipsis. Empty when nothing is left.
    static func cleanName(_ name: String) -> String {
        let kept = name.unicodeScalars.filter { scalar in
            switch scalar.properties.generalCategory {
            case .control, .format, .lineSeparator, .paragraphSeparator: return false
            default: return true
            }
        }
        let words = String(String.UnicodeScalarView(kept)).split(whereSeparator: { $0.isWhitespace })
        let line = words.joined(separator: " ")
        guard line.count > 32 else { return line }
        return String(line.prefix(31)) + "\u{2026}"
    }

    /// "\"A\"", "\"A\" and \"B\"", "\"A\", \"B\" and \"C\"" (names are from other processes, so each
    /// is cleaned and quoted); nil when there is no usable name.
    static func namesList(_ names: [String]) -> String? {
        let n = names.map(cleanName).filter { !$0.isEmpty }.map { "\"\($0)\"" }
        switch n.count {
        case 0: return nil
        case 1: return n[0]
        default: return n.dropLast().joined(separator: ", ") + " and " + n[n.count - 1]
        }
    }

    /// One sentence for a refused Confirm.
    public static func refusal(_ r: ConfirmRefusal) -> String {
        switch r {
        case .noAttempt, .staleAttempt:
            return "The code changed or went away before your click. Check the code on screen and try again."
        case .tooSoon:
            return "That click came too soon after the code appeared, so it was ignored. Check the code, then click Confirm."
        case .sessionActive:
            return "A Linux device could move the mouse right now. Turn off Allow raw mouse in the menu bar menu, then pair."
        case .nameCollision(let other):
            return "A device called \(other) is already paired and this name looks the same. Reject, rename this computer on the Linux side and pair again."
        case .windowCovered(let apps):
            guard let list = namesList(apps) else {
                return "Another window is covering this one, so Confirm is off. Move the other window away and check the code again."
            }
            return "\(list) \(apps.count == 1 ? "is" : "are") covering this window, so Confirm is off. Move \(apps.count == 1 ? "it" : "them") away and check the code again."
        }
    }

    public static func headline(_ s: PairingStatus) -> String {
        switch s.phase {
        case .closed: return "Pairing is closed"
        case .waiting: return "Pair a Linux device"
        case .confirm: return "Does this code match?"
        case .lockedOut: return "Pairing is locked"
        }
    }

    public static func detail(_ s: PairingStatus) -> String? {
        switch s.phase {
        case .closed:
            return "To add a Linux device, open the pairing window from the menu bar. It stays open for 2 minutes."
        case .waiting:
            return "On the Linux computer, open Preferences, then Mouse helper, and choose Pair with this Mac. This window closes by itself after 2 minutes."
        case .confirm(_, let device):
            return "\(device) wants to pair. Look at the Linux screen. Click Confirm only if it shows the same six digits. If they differ, click Reject."
        case .lockedOut(let seconds):
            return "Several codes did not match, so pairing is locked for \(duration(seconds)). This stops someone on your network from guessing. You can unlock it yourself if it was you."
        }
    }

    public static func notice(_ s: PairingStatus) -> String? {
        if s.anotherDeviceTrying { return "Another device is trying to pair. Wait for the first one to finish." }
        if let other = s.nameCollision, s.notice == nil { return refusal(.nameCollision(other)) }
        if let n = s.notice, case .refused(let r) = n { return refusal(r) }
        switch s.notice {
        case .refused: return nil
        case .unlockRefusedSessionActive:
            return "A Linux device could move the mouse right now, so pairing cannot be unlocked. Turn off Allow raw mouse in the menu bar menu, then try again."
        case .paired(let name): return "Paired with \(name)."
        case .rejected: return "Rejected. Nothing was saved. You can start again from the Linux device."
        case .expired: return "The pairing window closed. Open it again to pair."
        case .closedAfterFailures: return "Too many attempts. The pairing window closed."
        case .storeFailed(let why): return "Could not save the pairing. " + why
        case .resetRefusedSessionActive: return resetRefused
        case .keysReset: return resetDone
        case .keysChangedOutside: return keysChangedNotice
        case nil:
            if s.keyStoreDamaged { return damagedNotice }
            if s.ignoredRecords > 0 {
                let n = s.ignoredRecords
                return "\(n) unrecognised Keychain \(n == 1 ? "entry was" : "entries were") ignored. \(n == 1 ? "It does" : "They do") not give anyone access."
            }
            return nil
        }
    }

    /// Whether the notice for this status is a warning (yellow) rather than plain information.
    public static func isWarning(_ s: PairingStatus) -> Bool {
        if s.anotherDeviceTrying || s.nameCollision != nil && s.notice == nil { return true }
        switch s.notice {
        case .refused, .storeFailed, .unlockRefusedSessionActive, .resetRefusedSessionActive, .keysChangedOutside:
            return true
        case .paired, .rejected, .expired, .closedAfterFailures, .keysReset: return false
        case nil: return s.keyStoreDamaged || s.ignoredRecords > 0
        }
    }

    /// "about 23 hours", "about 5 minutes", "less than a minute".
    public static func duration(_ seconds: Double) -> String {
        if seconds >= 5400 { return "about \(Int((seconds / 3600).rounded())) hours" }
        if seconds >= 90 { return "about \(Int((seconds / 60).rounded())) minutes" }
        return "less than 2 minutes"
    }

    public static func removeQuestion(_ name: String) -> String { "Remove \(name)?" }
    public static func removeExplanation(_ name: String) -> String {
        "\(name) stops working at once, even in the middle of a session. To use it again you will have to pair it again."
    }
    public static let unlockQuestion = "Unlock pairing now?"
    public static let unlockExplanation =
        "Only do this if the failed codes were your own mistakes. Anyone on your network can try to pair again."
}
