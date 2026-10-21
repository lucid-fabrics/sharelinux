import AppKit
import HelperRuntime

/// The pairing window: shows the six-digit code and the device name, Confirm
/// and Reject, notices, and the paired devices with Remove. Opened only by the
/// owner's menu item. All words come from `PairingText`; all decisions are the
/// `PairingCoordinator`'s, reached through the closures below. Not exercised
/// by tests (AppKit); its logic is.
final class PairingWindowController: NSWindowController, NSWindowDelegate {
    var onConfirm: () -> Void = {}
    var onReject: () -> Void = {}
    var onClose: () -> Void = {}
    var onUnlock: () -> Void = {}
    var onRemove: (PairedDevice) -> Void = { _ in }

    private let headline = NSTextField(labelWithString: "")
    private let code = NSTextField(labelWithString: "")
    private let device = NSTextField(labelWithString: "")
    private let detail = NSTextField(wrappingLabelWithString: "")
    private let notice = NSTextField(wrappingLabelWithString: "")
    private let confirm = NSButton(title: PairingText.confirmButton, target: nil, action: nil)
    private let reject = NSButton(title: PairingText.rejectButton, target: nil, action: nil)
    private let unlock = NSButton(title: PairingText.unlockButton, target: nil, action: nil)
    private let devicesStack = NSStackView()
    private var current = PairingStatus()

    init() {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 440, height: 420), styleMask: [.titled, .closable],
            backing: .buffered, defer: false)
        window.title = PairingText.windowTitle
        window.isReleasedWhenClosed = false
        super.init(window: window)
        window.delegate = self
        build()
        window.center()
    }

    required init?(coder: NSCoder) { nil }

    private func build() {
        headline.font = .boldSystemFont(ofSize: 17)
        code.font = .monospacedDigitSystemFont(ofSize: 44, weight: .semibold)
        code.alignment = .center
        device.font = .systemFont(ofSize: 13)
        device.textColor = .secondaryLabelColor
        detail.textColor = .secondaryLabelColor
        notice.textColor = .labelColor
        confirm.keyEquivalent = "\r"
        confirm.bezelStyle = .rounded
        confirm.target = self; confirm.action = #selector(confirmClicked)
        reject.bezelStyle = .rounded
        reject.target = self; reject.action = #selector(rejectClicked)
        unlock.bezelStyle = .rounded
        unlock.target = self; unlock.action = #selector(unlockClicked)
        let buttons = NSStackView(views: [reject, confirm, unlock])
        buttons.spacing = 10
        devicesStack.orientation = .vertical
        devicesStack.alignment = .leading
        devicesStack.spacing = 6
        let devicesHeader = NSTextField(labelWithString: PairingText.devicesHeader)
        devicesHeader.font = .boldSystemFont(ofSize: 13)
        let stack = NSStackView(views: [
            headline, code, device, detail, notice, buttons, NSBox.separator(), devicesHeader, devicesStack,
        ])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 10
        stack.edgeInsets = NSEdgeInsets(top: 20, left: 20, bottom: 20, right: 20)
        stack.translatesAutoresizingMaskIntoConstraints = false
        let content = NSView()
        content.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            stack.topAnchor.constraint(equalTo: content.topAnchor),
            stack.bottomAnchor.constraint(equalTo: content.bottomAnchor),
            code.widthAnchor.constraint(equalToConstant: 400),
            detail.widthAnchor.constraint(equalToConstant: 400),
            notice.widthAnchor.constraint(equalToConstant: 400),
        ])
        window?.contentView = content
    }

    /// Shows the window in front. The caller has already opened the pairing window in the coordinator.
    func present() {
        NSApp.activate(ignoringOtherApps: true)
        showWindow(nil)
        window?.makeKeyAndOrderFront(nil)
    }

    func update(_ s: PairingStatus) {
        current = s
        headline.stringValue = PairingText.headline(s)
        detail.stringValue = PairingText.detail(s) ?? ""
        let note = PairingText.notice(s)
        notice.stringValue = note ?? ""
        notice.isHidden = note == nil
        if case .confirm(let c, let d) = s.phase {
            code.stringValue = String(c.prefix(3)) + " " + String(c.suffix(3))
            device.stringValue = "Linux device: " + d
            code.isHidden = false; device.isHidden = false
            confirm.isHidden = false; reject.isHidden = false
        } else {
            code.isHidden = true; device.isHidden = true
            confirm.isHidden = true; reject.isHidden = true
        }
        if case .lockedOut = s.phase { unlock.isHidden = false } else { unlock.isHidden = true }
        devicesStack.arrangedSubviews.forEach { $0.removeFromSuperview() }
        if s.devices.isEmpty {
            devicesStack.addArrangedSubview(NSTextField(labelWithString: PairingText.noDevices))
        }
        for (index, d) in s.devices.enumerated() {
            let name = NSTextField(labelWithString: d.name)
            name.setContentHuggingPriority(.defaultLow, for: .horizontal)
            let button = NSButton(title: PairingText.removeButton, target: self, action: #selector(removeClicked(_:)))
            button.tag = index
            button.bezelStyle = .rounded
            let row = NSStackView(views: [name, button])
            row.spacing = 12
            devicesStack.addArrangedSubview(row)
        }
    }

    @objc private func confirmClicked() { onConfirm() }
    @objc private func rejectClicked() { onReject() }

    @objc private func unlockClicked() {
        let alert = NSAlert()
        alert.messageText = PairingText.unlockQuestion
        alert.informativeText = PairingText.unlockExplanation
        alert.addButton(withTitle: "Cancel")
        alert.addButton(withTitle: "Unlock")
        if alert.runModal() == .alertSecondButtonReturn { onUnlock() }
    }

    @objc private func removeClicked(_ sender: NSButton) {
        guard current.devices.indices.contains(sender.tag) else { return }
        let d = current.devices[sender.tag]
        let alert = NSAlert()
        alert.messageText = PairingText.removeQuestion(d.name)
        alert.informativeText = PairingText.removeExplanation(d.name)
        alert.addButton(withTitle: "Cancel")
        alert.addButton(withTitle: PairingText.removeButton)
        if alert.runModal() == .alertSecondButtonReturn { onRemove(d) }
    }

    func windowWillClose(_ notification: Notification) { onClose() }
}

extension NSBox {
    static func separator() -> NSBox {
        let box = NSBox()
        box.boxType = .separator
        return box
    }
}
