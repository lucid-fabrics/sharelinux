import AppKit
import Carbon.HIToolbox
import CryptoKit
import Foundation
import HelperCore
import HelperRuntime
import PairingUI
import UserNotifications
import os

// sharelinux Mouse Helper: menu bar app that posts relative mouse motion for a paired
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
    private var helperMenu: HelperMenu!
    private var keyWatch: KeyWatch!
    private var standingProblem: String?
    private var state = RuntimeState(allowRawMouse: false, device: nil, injecting: false, lastDeny: nil)
    private var notificationsAsked = false
    private var pairing: PairingCoordinator!
    private var pairingWindow: PairingWindowController!
    private var pairingUpdates: StateCoalescer<PairingStatus>!

    func applicationDidFinishLaunching(_ note: Notification) {
        // The identity key and the paired devices live in the login Keychain. Files in the
        // support directory (timestamps, the failure cooldown) hold no secrets and no trust; an older
        // build's key and paired files are deleted unread, never imported.
        let store = IdentityStore(directory: IdentityStore.defaultDirectory())
        let keys = KeychainKeyStore()
        let key: Curve25519.KeyAgreement.PrivateKey
        do {
            key = try store.openIdentity(keys: keys)
            pairing = try PairingCoordinator(
                staticKey: key, store: keys,
                ledger: PairingFailureLedger(store: FilePairingFailureStore(url: store.pairingFailuresURL)))
            keyWatch = try KeyWatch(store: keys, ownKey: key.publicKey.rawRepresentation, defaults: .standard)
        } catch {
            log.error("cannot open the key store: \(String(describing: error), privacy: .public)")
            offerResetAtLaunch(keys: keys, error: error)
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
            doubleClickInterval: { NSEvent.doubleClickInterval }, pairing: pairing, keyWatch: keyWatch)
        // Coalesced: a peer flipping allowed/denied per packet must not flood the main thread,
        // where the hotkey and "Stop now" run.
        runtime.observeState(on: .main) { [weak self] s in self?.apply(s) }
        runtime.onKeysChanged = { [weak self] in DispatchQueue.main.async { self?.showKeysChanged() } }
        runtime.onSessionStart = { [weak self] device in DispatchQueue.main.async { self?.notifySessionStart(device) } }

        pairingWindow = PairingWindowController()
        pairingWindow.onConfirm = { [unowned self] code, key in
            runtime.queue.async { self.pairing.confirm(code: code, peerKey: key) }
        }
        pairingWindow.isSessionLive = { [unowned self] in state.pairingBlocked }
        pairingWindow.onReject = { [unowned self] in runtime.queue.async { self.pairing.reject() } }
        pairingWindow.onClose = { [unowned self] in runtime.queue.async { self.pairing.close() } }
        pairingWindow.onUnlock = { [unowned self] in runtime.queue.async { self.pairing.resetCooldown() } }
        pairingWindow.onRemove = { [unowned self] d in
            // Buttons up here, now; the runtime queue then deletes the key and ends the session.
            _ = runtime.removePairedDevice(d.key)
        }
        pairingUpdates = StateCoalescer<PairingStatus>(target: .main, minInterval: 0.05) { [weak self] s in
            self?.pairingWindow.update(s)
            // The paired-list fingerprint moves when a device is paired or removed.
            if let self { self.helperMenu?.update(self.menuEntries(for: self.state)) }
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
            standingProblem = "Cannot open UDP port \(UDPServer.defaultPort)"
        }
        apply(runtime.state)
        if runtime.state.keysReviewPending {
            // The fingerprints differ from what the last run left behind. The runtime already holds
            // everything off; the owner reviews them and decides. A same-user program can edit that
            // note too: a tripwire, not a guarantee (design 3.7).
            DispatchQueue.main.async { [self] in reviewKeys() }
        }
    }

    /// The keys changed outside the app. Everything is already off; this tells the owner.
    private func showKeysChanged() {
        NSApp.activate(ignoringOtherApps: true)
        HelperDialogs.keysChanged().runModal()
    }

    /// Shows the fingerprints from the last run next to the ones now, and what each choice does.
    /// Nothing is accepted from any device until "Keep paired devices" or a reset.
    @objc private func reviewKeys() {
        guard runtime.state.keysReviewPending else { return }
        NSApp.activate(ignoringOtherApps: true)
        let alert = HelperDialogs.keysReview(
            identityBefore: keyWatch.previousIdentityFingerprint, identityNow: keyWatch.identityFingerprint,
            pairedListBefore: keyWatch.previousTrustFingerprint, pairedListNow: keyWatch.trustFingerprint)
        switch alert.runModal() {
        case .alertSecondButtonReturn:
            runtime.queue.async { self.runtime.keepPairedDevices() }
        case .alertThirdButtonReturn:
            resetKeys()
        default:
            break  // stays held; the menu has "Review changed keys..."
        }
    }

    /// The Keychain items are damaged so badly that the helper cannot start: offer the reset here,
    /// so nobody has to open Keychain Access.
    private func offerResetAtLaunch(keys: KeychainKeyStore, error: Error) {
        NSApp.activate(ignoringOtherApps: true)
        guard HelperDialogs.resetConfirm().runModal() == .alertSecondButtonReturn else { return }
        do {
            try keys.resetAll()
            keyWatchRetireDefaults()
            HelperDialogs.resetDone().runModal()
        } catch {
            HelperDialogs.resetFailed().runModal()
        }
    }

    private func keyWatchRetireDefaults() {
        UserDefaults.standard.removeObject(forKey: "keys.lastSeen.identity")
        UserDefaults.standard.removeObject(forKey: "keys.lastSeen.trust")
    }

    // MARK: menu

    private func buildMenu() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        // The only way to open a pairing window is the owner's click on its menu item.
        helperMenu = HelperMenu(
            target: self,
            actions: [
                .toggle: #selector(toggle), .stop: #selector(stopNow), .review: #selector(reviewKeys),
                .grant: #selector(requestAccess),
                .pair: #selector(openPairing), .reset: #selector(resetKeys), .quit: #selector(quit),
            ],
            keyEquivalents: [.quit: "q"], entries: menuEntries(for: state))
        statusItem.menu = helperMenu.menu
    }

    private func menuEntries(for s: RuntimeState) -> [HelperMenuEntry] {
        HelperMenuModel.entries(
            state: s, cannotEnable: runtime?.cannotEnableReason, accessGranted: SystemAccessSource().granted(),
            identity: keyWatch?.identityFingerprint ?? "", pairedList: keyWatch?.trustFingerprint ?? "",
            problem: standingProblem)
    }

    /// Updates the menu bar icon and menu text. The filled pointer with click
    /// marks means events are being posted right now; a dimmed plain pointer
    /// means the switch is off.
    private func apply(_ s: RuntimeState) {
        state = s
        let name = s.injecting ? "cursorarrow.click.2" : "cursorarrow"
        statusItem.button?.image = NSImage(systemSymbolName: name, accessibilityDescription: "sharelinux Mouse Helper")
        statusItem.button?.alphaValue = s.allowRawMouse ? 1.0 : 0.5
        badge.update(text: s.badgeText)
        helperMenu.update(menuEntries(for: s))
    }

    @objc private func toggle() {
        let on = !state.allowRawMouse
        runtime.queue.async { self.runtime.setAllowRawMouse(on) }  // refused while cannotEnableReason is set
    }

    /// Released on this thread at once; the runtime queue only ends the session afterwards.
    @objc private func stopNow() { runtime.emergencyStop(.kill) }

    @objc private func openPairing() {
        // Not while a session could inject: its clicks could be the ones landing here.
        guard !state.pairingBlocked else { return }
        pairingWindow.presentFromOwnerClick()
        runtime.queue.async { self.pairing.open() }
    }

    /// "Reset helper keys...": a confirmation first, refused while a session is live (its clicks could
    /// be the ones landing on the confirmation). The key in memory is the old one, so the helper quits
    /// afterwards; the next launch makes a fresh identity.
    @objc private func resetKeys() {
        guard !state.pairingBlocked else {
            NSApp.activate(ignoringOtherApps: true)
            HelperDialogs.resetRefused().runModal()
            return
        }
        NSApp.activate(ignoringOtherApps: true)
        guard HelperDialogs.resetConfirm().runModal() == .alertSecondButtonReturn else { return }
        runtime.queue.async { [self] in
            let outcome = runtime.resetHelperKeys()
            DispatchQueue.main.async { [self] in
                switch outcome {
                case .done:
                    HelperDialogs.resetDone().runModal()
                    server.stop()
                    NSApp.terminate(nil)
                case .refusedSessionActive: HelperDialogs.resetRefused().runModal()
                case .failed: HelperDialogs.resetFailed().runModal()
                }
            }
        }
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
        center.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: nil) { [weak self] _ in
            guard let self else { return }
            self.runtime.queue.async { self.runtime.didWake() }  // looks at the keys again
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
