import AppKit
import HelperCore
import HelperRuntime

/// Whether a window is fully in view: nothing on screen sits in front of any part of it.
public enum WindowCoverage {
    /// A window in front of ours: where it is (screen points, top-left origin, as the
    /// window server reports it) and how opaque.
    public struct Other: Equatable {
        public var frame: CGRect
        public var alpha: Double
        public init(frame: CGRect, alpha: Double) { self.frame = frame; self.alpha = alpha }
    }

    /// True when any window in front that is not see-through overlaps `frame`. A
    /// see-through overlay that lets clicks pass could be showing the person
    /// something else, so any visible overlap counts, not only a full cover.
    public static func isCovered(_ frame: CGRect, by others: [Other]) -> Bool {
        others.contains { $0.alpha > 0.05 && $0.frame.intersects(frame) }
    }

    /// The windows in front of `window` from the window server (geometry and alpha only;
    /// no screen-recording permission is involved). Windows of this process are skipped.
    static func windowsInFront(of window: NSWindow) -> [Other]? {
        let id = CGWindowID(window.windowNumber)
        let opts: CGWindowListOption = [.optionOnScreenAboveWindow, .excludeDesktopElements]
        guard let list = CGWindowListCopyWindowInfo(opts, id) as? [[String: Any]] else { return nil }
        let me = Int(ProcessInfo.processInfo.processIdentifier)
        return list.compactMap { w in
            guard (w[kCGWindowOwnerPID as String] as? Int) != me,
                let b = w[kCGWindowBounds as String] as? [String: Any],
                let rect = CGRect(dictionaryRepresentation: b as CFDictionary)
            else { return nil }
            return Other(frame: rect, alpha: (w[kCGWindowAlpha as String] as? Double) ?? 1)
        }
    }

    /// The verdict from what is known: the window is up on this space and not hidden, and the
    /// window server's list of windows in front is available and none of them overlaps. An
    /// unavailable list is NOT a pass: with nothing to check against, Confirm stays off.
    static func verdict(onScreen: Bool, others: [Other]?, topLeftFrame: CGRect) -> Bool {
        guard onScreen, let others else { return false }
        return !isCovered(topLeftFrame, by: others)
    }

    /// The window on screen, on this space, not minimised, not even partly hidden.
    public static func isFullyVisible(_ window: NSWindow) -> Bool {
        let onScreen =
            window.isVisible && !window.isMiniaturized && window.occlusionState.contains(.visible)
            && window.isOnActiveSpace
        guard onScreen else { return false }
        let primaryHeight = NSScreen.screens.first?.frame.height ?? 0
        let f = window.frame
        let topLeft = CGRect(x: f.minX, y: primaryHeight - f.maxY, width: f.width, height: f.height)
        return verdict(onScreen: true, others: windowsInFront(of: window), topLeftFrame: topLeft)
    }
}

/// A push button that tells the window when the mouse goes down on it, before the click
/// completes: what covers the window at that moment is what the person was aiming at.
final class PressReportingButton: NSButton {
    var onMouseDown: () -> Void = {}
    override func mouseDown(with event: NSEvent) {
        onMouseDown()
        super.mouseDown(with: event)  // tracks until the mouse goes up, then sends the action
    }
}

/// The helper has no icon file in its bundle, so the window draws one: a rounded
/// square in the system accent colour with the same pointer symbol as the menu bar.
enum AppIcon {
    static func image(size: CGFloat) -> NSImage {
        NSImage(size: NSSize(width: size, height: size), flipped: false) { rect in
            let path = NSBezierPath(roundedRect: rect.insetBy(dx: 1, dy: 1), xRadius: size * 0.23, yRadius: size * 0.23)
            let top = NSColor.controlAccentColor.blended(withFraction: 0.25, of: .white) ?? .controlAccentColor
            NSGradient(starting: top, ending: .controlAccentColor)?.draw(in: path, angle: -90)
            let config = NSImage.SymbolConfiguration(pointSize: size * 0.5, weight: .semibold)
                .applying(.init(paletteColors: [.white]))
            if let symbol = NSImage(systemSymbolName: "cursorarrow.click.2", accessibilityDescription: nil)?
                .withSymbolConfiguration(config)
            {
                let s = symbol.size
                symbol.draw(in: NSRect(x: (rect.width - s.width) / 2, y: (rect.height - s.height) / 2, width: s.width, height: s.height))
            }
            return true
        }
    }
}

