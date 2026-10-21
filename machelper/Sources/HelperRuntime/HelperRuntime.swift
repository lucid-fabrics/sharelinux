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

    public init(allowRawMouse: Bool, device: String?, injecting: Bool, lastDeny: HelperState?) {
        self.allowRawMouse = allowRawMouse; self.device = device
        self.injecting = injecting; self.lastDeny = lastDeny
    }
}

/// The local "Allow raw mouse" switch. Default off on every launch and never
/// persisted: only a click at this Mac turns it on, and the remote side cannot
/// reach it.
final class LocalSwitch { var isOn = false }

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
    public let monitor: EnvironmentMonitor
    private let channel: HelperChannel
    private let localSwitch = LocalSwitch()
    private let monotonic: () -> Double
    private let deviceName: (Data) -> String
    private let pairedKeys: () -> [Data]
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
    /// Called on `queue` when a session starts (the app posts a notification).
    public var onSessionStart: ((String) -> Void)?

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey, pairedKeys: @escaping () -> [Data], poster: EventPoster,
        session: SessionStateSource, sharing: ScreenSharingPeerSource, access: AccessSource,
        ownUID: UInt32, clock: ChannelClock, timestamps: TimestampStore,
        deviceName: @escaping (Data) -> String = HelperRuntime.fingerprintName,
        doubleClickInterval: @escaping () -> Double = { 0.5 },
        queue: DispatchQueue = DispatchQueue(label: "io.github.sharelinux.mousehelper.runtime")
    ) {
        self.queue = queue
        staticPublicKey = staticKey.publicKey.rawRepresentation
        self.monotonic = clock.monotonic
        self.deviceName = deviceName
        self.pairedKeys = pairedKeys
        controller = MouseController(poster: poster, clock: clock.monotonic, doubleClickInterval: doubleClickInterval)
        let mon = EnvironmentMonitor(
            session: session, sharing: sharing, access: access, ownUID: ownUID, clock: clock.monotonic)
        monitor = mon
        let sw = localSwitch
        channel = HelperChannel(
            staticKey: staticKey, pairedKeys: pairedKeys,
            environment: { peer, _ in mon.gateInputs(udpPeer: peer, toggleOn: sw.isOn) },
            clock: clock, timestamps: timestamps)
        controller.onInjectingChange = { [weak self] _ in self?.publish() }
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
        let r = channel.receive(datagram, from: source)
        process(r.events, source: source)
        return r.reply
    }

    /// Samples the environment. Call about every 100 ms and after any
    /// lock/console/sleep notification. A sample that makes things worse
    /// releases all buttons at once.
    public func refreshEnvironment() {
        let (old, new) = monitor.refresh()
        if let old, isWorse(new, than: old) { release(.environment) }
    }

    /// Watchdogs. Call at least every 100 ms.
    public func tick() {
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

    /// The menu switch. Turning it off ends any session.
    public func setAllowRawMouse(_ on: Bool) {
        localSwitch.isOn = on
        if !on { stopEverything(.toggleOff) }
        publish()
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
