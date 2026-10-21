import AppKit
import HelperRuntime

/// A small always-on-top badge that shows while events are being posted. It is
/// a window of its own, so it does not depend on the menu bar icon (which the
/// notch, a full menu bar or a "hide icon" tool can hide). It sits in the
/// bottom-left corner of the main display, above the Dock and full-screen
/// apps, and ignores the mouse completely, so it never gets in the way of a
/// click. Nothing here posts events or asks for a permission.
final class InjectingBadge {
    private var panel: NSPanel?
    private let label = NSTextField(labelWithString: "")

    func update(text: String?) {
        guard let text else { hide(); return }
        let panel = self.panel ?? makePanel()
        self.panel = panel
        label.stringValue = text
        label.sizeToFit()
        let size = NSSize(width: label.frame.width + 28, height: label.frame.height + 14)
        let screen = NSScreen.main ?? NSScreen.screens.first
        let origin = screen.map { NSPoint(x: $0.frame.minX + 14, y: $0.frame.minY + 14) } ?? .zero
        panel.setFrame(NSRect(origin: origin, size: size), display: true)
        label.frame.origin = NSPoint(x: 14, y: 7)
        panel.orderFrontRegardless()
    }

    func hide() { panel?.orderOut(nil) }

    private func makePanel() -> NSPanel {
        let p = NSPanel(
            contentRect: .zero, styleMask: [.borderless, .nonactivatingPanel], backing: .buffered, defer: false)
        p.level = .screenSaver
        p.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .stationary, .ignoresCycle]
        p.ignoresMouseEvents = true
        p.isOpaque = false
        p.backgroundColor = .clear
        p.hasShadow = true
        p.hidesOnDeactivate = false
        p.isReleasedWhenClosed = false
        let box = NSView()
        box.wantsLayer = true
        box.layer?.backgroundColor = NSColor.systemRed.cgColor
        box.layer?.cornerRadius = 12
        label.textColor = .white
        label.font = .systemFont(ofSize: 13, weight: .semibold)
        box.addSubview(label)
        p.contentView = box
        return p
    }
}
