import Foundation

/// The words of the pairing window, kept apart from AppKit so they can be
/// tested (plain language, no em-dashes, no key material).
public enum PairingText {
    public static let windowTitle = "Pair a Linux device"
    public static let confirmButton = "Confirm"
    public static let rejectButton = "Reject"
    public static let removeButton = "Remove"
    public static let unlockButton = "Unlock pairing..."
    public static let devicesHeader = "Paired devices"
    public static let noDevices = "No Linux device is paired yet."

    public static func headline(_ s: PairingStatus) -> String {
        switch s.phase {
        case .closed: return "Pairing is closed"
        case .waiting: return "Waiting for a Linux device"
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
        switch s.notice {
        case .paired(let name): return "Paired with \(name)."
        case .rejected: return "Rejected. Nothing was saved. You can start again from the Linux device."
        case .expired: return "The pairing window closed. Open it again to pair."
        case .closedAfterFailures: return "Too many attempts. The pairing window closed."
        case .storeFailed(let why): return "Could not save the pairing. " + why
        case nil: return nil
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
