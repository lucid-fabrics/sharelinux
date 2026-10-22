import AppKit
import HelperCore
import HelperRuntime

/// What the menu bar menu shows, decided apart from AppKit so it can be tested and drawn offscreen.
public struct HelperMenuEntry: Equatable {
    public enum ID: Hashable {
        case state, toggle, stop, review, grant, pair, reset, keyMine, keyList, quit
        case separator(Int)
    }
    public var id: ID
    public var title: String
    public var enabled = true
    public var checked = false
    public var hidden = false
}

public enum HelperMenuModel {
    /// - Parameters:
    ///   - cannotEnable: why the switch cannot be turned on (hotkey missing, keys reset), if it cannot.
    ///   - problem: a standing problem to show in place of the state (the UDP port could not be opened).
    ///   - identity / pairedList: the two fingerprints, see `PairingText.keyMenuLines`.
    public static func entries(
        state: RuntimeState, cannotEnable: String?, accessGranted: Bool, identity: String, pairedList: String,
        problem: String? = nil
    ) -> [HelperMenuEntry] {
        let stateTitle: String
        if let problem {
            stateTitle = problem
        } else if state.keysReviewPending {
            stateTitle = PairingText.keysReviewState
        } else if state.keysChanged {
            stateTitle = PairingText.keysChangedState
        } else if let why = cannotEnable {
            stateTitle = "Cannot switch on: " + why
        } else if let device = state.device {
            let why = state.lastDeny.map { " (\(describe($0)))" } ?? ""
            stateTitle = state.injecting ? "Raw mouse active: \(device)" : "Connected: \(device)\(why)"
        } else {
            stateTitle = state.allowRawMouse ? "Raw mouse is on, waiting for a device" : "Raw mouse is off"
        }
        let busy = state.pairingBlocked
        let lines = PairingText.keyMenuLines(identity: identity, pairedList: pairedList)
        return [
            HelperMenuEntry(id: .state, title: stateTitle, enabled: false),
            HelperMenuEntry(id: .separator(0), title: ""),
            HelperMenuEntry(
                id: .toggle, title: "Allow raw mouse", enabled: cannotEnable == nil && !state.keysChanged,
                checked: state.allowRawMouse),
            HelperMenuEntry(id: .stop, title: "Stop now"),
            HelperMenuEntry(id: .review, title: PairingText.reviewMenuItem, hidden: !state.keysReviewPending),
            HelperMenuEntry(id: .separator(1), title: ""),
            HelperMenuEntry(id: .grant, title: "Request Accessibility access...", hidden: accessGranted),
            HelperMenuEntry(
                id: .pair, title: busy ? PairingText.pairMenuItemBusy : PairingText.pairMenuItem, enabled: !busy),
            HelperMenuEntry(
                id: .reset, title: busy ? PairingText.resetMenuItemBusy : PairingText.resetMenuItem, enabled: !busy),
            HelperMenuEntry(id: .separator(2), title: ""),
            HelperMenuEntry(id: .keyMine, title: lines.0, enabled: false),
            HelperMenuEntry(id: .keyList, title: lines.1, enabled: false),
            HelperMenuEntry(id: .separator(3), title: ""),
            HelperMenuEntry(id: .quit, title: "Quit"),
        ]
    }

    public static func describe(_ s: HelperState) -> String {
        switch s {
        case .active: return "active"
        case .disabled: return "switched off"
        case .locked: return "screen locked"
        case .noSession: return "no Screen Sharing session from this device"
        case .notConsole: return "not the console session"
        case .rateLimited: return "too fast"
        case .needsGrant: return "waiting for Accessibility access"
        }
    }
}

/// The real NSMenu, driven by `HelperMenuModel`. The app gives each action's target and selector.
public final class HelperMenu {
    public let menu = NSMenu()
    private var items: [HelperMenuEntry.ID: NSMenuItem] = [:]

