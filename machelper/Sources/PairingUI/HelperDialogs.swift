import AppKit
import HelperRuntime

/// The alerts and sheets about the helper's keys, built here so the app and the offscreen preview
/// show exactly the same thing (words come from `PairingText`).
public enum HelperDialogs {
    private static func alert(
        title: String, text: String, style: NSAlert.Style = .informational, buttons: [String] = ["OK"],
        destructive: String? = nil
    ) -> NSAlert {
        let alert = NSAlert()
        alert.alertStyle = style
        alert.messageText = title
        alert.informativeText = text
        for b in buttons {
            let button = alert.addButton(withTitle: b)
            if b == destructive { button.hasDestructiveAction = true }
        }
        return alert
    }

    /// The keys changed outside the app (at launch, before a session, on wake, or a record changed).
    public static func keysChanged() -> NSAlert {
        alert(title: PairingText.keysChangedTitle, text: PairingText.keysChangedAlert, style: .critical)
    }

    /// Launch found the keys changed since the last run. "Leave the helper off" is the default button
    /// (Return); keeping the paired devices or resetting has to be chosen.
    public static func keysReview(
        identityBefore: String?, identityNow: String, pairedListBefore: String?, pairedListNow: String
    ) -> NSAlert {
        alert(
            title: PairingText.launchChangeTitle,
            text: PairingText.launchChangeText(
                identityBefore: identityBefore, identityNow: identityNow, pairedListBefore: pairedListBefore,
                pairedListNow: pairedListNow),
            style: .critical,
            buttons: [PairingText.leaveOffButton, PairingText.keepPairedButton, PairingText.resetMenuItem],
            destructive: PairingText.resetMenuItem)
    }

    /// "Reset helper keys...": Cancel is the default button (Return), the reset has to be chosen.
    public static func resetConfirm() -> NSAlert {
        alert(
            title: PairingText.resetQuestion, text: PairingText.resetExplanation, style: .warning,
            buttons: ["Cancel", PairingText.resetConfirmButton], destructive: PairingText.resetConfirmButton)
    }

    public static func resetRefused() -> NSAlert {
        alert(title: PairingText.resetRefusedTitle, text: PairingText.resetRefused, style: .warning)
    }

    public static func resetDone() -> NSAlert {
        alert(title: PairingText.resetDoneTitle, text: PairingText.resetDone)
    }

    public static func resetFailed() -> NSAlert {
        alert(title: PairingText.resetRefusedTitle, text: PairingText.resetFailed, style: .warning)
    }

    /// Renders the alert's window to PNG data without showing anything (preview and tests).
    public static func renderPNG(_ alert: NSAlert, dark: Bool = false) -> Data? {
        alert.layout()
        let window = alert.window
        window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        window.backgroundColor = .windowBackgroundColor
        guard let content = window.contentView else { return nil }
        content.layoutSubtreeIfNeeded()
        window.displayIfNeeded()
        let view = content.superview ?? content
        view.layoutSubtreeIfNeeded()
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return nil }
        view.cacheDisplay(in: view.bounds, to: rep)
        return rep.representation(using: .png, properties: [:])
    }
}
