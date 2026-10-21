import AppKit
import Carbon.HIToolbox
import CryptoKit
import Foundation
import HelperCore
import HelperRuntime
import PairingUI
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
    private let badge = InjectingBadge()
    private var stateItem: NSMenuItem!
    private var toggleItem: NSMenuItem!
    private var grantItem: NSMenuItem!
    private var state = RuntimeState(allowRawMouse: false, device: nil, injecting: false, lastDeny: nil)
    private var notificationsAsked = false
    private var pairing: PairingCoordinator!
    private var pairingWindow: PairingWindowController!
    private var pairingUpdates: StateCoalescer<PairingStatus>!

    func applicationDidFinishLaunching(_ note: Notification) {
        // The identity key and the paired devices live in the login Keychain. Files in the
        // support directory (timestamps, the failure cooldown) hold no secrets; an older
        // build's key file is moved into the Keychain and deleted.
        let store = IdentityStore(directory: IdentityStore.defaultDirectory())
        let keys = KeychainKeyStore()
        let key: Curve25519.KeyAgreement.PrivateKey
        do {
            try keys.importLegacyFiles(from: store)
            key = try keys.loadOrCreateIdentity()
            pairing = try PairingCoordinator(
                staticKey: key, store: keys,
                ledger: PairingFailureLedger(store: FilePairingFailureStore(url: store.pairingFailuresURL)))
        } catch {
            log.error("cannot open the key store: \(String(describing: error), privacy: .public)")
            NSApp.terminate(nil)
            return
        }

        sessionSource = SystemSessionSource()
        runtime = HelperRuntime(
            staticKey: key, pairedKeys: { [pairing] in pairing?.pairedKeys ?? [] },
            poster: CGEventPoster(),
            session: sessionSource, sharing: SystemScreenSharingSource(), access: SystemAccessSource(),
            ownUID: getuid(),
            clock: ChannelClock(monotonic: { ProcessInfo.processInfo.systemUptime }, wall: { Date() }),
            timestamps: FileTimestampStore(url: store.timestampsURL),
            deviceName: { [pairing] k in pairing?.name(for: k) ?? HelperRuntime.fingerprintName(k) },
            doubleClickInterval: { NSEvent.doubleClickInterval }, pairing: pairing)
        // Coalesced: a peer flipping allowed/denied per packet must not flood the main thread,
        // where the hotkey and "Stop now" run.
        runtime.observeState(on: .main) { [weak self] s in self?.apply(s) }
        runtime.onSessionStart = { [weak self] device in DispatchQueue.main.async { self?.notifySessionStart(device) } }

        pairingWindow = PairingWindowController()
        pairingWindow.onConfirm = { [unowned self] in runtime.queue.async { self.pairing.confirm() } }
        pairingWindow.onReject = { [unowned self] in runtime.queue.async { self.pairing.reject() } }
        pairingWindow.onClose = { [unowned self] in runtime.queue.async { self.pairing.close() } }
        pairingWindow.onUnlock = { [unowned self] in runtime.queue.async { self.pairing.resetCooldown() } }
        pairingWindow.onRemove = { [unowned self] d in
            // Buttons up here, now; the runtime queue then deletes the key and ends the session.
            _ = runtime.removePairedDevice(d.key)
        }
        pairingUpdates = StateCoalescer<PairingStatus>(target: .main, minInterval: 0.05) { [weak self] s in
            self?.pairingWindow.update(s)
        }
        pairing.onChange = { [pairingUpdates] s in pairingUpdates?.push(s) }
        pairingWindow.update(pairing.status)

        buildMenu()
        if let problem = registerKillHotKey() {
            // No hotkey means no stop that survives a hidden menu bar icon: refuse to switch on.
            runtime.cannotEnableReason = problem
            log.error("\(problem, privacy: .public)")
        }
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
        // The only way to open a pairing window: the owner's click, here.
        let pair = NSMenuItem(title: "Pair a Linux device...", action: #selector(openPairing), keyEquivalent: "")
        pair.target = self
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
        badge.update(text: s.badgeText)
        if let why = runtime.cannotEnableReason {
            toggleItem.isEnabled = false
            stateItem.title = "Cannot switch on: " + why
        } else if let device = s.device {
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
        runtime.queue.async { self.runtime.setAllowRawMouse(on) }  // refused while cannotEnableReason is set
    }

    /// Released on this thread at once; the runtime queue only ends the session afterwards.
    @objc private func stopNow() { runtime.emergencyStop(.kill) }

    @objc private func openPairing() {
        pairingWindow.present()
        runtime.queue.async { self.pairing.open() }
    }

    @objc private func requestAccess() {
        // The only place a permission is requested, and only on the owner's click.
        _ = CGRequestPostEventAccess()
    }

    @objc private func quit() {
        // Buttons are up and posting is off before this returns; the wait is
        // bounded so a busy queue cannot keep the app from quitting.
        runtime.emergencyStop(.kill, wait: 0.5)
        server.stop()
        NSApp.terminate(nil)
    }

    // MARK: kill switch hotkey (Carbon, needs no grant): Control-Option-Command-Escape

    /// Registers the hotkey. Returns nil on success, otherwise the reason it failed
    /// (another app owns the shortcut, or the handler could not be installed).
    private func registerKillHotKey() -> String? {
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let me = Unmanaged.passUnretained(self).toOpaque()
        let installed = InstallEventHandler(
            GetApplicationEventTarget(),
            { _, _, user in
                guard let user else { return noErr }
                let app = Unmanaged<AppDelegate>.fromOpaque(user).takeUnretainedValue()
                app.runtime.emergencyStop(.kill)  // on this thread, not through the runtime queue
                return noErr
            }, 1, &spec, me, nil)
        guard installed == noErr else {
            return "the kill hotkey handler could not be installed (error \(installed))"
        }
        let id = EventHotKeyID(signature: OSType(0x4F53_5348), id: 1)  // 'OSSH'
        let registered = RegisterEventHotKey(
            UInt32(kVK_Escape), UInt32(controlKey | optionKey | cmdKey), id, GetApplicationEventTarget(), 0,
            &hotKeyRef)
        guard registered == noErr, hotKeyRef != nil else {
            return "Control-Option-Command-Escape could not be registered (error \(registered)); another app may use it"
        }
        return nil
    }

    // MARK: system notifications -> stuck-button protection

    private func observeSystem() {
        let dist = DistributedNotificationCenter.default()
        dist.addObserver(forName: .init("com.apple.screenIsLocked"), object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.sessionSource.noteLockNotification()
            self.runtime.emergencyStop(.lock)
        }
        dist.addObserver(forName: .init("com.apple.screenIsUnlocked"), object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.sessionSource.noteUnlockNotification()
            self.runtime.queue.async { self.runtime.refreshEnvironment() }
        }
        let center = NSWorkspace.shared.notificationCenter
        center.addObserver(forName: NSWorkspace.willSleepNotification, object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.runtime.emergencyStop(.sleep, wait: 1.0)  // bounded: sleep never waits on a stuck queue
        }
        center.addObserver(forName: NSWorkspace.sessionDidResignActiveNotification, object: nil, queue: nil) {
            [weak self] _ in
            guard let self else { return }
            self.runtime.emergencyStop(.sessionLost)
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