    public init(target: AnyObject, actions: [HelperMenuEntry.ID: Selector], keyEquivalents: [HelperMenuEntry.ID: String] = [:],
        entries: [HelperMenuEntry]
    ) {
        menu.autoenablesItems = false
        for e in entries {
            let item: NSMenuItem
            if case .separator = e.id {
                item = .separator()
            } else {
                item = NSMenuItem(title: e.title, action: actions[e.id], keyEquivalent: keyEquivalents[e.id] ?? "")
                if actions[e.id] != nil { item.target = target }
            }
            items[e.id] = item
            menu.addItem(item)
        }
        update(entries)
    }

    public func update(_ entries: [HelperMenuEntry]) {
        for e in entries {
            guard let item = items[e.id] else { continue }
            item.title = e.title
            item.isEnabled = e.enabled
            item.state = e.checked ? .on : .off
            item.isHidden = e.hidden
        }
    }

    /// The titles as the menu holds them (tests).
    public var visibleTitles: [String] { menu.items.filter { !$0.isHidden && !$0.isSeparatorItem }.map(\.title) }

    /// A drawing of the menu from its entries, for the offscreen preview (a real NSMenu cannot be
    /// drawn without showing it). Same titles, enabled and hidden states as the real menu.
    public static func renderPNG(_ entries: [HelperMenuEntry], dark: Bool = false) -> Data? {
        let width: CGFloat = 340
        let rows = NSStackView()
        rows.orientation = .vertical
        rows.alignment = .leading
        rows.spacing = 0
        rows.edgeInsets = NSEdgeInsets(top: 6, left: 0, bottom: 6, right: 0)
        for e in entries where !e.hidden {
            if case .separator = e.id {
                let line = NSBox()
                line.boxType = .separator
                let holder = NSView()
                holder.translatesAutoresizingMaskIntoConstraints = false
                line.translatesAutoresizingMaskIntoConstraints = false
                holder.addSubview(line)
                NSLayoutConstraint.activate([
                    line.leadingAnchor.constraint(equalTo: holder.leadingAnchor, constant: 12),
                    line.trailingAnchor.constraint(equalTo: holder.trailingAnchor, constant: -12),
                    line.centerYAnchor.constraint(equalTo: holder.centerYAnchor),
                    holder.heightAnchor.constraint(equalToConstant: 9), holder.widthAnchor.constraint(equalToConstant: width),
                ])
                rows.addArrangedSubview(holder)
                continue
            }
            let label = NSTextField(labelWithString: (e.checked ? "\u{2713}  " : "     ") + e.title)
            label.font = .menuFont(ofSize: 0)
            label.textColor = e.enabled ? .labelColor : .disabledControlTextColor
            label.lineBreakMode = .byTruncatingTail
            label.translatesAutoresizingMaskIntoConstraints = false
            let holder = NSView()
            holder.translatesAutoresizingMaskIntoConstraints = false
            holder.addSubview(label)
            NSLayoutConstraint.activate([
                label.leadingAnchor.constraint(equalTo: holder.leadingAnchor, constant: 14),
                label.trailingAnchor.constraint(equalTo: holder.trailingAnchor, constant: -14),
                label.centerYAnchor.constraint(equalTo: holder.centerYAnchor),
                holder.heightAnchor.constraint(equalToConstant: 22), holder.widthAnchor.constraint(equalToConstant: width),
            ])
            rows.addArrangedSubview(holder)
        }
        let size = rows.fittingSize
        let view = NSVisualEffectView(frame: NSRect(origin: .zero, size: NSSize(width: width, height: size.height)))
        view.material = .menu
        view.state = .active
        view.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        view.wantsLayer = true
        view.layer?.cornerRadius = 10
        rows.frame = view.bounds
        view.addSubview(rows)
        view.layoutSubtreeIfNeeded()
        guard let rep = view.bitmapImageRepForCachingDisplay(in: view.bounds) else { return nil }
        view.cacheDisplay(in: view.bounds, to: rep)
        return rep.representation(using: .png, properties: [:])
    }
}