/// The pairing window: shows the six-digit code and the device name, Confirm
/// and Reject, notices, and the paired devices with Remove. Opened only by the
/// owner's menu item. All words come from `PairingText`; all decisions are the
/// `PairingCoordinator`'s, reached through the closures below.
///
/// Rules this window keeps (design 2.8):
///  * Confirm is not the default button and has no key equivalent, and it
///    never takes keyboard focus, so Return and Space cannot press it. Esc
///    rejects (the safe direction).
///  * Only `presentFromOwnerClick()` activates the app and orders the window
///    front. `update(_:)` never does, so a device arriving cannot pull focus.
///  * A click on Confirm within a second of the code appearing or changing is
///    ignored, and one while another window covers this one is refused with a
///    reason. What is confirmed is the code and key that were on screen.
public final class PairingWindowController: NSWindowController, NSWindowDelegate {
    /// The code and the device key that were on screen when Confirm was clicked.
    public var onConfirm: (String, Data) -> Void = { _, _ in }
    public var onReject: () -> Void = {}
    public var onClose: () -> Void = {}
    public var onUnlock: () -> Void = {}
    public var onRemove: (PairedDevice) -> Void = { _ in }
    /// Seconds on a monotonic clock (tests move it).
    public var now: () -> Double = { ProcessInfo.processInfo.systemUptime }
    /// Whether the window can be seen in full (tests replace it).
    public var isFullyVisible: (NSWindow) -> Bool = WindowCoverage.isFullyVisible

    static let width: CGFloat = 420
    private static let inset: CGFloat = 20
    private static let contentWidth: CGFloat = 380

    private let icon = NSImageView()
    private let headline = NSTextField(labelWithString: "")
    private let code = NSTextField(labelWithString: "")
    private let deviceName = NSTextField(labelWithString: "")
    private let deviceFingerprint = NSTextField(labelWithString: "")
    private let deviceBlock = NSStackView()
    private let detail = NSTextField(wrappingLabelWithString: "")
    private let spinner = NSProgressIndicator()
    private let waitingRow = NSStackView()
    private let noticeBox = NSBox()
    private let notice = NSTextField(wrappingLabelWithString: "")
    let confirm = PressReportingButton(title: PairingText.confirmButton, target: nil, action: nil)
    let reject = NSButton(title: PairingText.rejectButton, target: nil, action: nil)
    private let cancel = NSButton(title: PairingText.cancelButton, target: nil, action: nil)
    private let unlock = NSButton(title: PairingText.unlockButton, target: nil, action: nil)
    private let buttons = NSStackView()
    private let devicesList = NSStackView()
    private let devicesBox = NSBox()
    private let stack = NSStackView()
    private var current = PairingStatus()
    private var localRefusal: ConfirmRefusal?
    /// When the code now on screen appeared or last changed.
    private(set) var shownSince: Double?
    private var shownTuple: (String, Data)?
    /// Whether the window was fully in view when the mouse went down on Confirm (nil: no press is pending).
    private var visibleAtMouseDown: Bool?

