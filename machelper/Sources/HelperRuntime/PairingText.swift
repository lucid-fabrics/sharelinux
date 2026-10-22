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
    public static let pairMenuItemBusy = "Pair a Linux device... (a session is active)"

    /// "1a2b 3c4d 5e6f 7a8b": the same string the Linux side shows for the key.
    public static func fingerprint(_ key: Data) -> String { DeviceIdentity.fingerprint(key) }

    /// One sentence for a refused Confirm.
    public static func refusal(_ r: ConfirmRefusal) -> String {
        switch r {
        case .noAttempt, .staleAttempt:
            return "The code changed or went away before your click. Check the code on screen and try again."
        case .tooSoon:
            return "That click came too soon after the code appeared, so it was ignored. Check the code, then click Confirm."
        case .sessionActive:
            return "A Linux device is using the mouse helper right now. Pair another device after that session ends."
        case .nameCollision(let other):
            return "A device called \(other) is already paired and this name looks the same. Reject, rename this computer on the Linux side and pair again."
        case .windowCovered:
            return "Another window is covering this one, so Confirm is off. Move the other window away and check the code again."
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
            return "A Linux device is using the mouse helper right now, so pairing cannot be unlocked. Try again after that session ends."
        case .paired(let name): return "Paired with \(name)."
        case .rejected: return "Rejected. Nothing was saved. You can start again from the Linux device."
        case .expired: return "The pairing window closed. Open it again to pair."
        case .closedAfterFailures: return "Too many attempts. The pairing window closed."
        case .storeFailed(let why): return "Could not save the pairing. " + why
        case nil:
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
        case .refused, .storeFailed, .unlockRefusedSessionActive: return true
        case .paired, .rejected, .expired, .closedAfterFailures: return false
        case nil: return s.ignoredRecords > 0
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
