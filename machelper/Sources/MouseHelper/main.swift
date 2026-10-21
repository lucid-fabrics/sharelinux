import AppKit
import Carbon.HIToolbox
import CryptoKit
import Foundation
import HelperCore
import HelperRuntime
import UserNotifications
import os

// OSS Mouse Helper: menu bar app that posts relative mouse motion for a paired
// Linux client while a Screen Sharing session from the same address is live
// (design sections 2 and 3).
//
// Safety rules that live here (the logic is in HelperRuntime and is unit tested):
//  * "Allow raw mouse" is OFF on every launch and never saved.
//  * Nothing asks for a permission by itself. The Accessibility prompt only
//    appears when the owner chooses "Request Accessibility access...".
//  * No keyboard events are ever posted; there is no IPC for other apps.

let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "app")

final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    private var runtime: HelperRuntime!
    private var server: UDPServer!
    private var sessionSource: SystemSessionSource!
    private var hotKeyRef: EventHotKeyRef?
    private var stateItem: NSMenuItem!
    private var toggleItem: NSMenuItem!
    private var grantItem: NSMenuItem!
    private var state = RuntimeState(allowRawMouse: false, device: nil, injecting: false, lastDeny: nil)
    private var notificationsAsked = false
    // W2 reads the paired list; the pairing flow (W3) fills it. An unreadable
    // file means nobody is paired.
    private var paired: [IdentityStore.Paired] = []

    func applicationDidFinishLaunching(_ note: Notification) {
        let store = IdentityStore(directory: IdentityStore.defaultDirectory())
        let key: Curve25519.KeyAgreement.PrivateKey
        do { key = try store.loadOrCreateKey() } catch {
            log.error("cannot load the identity key: \(String(describing: error), privacy: .public)")
            NSApp.terminate(nil)
            return
        }
        paired = (try? store.loadPaired()) ?? []

        sessionSource = SystemSessionSource()
        runtime = HelperRuntime(
            staticKey: key, pairedKeys: { [unowned self] in self.paired.compactMap { Data(hex: $0.key) } },
            poster: CGEventPoster(),
            session: sessionSource, sharing: SystemScreenSharingSource(), access: SystemAccessSource(),
            ownUID: getuid(),
            clock: ChannelClock(monotonic: { ProcessInfo.processInfo.systemUptime }, wall: { Date() }),
            timestamps: FileTimestampStore(url: store.timestampsURL),
            deviceName: { [unowned self] k in
                self.paired.first { Data(hex: $0.key) == k }?.name ?? HelperRuntime.fingerprintName(k)
            },
            doubleClickInterval: { NSEvent.doubleClickInterval })
        runtime.onState = { [weak self] s in DispatchQueue.main.async { self?.apply(s) } }
        runtime.onSessionStart = { [weak self] device in DispatchQueue.main.async { self?.notifySessionStart(device) } }

        buildMenu()
        registerKillHotKey()
        observeSystem()
        server = UDPServer(runtime: runtime)
        do { try server.start() } catch {
            log.error("cannot open the UDP port: \(String(describing: error), privacy: .public)")
            stateItem.title = "Cannot open UDP port \(UDPServer.defaultPort)"
        }
        apply(runtime.state)
    }

    // MARK: menu

    private func buildMenu() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        let menu = NSMenu()
        stateItem = NSMenuItem(title: "Raw mouse is off", action: nil, keyEquivalent: "")
        stateItem.isEnabled = false
        menu.addItem(stateItem)
        menu.addItem(.separator())
        toggleItem = NSMenuItem(title: "Allow raw mouse", action: #selector(toggle), keyEquivalent: "")
        toggleItem.target = self
        menu.addItem(toggleItem)
        let stop = NSMenuItem(title: "Stop now", action: #selector(stopNow), keyEquivalent: "")
        stop.target = self
        menu.addItem(stop)
        menu.addItem(.separator())
        grantItem = NSMenuItem(
            title: "Request Accessibility access...", action: #selector(requestAccess), keyEquivalent: "")
        grantItem.target = self
        menu.addItem(grantItem)
        let pair = NSMenuItem(title: "Pair new device... (not available yet)", action: nil, keyEquivalent: "")
        pair.isEnabled = false
        menu.addItem(pair)
        menu.addItem(.separator())
        let quit = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quit.target = self
        menu.addItem(quit)
        statusItem.menu = menu
    }

    /// Updates the menu bar icon and menu text. The filled pointer with click
    /// marks means events are being posted right now; a dimmed plain pointer
    /// means the switch is off.
    private func apply(_ s: RuntimeState) {
        state = s
        toggleItem.state = s.allowRawMouse ? .on : .off
        let name = s.injecting ? "cursorarrow.click.2" : "cursorarrow"
        statusItem.button?.image = NSImage(systemSymbolName: name, accessibilityDescription: "OSS Mouse Helper")
        statusItem.button?.alphaValue = s.allowRawMouse ? 1.0 : 0.5
        if let device = s.device {
            let why = s.lastDeny.map { " (\(Self.describe($0)))" } ?? ""
            stateItem.title = s.injecting ? "Raw mouse active: \(device)" : "Connected: \(device)\(why)"
        } else {
            stateItem.title = s.allowRawMouse ? "Raw mouse is on, waiting for a device" : "Raw mouse is off"
        }
        grantItem.isHidden = SystemAccessSource().granted()
    }

    private static func describe(_ s: HelperState) -> String {
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

    @objc private func toggle() {
        let on = !state.allowRawMouse
        runtime.queue.async { self.runtime.setAllowRawMouse(on) }
    }

    @objc private func stopNow() { runtime.queue.async { self.runtime.killSwitch() } }

    @objc private func requestAccess() {
        // The only place a permission is requested, and only on the owner's click.
        _ = CGRequestPostEventAccess()
    }

    @objc private func quit() {
        runtime.queue.sync { self.runtime.killSwitch() }
        server.stop()
        NSApp.terminate(nil)
    }

    // MARK: kill switch hotkey (Carbon, needs no grant): Control-Option-Command-Escape

    private func registerKillHotKey() {
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let me = Unmanaged.passUnretained(self).toOpaque()
        InstallEventHandler(
            GetApplicationEventTarget(),
            { _, _, user in
                guard let user else { return noErr }
                let app = Unmanaged<AppDelegate>.fromOpaque(user).takeUnretainedValue()
                app.runtime.queue.async { app.runtime.killSwitch() }
                return noErr
            }, 1, &spec, me, nil)
        let id = EventHotKeyID(signature: OSType(0x4F53_5348), id: 1)  // 'OSSH'
        RegisterEventHotKey(
            UInt32(kVK_Escape), UInt32(controlKey | optionKey | cmdKey), id, GetApplicationEventTarget(), 0,
            &hotKeyRef)
    }

    // MARK: system notifications -> stuck-button protection

    private func observeSystem() {
        let dist = DistributedNotificationCenter.default()
        dist.addObserver(forName: .init("com.apple.screenIsLocked"), object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.sessionSource.noteLockNotification()
            self.runtime.queue.async { self.runtime.environmentInterruption(.locked) }
        }
        dist.addObserver(forName: .init("com.apple.screenIsUnlocked"), object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.sessionSource.noteUnlockNotification()
            self.runtime.queue.async { self.runtime.refreshEnvironment() }
        }
        let center = NSWorkspace.shared.notificationCenter
        center.addObserver(forName: NSWorkspace.willSleepNotification, object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.runtime.queue.sync { self.runtime.willSleep() }
        }
        center.addObserver(forName: NSWorkspace.sessionDidResignActiveNotification, object: nil, queue: nil) {
            [weak self] _ in
            guard let self else { return }
            self.runtime.queue.async { self.runtime.environmentInterruption(.environment) }
        }
        center.addObserver(forName: NSWorkspace.sessionDidBecomeActiveNotification, object: nil, queue: nil) {
            [weak self] _ in
            guard let self else { return }
            self.runtime.queue.async { self.runtime.refreshEnvironment() }
        }
    }

    // MARK: notification at session start

    private func notifySessionStart(_ device: String) {
        guard Bundle.main.bundleIdentifier != nil else { return }
        let center = UNUserNotificationCenter.current()
        func post() {
            let content = UNMutableNotificationContent()
            content.title = "Raw mouse active"
            content.body = "\(device) can move the mouse until you click Stop now."
            center.add(UNNotificationRequest(identifier: UUID().uuidString, content: content, trigger: nil))
        }
        if notificationsAsked { post(); return }
        notificationsAsked = true
        center.requestAuthorization(options: [.alert]) { granted, _ in if granted { post() } }
    }
}

let app = NSApplication.shared
app.setActivationPolicy(.accessory)  // menu bar only (also LSUIElement in Info.plist)
let delegate = AppDelegate()
app.delegate = delegate
app.run()