    public init() {
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: Self.width, height: 300), styleMask: [.titled, .closable],
            backing: .buffered, defer: false)
        window.title = PairingText.windowTitle
        window.isReleasedWhenClosed = false
        window.hidesOnDeactivate = false
        window.level = .normal
        super.init(window: window)
        window.delegate = self
        build()
        window.center()
    }

    public required init?(coder: NSCoder) { nil }

    private func label(_ l: NSTextField, size: CGFloat, weight: NSFont.Weight = .regular, color: NSColor = .labelColor) {
        l.font = .systemFont(ofSize: size, weight: weight)
        l.textColor = color
    }

    private func build() {
        icon.image = AppIcon.image(size: 48)
        icon.imageScaling = .scaleProportionallyUpOrDown
        label(headline, size: 17, weight: .semibold)
        headline.lineBreakMode = .byWordWrapping
        headline.maximumNumberOfLines = 2
        let header = NSStackView(views: [icon, headline])
        header.orientation = .horizontal
        header.alignment = .centerY
        header.spacing = 12

        code.font = .monospacedDigitSystemFont(ofSize: 44, weight: .medium)
        code.alignment = .center
        label(deviceName, size: 13, color: .secondaryLabelColor)
        deviceName.alignment = .center
        deviceName.lineBreakMode = .byTruncatingTail
        deviceFingerprint.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
        deviceFingerprint.textColor = .tertiaryLabelColor
        deviceFingerprint.alignment = .center
        deviceBlock.setViews([deviceName, deviceFingerprint], in: .center)
        deviceBlock.orientation = .vertical
        deviceBlock.alignment = .centerX
        deviceBlock.spacing = 2

        label(detail, size: 13, color: .secondaryLabelColor)
        detail.preferredMaxLayoutWidth = 340

        spinner.style = .spinning
        spinner.controlSize = .small
        spinner.isIndeterminate = true
        let waitingText = NSTextField(labelWithString: PairingText.waitingLine)
        label(waitingText, size: 13, color: .secondaryLabelColor)
        waitingRow.setViews([spinner, waitingText], in: .leading)
        waitingRow.orientation = .horizontal
        waitingRow.alignment = .centerY
        waitingRow.spacing = 8

        notice.font = .systemFont(ofSize: 13)
        notice.preferredMaxLayoutWidth = Self.contentWidth - 24
        noticeBox.boxType = .custom
        noticeBox.borderWidth = 0
        noticeBox.cornerRadius = 8
        noticeBox.contentViewMargins = .zero
        noticeBox.titlePosition = .noTitle
        noticeBox.contentView = {
            let v = NSView()
            notice.translatesAutoresizingMaskIntoConstraints = false
            v.addSubview(notice)
            NSLayoutConstraint.activate([
                notice.leadingAnchor.constraint(equalTo: v.leadingAnchor, constant: 12),
                notice.trailingAnchor.constraint(equalTo: v.trailingAnchor, constant: -12),
                notice.topAnchor.constraint(equalTo: v.topAnchor, constant: 8),
                notice.bottomAnchor.constraint(equalTo: v.bottomAnchor, constant: -8),
            ])
            return v
        }()

        // Confirm: a plain push button. Not the default, no key equivalent, never first responder.
        for b in [confirm, reject, cancel, unlock] {
            b.bezelStyle = .rounded
            b.controlSize = .large
            b.heightAnchor.constraint(equalToConstant: 32).isActive = true
        }
        confirm.keyEquivalent = ""
        confirm.refusesFirstResponder = true
        confirm.target = self; confirm.action = #selector(confirmClicked)
        confirm.onMouseDown = { [weak self] in self?.confirmMouseDown() }
        reject.target = self; reject.action = #selector(rejectClicked)
        cancel.target = self; cancel.action = #selector(cancelClicked)
        unlock.target = self; unlock.action = #selector(unlockClicked)
        let spacer = NSView()
        spacer.setContentHuggingPriority(.init(1), for: .horizontal)
        buttons.setViews([spacer, unlock, cancel, reject, confirm], in: .leading)
        buttons.orientation = .horizontal
        buttons.spacing = 8
        buttons.distribution = .fill

        let devicesHeader = NSTextField(labelWithString: PairingText.devicesHeader)
        label(devicesHeader, size: 13, weight: .semibold)
        devicesList.orientation = .vertical
        devicesList.alignment = .leading
        devicesList.spacing = 0
        devicesBox.boxType = .custom
        devicesBox.borderColor = .separatorColor
        devicesBox.borderWidth = 1
        devicesBox.cornerRadius = 8
        devicesBox.fillColor = .controlBackgroundColor
        devicesBox.titlePosition = .noTitle
        devicesBox.contentViewMargins = .zero
        let boxContent = NSView()
        devicesList.translatesAutoresizingMaskIntoConstraints = false
        boxContent.addSubview(devicesList)
        NSLayoutConstraint.activate([
            devicesList.leadingAnchor.constraint(equalTo: boxContent.leadingAnchor),
            devicesList.trailingAnchor.constraint(equalTo: boxContent.trailingAnchor),
            devicesList.topAnchor.constraint(equalTo: boxContent.topAnchor),
            devicesList.bottomAnchor.constraint(equalTo: boxContent.bottomAnchor),
        ])
        devicesBox.contentView = boxContent

        stack.setViews(
            [header, code, deviceBlock, detail, waitingRow, noticeBox, buttons, NSBox.separator(), devicesHeader, devicesBox],
            in: .leading)
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.edgeInsets = NSEdgeInsets(top: Self.inset, left: Self.inset, bottom: Self.inset, right: Self.inset)
        stack.translatesAutoresizingMaskIntoConstraints = false
        let content = NSView()
        content.addSubview(stack)
        var constraints = [
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            stack.topAnchor.constraint(equalTo: content.topAnchor),
            stack.widthAnchor.constraint(equalToConstant: Self.width),
            icon.widthAnchor.constraint(equalToConstant: 48),
            icon.heightAnchor.constraint(equalToConstant: 48),
            detail.widthAnchor.constraint(lessThanOrEqualToConstant: 340),
        ]
        for wide in [code, deviceBlock, noticeBox, buttons, devicesBox, header] as [NSView] {
            constraints.append(wide.widthAnchor.constraint(equalToConstant: Self.contentWidth))
        }
        NSLayoutConstraint.activate(constraints)
        window?.contentView = content
    }

    /// Shows the window in front. Call it only from the owner's own click on the
    /// menu item: it is the one place that activates the app. The caller has
    /// already opened the pairing window in the coordinator.
    public func presentFromOwnerClick() {
        NSApp.activate(ignoringOtherApps: true)
        showWindow(nil)
        window?.makeKeyAndOrderFront(nil)
    }

    /// Redraws for a new status. Never orders the window front or takes focus.
    public func update(_ s: PairingStatus) {
        current = s
        localRefusal = nil
        render()
    }

    private func render() {
        let s = current
        let showsConfirm: Bool
        let waiting: Bool
        if case .confirm = s.phase { showsConfirm = true } else { showsConfirm = false }
        if case .waiting = s.phase { waiting = true } else { waiting = false }

        headline.stringValue = PairingText.headline(s)
        var tuple: (String, Data)?
        if case .confirm(let c, let d) = s.phase {
            let triplets = String(c.prefix(3)) + "\u{2009}" + String(c.suffix(3))
            code.stringValue = triplets
            deviceName.stringValue = d
            if let peer = s.pendingKey, let own = s.ownKey {
                deviceFingerprint.stringValue = PairingText.fingerprintLines(mac: own, computer: peer)
            } else {
                deviceFingerprint.stringValue = s.pendingKey.map(PairingText.fingerprint) ?? ""
            }
            if let k = s.pendingKey { tuple = (c, k) }
        }
        // The one-second clock restarts whenever the code or the device changes.
        if let t = tuple {
            if shownTuple?.0 != t.0 || shownTuple?.1 != t.1 { shownSince = now() }
        } else {
            shownSince = nil
        }
        shownTuple = tuple

        var instruction = PairingText.detail(s)
        var warning: String?
        var info: String?
        if let local = localRefusal {
            warning = PairingText.refusal(local)
        } else if case .lockedOut = s.phase {
            warning = instruction
            instruction = nil
        } else if let n = PairingText.notice(s) {
            if PairingText.isWarning(s) { warning = n } else { info = n }
        }
        if case .closed = s.phase, info != nil { instruction = nil }
        detail.stringValue = instruction ?? ""
        detail.isHidden = instruction == nil

        code.isHidden = !showsConfirm
        deviceBlock.isHidden = !showsConfirm
        waitingRow.isHidden = !waiting
        if waiting { spinner.startAnimation(nil) } else { spinner.stopAnimation(nil) }
        let message = warning ?? info
        noticeBox.isHidden = message == nil
        notice.stringValue = message ?? ""
        noticeBox.fillColor =
            warning != nil ? NSColor.systemYellow.withAlphaComponent(0.2) : NSColor.quaternaryLabelColor
        confirm.isHidden = !showsConfirm
        reject.isHidden = !showsConfirm
        cancel.isHidden = !waiting
        // Esc belongs to whichever of the two is on screen: reject a code, or cancel the wait.
        reject.keyEquivalent = showsConfirm ? "\u{1b}" : ""
        cancel.keyEquivalent = waiting ? "\u{1b}" : ""
        var locked = false
        if case .lockedOut = s.phase { locked = true }
        unlock.isHidden = !locked
        buttons.isHidden = !(showsConfirm || waiting || locked)

        renderDevices(s.devices)
        resizeToFit()
    }

    private func renderDevices(_ devices: [PairedDevice]) {
        devicesList.arrangedSubviews.forEach { $0.removeFromSuperview() }
        let rowWidth = Self.contentWidth - 2
        if devices.isEmpty {
            let empty = NSTextField(labelWithString: PairingText.noDevices)
            label(empty, size: 13, color: .secondaryLabelColor)
            empty.alignment = .center
            let holder = NSView()
            empty.translatesAutoresizingMaskIntoConstraints = false
            holder.addSubview(empty)
            NSLayoutConstraint.activate([
                empty.centerXAnchor.constraint(equalTo: holder.centerXAnchor),
                empty.topAnchor.constraint(equalTo: holder.topAnchor, constant: 14),
                empty.bottomAnchor.constraint(equalTo: holder.bottomAnchor, constant: -14),
                holder.widthAnchor.constraint(equalToConstant: rowWidth),
            ])
            devicesList.addArrangedSubview(holder)
            return
        }
        for (index, d) in devices.enumerated() {
            if index > 0 {
                let line = NSBox.separator()
                devicesList.addArrangedSubview(line)
                line.widthAnchor.constraint(equalToConstant: rowWidth).isActive = true
            }
            let name = NSTextField(labelWithString: d.name)
            label(name, size: 13)
            name.lineBreakMode = .byTruncatingTail
            name.setContentCompressionResistancePriority(.init(250), for: .horizontal)
            let fp = NSTextField(labelWithString: PairingText.fingerprint(d.key))
            fp.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
            fp.textColor = .secondaryLabelColor
            let text = NSStackView(views: [name, fp])
            text.orientation = .vertical
            text.alignment = .leading
            text.spacing = 1
            let remove = NSButton(title: PairingText.removeButton, target: self, action: #selector(removeClicked(_:)))
            remove.tag = index
            remove.isBordered = false
            remove.attributedTitle = NSAttributedString(
                string: PairingText.removeButton,
                attributes: [.foregroundColor: NSColor.systemRed, .font: NSFont.systemFont(ofSize: 13)])
            remove.setContentHuggingPriority(.required, for: .horizontal)
            let row = NSStackView(views: [text, remove])
            row.orientation = .horizontal
            row.alignment = .centerY
            row.distribution = .fill
            row.spacing = 12
            row.edgeInsets = NSEdgeInsets(top: 8, left: 12, bottom: 8, right: 12)
            text.setContentHuggingPriority(.defaultLow, for: .horizontal)
            devicesList.addArrangedSubview(row)
            row.widthAnchor.constraint(equalToConstant: rowWidth).isActive = true
        }
    }

    /// Height fits the content; the top edge stays where it is.
    private func resizeToFit() {
        guard let window, let content = window.contentView else { return }
        content.layoutSubtreeIfNeeded()
        let height = ceil(stack.fittingSize.height)
        let target = window.frameRect(forContentRect: NSRect(x: 0, y: 0, width: Self.width, height: height))
        var frame = window.frame
        frame.origin.y += frame.height - target.height
        frame.size = target.size
        window.setFrame(frame, display: false)
        window.contentMinSize = NSSize(width: Self.width, height: height)
        window.contentMaxSize = NSSize(width: Self.width, height: height)
        content.layoutSubtreeIfNeeded()
    }

    /// The key lines under the device name (tests).
    var fingerprintText: String { deviceBlock.isHidden ? "" : deviceFingerprint.stringValue }

    /// The text of the notice area (tests).
    var noticeText: String { noticeBox.isHidden ? "" : notice.stringValue }

    // MARK: actions

    /// The mouse went down on Confirm: note whether the window was fully in view right then.
    func confirmMouseDown() {
        visibleAtMouseDown = window.map { isFullyVisible($0) } ?? false
    }

    /// The click handler (mouse up), callable by tests. The window must have been fully in view at
    /// mouse-down (when there was one) and must still be at mouse-up.
    @objc public func confirmClicked() {
        let downVisible = visibleAtMouseDown
        visibleAtMouseDown = nil
        guard case .confirm(let c, _) = current.phase, let key = current.pendingKey else { return }
        // A click within a second of the code appearing was aimed at something else.
        if let since = shownSince, now() - since < PairingCoordinator.minConfirmDelay { return }
        if downVisible == false || window.map({ !isFullyVisible($0) }) ?? true {
            localRefusal = .windowCovered
            render()
            return
        }
        onConfirm(c, key)
    }

    @objc private func rejectClicked() { onReject() }
    @objc private func cancelClicked() { window?.close() }  // windowWillClose tells the coordinator

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

    public func windowWillClose(_ notification: Notification) { onClose() }

    /// Renders the window, frame included, to PNG data without showing anything (the
    /// layout preview and its test use it; no screen capture permission is needed).
    public func renderPNG(dark: Bool = false) -> Data? {
        guard let window, let content = window.contentView else { return nil }
        window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        window.backgroundColor = .windowBackgroundColor
        render()
        content.layoutSubtreeIfNeeded()
        window.displayIfNeeded()
        let view = content.superview ?? content
        view.layoutSubtreeIfNeeded()
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return nil }
        view.cacheDisplay(in: view.bounds, to: rep)
        return rep.representation(using: .png, properties: [:])
    }
}

extension NSBox {
    static func separator() -> NSBox {
        let box = NSBox()
        box.boxType = .separator
        return box
    }
}
