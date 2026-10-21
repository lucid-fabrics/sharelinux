import Foundation
import CryptoKit
import HelperCore
import os

/// What the menu bar shows.
public struct RuntimeState: Equatable, Sendable {
    public var allowRawMouse: Bool
    /// Display name of the connected device, nil without a session.
    public var device: String?
    /// Events are being posted right now (filled pointer icon).
    public var injecting: Bool
    /// Last reason input was refused, nil while accepted or without a session.
    public var lastDeny: HelperState?

    /// The text of the always-on-top badge: shown exactly while events are being
    /// posted, whatever the menu bar does (the notch or "hide menu bar icon"
    /// can hide the icon; the badge is a separate window). nil: no badge.
    public var badgeText: String? {
        injecting ? "Raw mouse active: \(device ?? "a device")" : nil
    }

    public init(allowRawMouse: Bool, device: String?, injecting: Bool, lastDeny: HelperState?) {
        self.allowRawMouse = allowRawMouse; self.device = device
        self.injecting = injecting; self.lastDeny = lastDeny
    }
}

/// The local "Allow raw mouse" switch. Default off on every launch and never
/// persisted: only a click at this Mac turns it on, and the remote side cannot
/// reach it.
final class LocalSwitch {
    private let state = OSAllocatedUnfairLock(initialState: false)
    /// Readable and writable from any thread: the kill switch turns it off
    /// without going through the runtime queue.
    var isOn: Bool {
        get { state.withLock { $0 } }
        set { state.withLock { $0 = newValue } }
    }
}

/// Local emergencies that stop input before the runtime queue gets to run.
public enum EmergencyKind: Sendable {
    /// "Stop now", the hotkey, Quit: switch off, session over.
    case kill
    /// System going to sleep: session over, switch unchanged.
    case sleep
    /// Screen locked: buttons up, session stays.
    case lock
    /// The user session left the console: buttons up, session stays.
    case sessionLost
}

/// Wires channel, gate, environment monitor and mouse controller together.
/// Everything runs on `queue` (one serial queue), so no locks are needed; the
/// UDP server and the app dispatch onto it.
///
/// Stuck-button rules, all through `MouseController.releaseAll`: any gate deny,
/// session end (bye, silence, expiry, replaced, kill switch, toggle off,
/// Screen Sharing gone), environment getting worse (lock, console lost, grant
/// lost, Screen Sharing peer lost), sleep, and the controller's own watchdog.
public final class HelperRuntime {
    public let queue: DispatchQueue
    public let controller: MouseController
    /// Everything the controller posts goes through this guard.
    public let guardedPoster: GuardedPoster
    public let monitor: EnvironmentMonitor
    private let channel: HelperChannel
    private let localSwitch = LocalSwitch()
    private let monotonic: () -> Double
    private let deviceName: (Data) -> String
    private let pairedKeys: () -> [Data]
    /// The pairing flow, or nil (tests and the harness that do not pair).
    public let pairing: PairingCoordinator?
    private let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "runtime")

    private var activeKey: Data?
    private var activeSource: PeerAddress?
    private var sharingSeen = false
    private var device: String?
    private var lastDeny: HelperState?
    private var acceptedInputs = 0, deniedInputs = 0, droppedDatagrams = 0
    private var lastLoggedDeny: HelperState?

    /// Called on `queue` whenever the visible state changes.
    public var onState: ((RuntimeState) -> Void)?
    private var stateCoalescer: StateCoalescer<RuntimeState>?

    /// Delivers visible-state changes to `target` (the main queue in the app)
    /// through a `StateCoalescer`: at most one update pending and at most
    /// `1 / minInterval` per second, whatever the peer does. Replaces `onState`.
    @discardableResult
    public func observeState(
        on target: DispatchQueue, minInterval: Double = 0.1, _ handler: @escaping (RuntimeState) -> Void
    ) -> StateCoalescer<RuntimeState> {
        let c = StateCoalescer<RuntimeState>(target: target, minInterval: minInterval, deliver: handler)
        stateCoalescer = c
        onState = { [c] in c.push($0) }
        return c
    }

    /// Called on `queue` when a session starts (the app posts a notification).
    public var onSessionStart: ((String) -> Void)?

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey, pairedKeys: @escaping () -> [Data], poster: EventPoster,
        session: SessionStateSource, sharing: ScreenSharingPeerSource, access: AccessSource,
        ownUID: UInt32, clock: ChannelClock, timestamps: TimestampStore,
        deviceName: @escaping (Data) -> String = HelperRuntime.fingerprintName,
        doubleClickInterval: @escaping () -> Double = { 0.5 },
        pairing: PairingCoordinator? = nil,
        queue: DispatchQueue = DispatchQueue(label: "io.github.sharelinux.mousehelper.runtime")
    ) {
        self.queue = queue
        self.pairing = pairing
        staticPublicKey = staticKey.publicKey.rawRepresentation
        self.monotonic = clock.monotonic
        self.deviceName = deviceName
        self.pairedKeys = pairedKeys
        guardedPoster = GuardedPoster(poster)
        controller = MouseController(poster: guardedPoster, clock: clock.monotonic, doubleClickInterval: doubleClickInterval)
        let mon = EnvironmentMonitor(
            session: session, sharing: sharing, access: access, ownUID: ownUID, clock: clock.monotonic)
        monitor = mon
        let sw = localSwitch
        channel = HelperChannel(
            staticKey: staticKey, pairedKeys: pairedKeys,
            environment: { peer, _ in mon.gateInputs(udpPeer: peer, toggleOn: sw.isOn) },
            clock: clock, timestamps: timestamps)
        controller.onInjectingChange = { [weak self] _ in self?.publish() }
        pairing?.onPairedChanged = { [weak self] in self?.pairedKeysChanged() }
        // Called on `queue`, where `device` lives. A live session means the person's own
        // clicks may be landing on the pairing window: no new pairing meanwhile.
        pairing?.isSessionActive = { [weak self] in self?.device != nil }
        mon.refresh()
    }

    /// "Device 1a2b3c4d": the key fingerprint, when no better name is known.
    public static func fingerprintName(_ key: Data) -> String { "Device " + key.prefix(4).hex }

    public let staticPublicKey: Data
    public var allowRawMouse: Bool { localSwitch.isOn }
    public var state: RuntimeState {
        RuntimeState(
            allowRawMouse: localSwitch.isOn, device: device, injecting: controller.injecting, lastDeny: lastDeny)
    }

    // MARK: datagrams and time

    /// One datagram from the socket. Returns the reply to send, if any.
    public func handle(datagram: Data, from source: PeerAddress?) -> Data? {
        // Pairing handshake kinds go to the pairing window (open only after the
        // owner's local click). A runtime without pairing ignores them.
        if let kind = datagram.first, Self.pairingKinds.contains(kind) {
            return pairing?.receive(datagram, from: source)
        }
        let r = channel.receive(datagram, from: source)
        process(r.events, source: source)
        return r.reply
    }

    static let pairingKinds: Set<UInt8> = [
        DatagramKind.xxMessage1.rawValue, DatagramKind.xxMessage2.rawValue, DatagramKind.xxMessage3.rawValue,
        DatagramKind.pairingTransport.rawValue,
    ]

    /// Samples the environment. Call about every 100 ms and after any
    /// lock/console/sleep notification. A sample that makes things worse
    /// releases all buttons at once.
    public func refreshEnvironment() {
        let (old, new) = monitor.refresh()
        if let old, isWorse(new, than: old) { release(.environment) }
    }

    /// Watchdogs. Call at least every 100 ms.
    public func tick() {
        pairing?.tick()
        if let e = channel.tick() { process([e], source: nil) }
        controller.tick()
        watchActiveSession()
    }

    private func isWorse(_ n: EnvironmentSnapshot, than o: EnvironmentSnapshot) -> Bool {
        let ownUID = monitor.ownUID
        if o.consoleUID == ownUID, n.consoleUID != ownUID { return true }
        if o.screenLocked == false, n.screenLocked != false { return true }
        if o.accessGranted, !n.accessGranted { return true }
        if let op = o.peers, !(n.peers ?? []).isSuperset(of: op) { return true }
        return false
    }

    /// The gate is checked per packet; this also checks a quiet session every
    /// tick so a lost Screen Sharing connection ends it within the sampling
    /// interval, not at the next packet.
    private func watchActiveSession() {
        guard let key = activeKey else { return }
        var inputs = monitor.gateInputs(udpPeer: activeSource, toggleOn: localSwitch.isOn)
        inputs.peerPaired = pairedKeys().contains { constantTimeEqual($0, key) }
        switch Gate.evaluate(inputs, now: monotonic()) {
        case .accept:
            sharingSeen = true
        case .dropSilently:
            end(.unpaired)
        case .deny(let s):
            release(.environment)
            // A session that has seen its Screen Sharing connection ends with it.
            // One that connected first and is still waiting for it just waits.
            if s == .noSession {
                if sharingSeen { end(.screenSharingEnded) }
            } else {
                sharingSeen = sharingSeen || inputs.udpPeer.map { inputs.screenSharingPeers.contains($0) } ?? false
            }
        }
    }

    // MARK: events from the channel

    private func process(_ events: [ChannelEvent], source: PeerAddress?) {
        var changed = false
        for e in events {
            switch e {
            case .sessionStarted(let key):
                activeKey = key; activeSource = source; sharingSeen = false
                acceptedInputs = 0; deniedInputs = 0; droppedDatagrams = 0
                lastDeny = nil; lastLoggedDeny = nil
                device = deviceName(key)
                log.info("session started with \(key.prefix(4).hex, privacy: .public)")
                onSessionStart?(device ?? "")
                changed = true
            case .input(let p):
                activeSource = source ?? activeSource
                sharingSeen = true
                acceptedInputs += 1
                if lastDeny != nil { lastDeny = nil; lastLoggedDeny = nil; changed = true }
                controller.apply(p)
            case .keepalive:
                activeSource = source ?? activeSource
                sharingSeen = true
                if lastDeny != nil { lastDeny = nil; lastLoggedDeny = nil; changed = true }
                controller.touch()
            case .denied(let s):
                activeSource = source ?? activeSource
                deniedInputs += 1
                controller.releaseAll(.denied)
                if lastDeny != s { lastDeny = s; changed = true }
                if lastLoggedDeny != s {
                    lastLoggedDeny = s
                    log.info("input refused: \(String(describing: s), privacy: .public)")
                }
            case .ended(let reason):
                controller.releaseAll(.ended)
                log.info(
                    "session ended (\(String(describing: reason), privacy: .public)): \(self.acceptedInputs) accepted, \(self.deniedInputs) refused, \(self.droppedDatagrams) dropped"
                )
                activeKey = nil; activeSource = nil; device = nil; lastDeny = nil
                changed = true
            case .dropped:
                droppedDatagrams += 1
            case .handshakeAnswered:
                break
            }
        }
        if changed { publish() }
    }

    private func release(_ reason: ReleaseReason) {
        controller.releaseAll(reason)
    }

    private func end(_ reason: EndReason) {
        controller.releaseAll(.ended)
        if let e = channel.endSession(reason: reason) { process([e], source: nil) }
    }

    private func publish() { onState?(state) }

    // MARK: local actions (menu, hotkey, system notifications)

    /// Why the switch cannot be turned on, nil when it can. The app sets this
    /// when the kill hotkey could not be registered: the owner must always have
    /// a way to stop that does not depend on a menu bar icon that can be hidden.
    public var cannotEnableReason: String? {
        get { enableBlock.withLock { $0 } }
        set { enableBlock.withLock { $0 = newValue } }
    }
    private let enableBlock = OSAllocatedUnfairLock<String?>(initialState: nil)

    /// The menu switch. Turning it off ends any session. Turning it on is
    /// refused while `cannotEnableReason` is set; returns whether the switch
    /// ended up as asked.
    @discardableResult
    public func setAllowRawMouse(_ on: Bool) -> Bool {
        if on, cannotEnableReason != nil {
            localSwitch.isOn = false
            publish()
            return false
        }
        localSwitch.isOn = on
        if !on { stopEverything(.toggleOff) }
        publish()
        return true
    }

    /// The kill switch for handlers that must not wait for the runtime queue
    /// (hotkey, menu, sleep, lock). Callable from any thread. On the calling
    /// thread, right now: the switch goes off (for `.kill`), posting stops and
    /// every held button comes up. Then the runtime queue ends the session and
    /// releases the controller as usual, and lifts the posting block.
    ///
    /// - Parameter wait: how long to wait for the queue to finish (0: not at
    ///   all). Sleep and Quit use a short wait and carry on after it: the
    ///   buttons are already up whether or not the queue answered.
    /// - Returns: true when the queue finished within `wait`.
    @discardableResult
    public func emergencyStop(_ kind: EmergencyKind, wait: TimeInterval = 0) -> Bool {
        if kind == .kill { localSwitch.isOn = false }
        guardedPoster.beginStop()
        let done = DispatchSemaphore(value: 0)
        queue.async { [self] in
            switch kind {
            case .kill: killSwitch()
            case .sleep: willSleep()
            case .lock: environmentInterruption(.locked)
            case .sessionLost: environmentInterruption(.environment)
            }
            guardedPoster.endStop()
            done.signal()
        }
        return wait > 0 && done.wait(timeout: .now() + wait) == .success
    }

    /// Kill switch (menu "Stop now" and the hotkey): releases buttons, ends the
    /// session and turns the switch off until the owner turns it back on.
    public func killSwitch() {
        localSwitch.isOn = false
        stopEverything(.killSwitch)
        publish()
    }

    /// The screen locked or the session left the console (lock notification,
    /// fast user switching): buttons up at once. The session stays, so the next
    /// packets are answered with the `locked` / `notConsole` status instead of
    /// the client having to reconnect after unlocking.
    public func environmentInterruption(_ reason: ReleaseReason) {
        refreshEnvironment()
        controller.releaseAll(reason)
    }

    /// System going to sleep: buttons up, session over.
    public func willSleep() {
        stopEverything(.sleep)
    }

    /// Removes a paired device and ends its live session. Callable from any
    /// thread: posting stops and held buttons come up on the calling thread
    /// first (like the kill switch), then the runtime queue deletes the key and
    /// ends the session, so a flooded queue cannot keep a removed device moving the mouse.
    /// - Returns: true when the queue finished within `wait`.
    @discardableResult
    public func removePairedDevice(_ key: Data, wait: TimeInterval = 0) -> Bool {
        guard let pairing else { return false }
        guardedPoster.beginStop()
        let done = DispatchSemaphore(value: 0)
        queue.async { [self] in
            pairing.remove(key: key)  // calls pairedKeysChanged on success
            guardedPoster.endStop()
            done.signal()
        }
        return wait > 0 && done.wait(timeout: .now() + wait) == .success
    }

    /// The paired list changed (a device was removed): drop a session whose key is gone.
    public func pairedKeysChanged() {
        guard let key = activeKey, !pairedKeys().contains(where: { constantTimeEqual($0, key) }) else { return }
        end(.unpaired)
    }

    private func stopEverything(_ reason: ReleaseReason) {
        controller.releaseAll(reason)
        if let e = channel.endSession(reason: .local) { process([e], source: nil) }
    }
}
