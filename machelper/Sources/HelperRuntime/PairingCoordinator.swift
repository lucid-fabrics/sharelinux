import CryptoKit
import Foundation
import HelperCore
import os

/// What the pairing window shows. Built by `PairingCoordinator` on the runtime
/// queue; the app turns it into AppKit.
public struct PairingStatus: Equatable, Sendable {
    public enum Phase: Equatable, Sendable {
        /// No window open. The only way to open one is the owner's local action.
        case closed
        /// Open, nobody has got as far as a code.
        case waiting
        /// A device is waiting for the person to compare `code` and decide.
        case confirm(code: String, device: String)
        /// Too many failed codes lately: pairing is locked for this many more seconds.
        case lockedOut(seconds: Double)
    }
    /// Why the last window ended, or what just happened. Cleared by the next `open()`.
    public enum Notice: Equatable, Sendable {
        case paired(String)
        case rejected
        case expired
        case closedAfterFailures
        case storeFailed(String)
        /// A Confirm (or a new window) was refused; the reason is shown.
        case refused(ConfirmRefusal)
        /// "Unlock pairing" was refused because a helper session is live.
        case unlockRefusedSessionActive
        /// "Reset helper keys..." was refused because a helper session is live.
        case resetRefusedSessionActive
        /// The keys were reset; all devices must pair again.
        case keysReset
        /// Nothing was written: the keys changed outside the app (see `KeyWatch`).
        case keysChangedOutside
    }
    public var phase: Phase = .closed
    /// Another address tried to start or finish a pairing while one was in progress.
    public var anotherDeviceTrying = false
    public var notice: Notice?
    public var devices: [PairedDevice] = []
    /// This Mac's own public key, so the window can show its fingerprint beside the device's.
    public var ownKey: Data?
    /// The key of the device whose code is on screen (set with `.confirm`). A
    /// Confirm must name this key and that code, or it is refused.
    public var pendingKey: Data?
    /// The paired device whose name looks like the pending device's name, if any.
    public var nameCollision: String?
    /// Items in the key store that this app did not write (they grant nothing).
    public var ignoredRecords = 0
    /// The removed-devices list is damaged: nothing is trusted and nothing can be added until the
    /// owner chooses "Reset helper keys...".
    public var keyStoreDamaged = false
    public init() {}
}

/// How "Reset helper keys..." ended.
public enum KeyResetOutcome: Equatable, Sendable {
    case done
    case refusedSessionActive
    case failed
}

/// Why a Confirm did nothing. Each is a plain sentence in `PairingText`.
public enum ConfirmRefusal: Equatable, Sendable {
    /// Nothing is waiting for a decision.
    case noAttempt
    /// The Confirm named another device or code than the one now on screen.
    case staleAttempt
    /// The click came within a second of the code appearing or changing.
    case tooSoon
    /// A helper session is live: its clicks could be the ones landing here.
    case sessionActive
    /// The pending device's name looks like that of a device already paired.
    case nameCollision(String)
    /// Another window covers the pairing window (checked by the window); the apps that own
    /// the covering windows, when known.
    case windowCovered(by: [String])
}

/// The Mac side of the pairing flow, without any AppKit: opens the pairing
/// window on the owner's local request, feeds it the pairing datagrams, keeps
/// the paired list in a `KeyStore` and tells the UI what to show. Everything
/// runs on the runtime queue.
///
/// Only `open()` can open a window and it is only called from the menu. A
/// pairing datagram that arrives with no window gets no reply and changes
/// nothing. The code and the device name are shown only once the commit-reveal
/// is complete (`PairingWindow.pending` guarantees that).
public final class PairingCoordinator {
    private let staticKey: Curve25519.KeyAgreement.PrivateKey
    private let store: KeyStore
    private let monotonic: () -> Double
    private let ledger: PairingFailureLedger
    private let nonceSource: () -> Data
    private var window: PairingWindow?
    private var devices: [PairedDevice]
    private var notice: PairingStatus.Notice?
    private var anotherDeviceTrying = false
    private var lastPublished: PairingStatus?
    /// What the person is looking at, and since when (monotonic seconds).
    private var displayed: (code: String, key: Data, since: Double)?
    private let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "pairing")

    /// A click on Confirm within this many seconds of the code appearing or
    /// changing is ignored: it was aimed at something else.
    public static let minConfirmDelay = 1.0

    /// True while a helper session is live (the runtime sets this). Its clicks
    /// may be what lands on the window, so pairing is refused meanwhile.
    public var isSessionActive: () -> Bool = { false }

    /// Asked before the key store is written (pair, remove). False: the keys changed outside the
    /// app, nothing is written. The runtime sets this to its `KeyWatch` check.
    public var writeAllowed: () -> Bool = { true }

    /// Called on the runtime queue whenever the status changes.
    public var onChange: ((PairingStatus) -> Void)?
    /// Called on the runtime queue after any write to the key store, finished or not: a write
    /// that stopped half way (the removed-devices list updated, the record still there) has changed
    /// the store all the same, and that is the app's own doing, not a change from outside.
    public var onStoreWritten: (() -> Void)?
    /// Called on the runtime queue after the paired list changed (a device was
    /// added or removed); the runtime ends a session whose key is gone.
    public var onPairedChanged: (() -> Void)?

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey, store: KeyStore, ledger: PairingFailureLedger,
        monotonic: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime },
        nonceSource: @escaping () -> Data = randomPairingNonce
    ) throws {
        self.staticKey = staticKey; self.store = store; self.ledger = ledger
        self.monotonic = monotonic; self.nonceSource = nonceSource
        devices = try store.loadPaired()
    }

    // MARK: reads

    public var pairedKeys: [Data] { devices.map(\.key) }
    public func name(for key: Data) -> String? { devices.first { constantTimeEqual($0.key, key) }?.name }

    public var status: PairingStatus {
        var s = PairingStatus()
        s.ownKey = staticKey.publicKey.rawRepresentation
        s.devices = devices
        s.anotherDeviceTrying = anotherDeviceTrying
        s.notice = notice
        s.ignoredRecords = store.ignoredRecords
        s.keyStoreDamaged = store.revocationDamaged
        if let remaining = ledger.lockoutRemaining {
            s.phase = .lockedOut(seconds: remaining.rounded(.up))
        } else if let w = window, w.isOpen {
            if let p = w.pending, let code = p.sas, let name = p.deviceName {
                s.phase = .confirm(code: code, device: name)
                s.pendingKey = p.remoteStatic
                s.nameCollision = collidingDevice(name: name, key: p.remoteStatic)
            } else {
                s.phase = .waiting
            }
        }
        return s
    }

    /// A paired device (under another key) whose name looks like `name`.
    private func collidingDevice(name: String, key: Data?) -> String? {
        devices.first { d in
            if let key, constantTimeEqual(d.key, key) { return false }
            return DeviceIdentity.looksAlike(d.name, name)
        }?.name
    }

    // MARK: local actions (the owner, at the Mac)

    /// Opens a two-minute window for one pairing. Refused (with the cooldown
    /// shown) while the failure cap holds pairing locked. Returns whether a window is open now.
    @discardableResult
    public func open() -> Bool {
        window?.close()
        window = nil
        notice = nil
        anotherDeviceTrying = false
        if isSessionActive() {
            notice = .refused(.sessionActive)
            publish()
            return false
        }
        if ledger.isLockedOut { publish(); return false }
        let w = PairingWindow(staticKey: staticKey, clock: monotonic, nonceSource: nonceSource, ledger: ledger)
        w.onEvent = { [weak self] e in self?.windowEvent(e) }
        window = w
        publish()
        return true
    }

    /// The person closes the window.
    public func close() {
        window?.close()
        window = nil
        anotherDeviceTrying = false
        publish()
    }

    /// "Confirm": the person says the codes match. It must name the code and
    /// the device key now on screen: a Confirm for anything else (an attempt
    /// that was replaced while the person was clicking) is refused and pairs
    /// nothing. Also refused within `minConfirmDelay` of the code appearing or
    /// changing, while a helper session is live, and when the device's name
    /// looks like one already paired. Pins the device in the key store; if the
    /// store refuses, nothing is paired and the notice says so.
    /// - Returns: nil when the device was handed to the store, otherwise why not.
    @discardableResult
    public func confirm(code: String, peerKey: Data) -> ConfirmRefusal? {
        track()
        let refusal = confirmRefusal(code: code, peerKey: peerKey)
        if let refusal {
            // A refusal keeps the attempt open (the person can look again), except
            // that the reason is shown.
            notice = .refused(refusal)
            publish()
            return refusal
        }
        guard let w = window, let paired = w.confirm() else { return .noAttempt }
        window = nil
        anotherDeviceTrying = false
        displayed = nil
        guard writeAllowed() else {
            notice = .keysChangedOutside
            publish()
            return nil
        }
        defer { onStoreWritten?() }
        do {
            try store.addPaired(PairedDevice(name: paired.name, key: paired.key))
            devices = try store.loadPaired()
            notice = .paired(paired.name)
            log.info("paired with \(paired.key.prefix(4).hex, privacy: .public)")
            publish()
            onPairedChanged?()
        } catch KeyStoreError.corrupt {
            notice = .storeFailed(PairingText.damagedShort)
            log.error("could not store a paired key: the removed-devices list is damaged")
            publish()
        } catch {
            notice = .storeFailed("The Keychain did not accept the key.")
            log.error("could not store a paired key")
            publish()
        }
        return nil
    }

    private func confirmRefusal(code: String, peerKey: Data) -> ConfirmRefusal? {
        guard let w = window, w.isOpen, let p = w.pending, let sas = p.sas, let key = p.remoteStatic,
            let name = p.deviceName
        else { return .noAttempt }
        guard code == sas, constantTimeEqual(key, peerKey) else { return .staleAttempt }
        if isSessionActive() { return .sessionActive }
        if let shown = displayed, monotonic() - shown.since < Self.minConfirmDelay { return .tooSoon }
        if let other = collidingDevice(name: name, key: key) { return .nameCollision(other) }
        return nil
    }

    /// Notes when the code on screen appeared or changed.
    private func track() {
        if let w = window, w.isOpen, let p = w.pending, let sas = p.sas, let key = p.remoteStatic {
            if let d = displayed, d.code == sas, constantTimeEqual(d.key, key) { return }
            displayed = (sas, key, monotonic())
        } else {
            displayed = nil
        }
    }

    /// "Reject": the codes differ. Counts as a failed attempt; the window stays
    /// open for another try unless that was the last one.
    public func reject() {
        guard let w = window else { return }
        w.reject()
        notice = w.isOpen ? .rejected : .closedAfterFailures
        if !w.isOpen { window = nil }
        anotherDeviceTrying = false
        publish()
    }

    /// The owner clears the cooldown on purpose (the UI asks first). Refused while a helper
    /// session is live: the clicks that reach the dialog could be that session's.
    /// - Returns: whether the cooldown was cleared.
    @discardableResult
    public func resetCooldown() -> Bool {
        track()
        if isSessionActive() {
            notice = .unlockRefusedSessionActive
            publish()
            return false
        }
        ledger.reset()
        publish()
        return true
    }

    /// "Reset helper keys...": the owner confirmed. Deletes the identity, every record and the
    /// removed-devices list. Refused while a helper session is live (its clicks could be the ones
    /// landing on the confirmation). Afterwards nothing is paired.
    public func resetAllKeys() -> KeyResetOutcome {
        if isSessionActive() {
            notice = .resetRefusedSessionActive
            publish()
            return .refusedSessionActive
        }
        window?.close()
        window = nil
        defer { onStoreWritten?() }
        do { try store.resetAll() } catch {
            notice = .storeFailed("The Keychain did not let go of the keys.")
            publish()
            return .failed
        }
        devices = []
        notice = .keysReset
        log.info("helper keys reset")
        publish()
        return .done
    }

    /// Removes a paired device. The runtime then drops its live session.
    public func remove(key: Data) {
        guard writeAllowed() else {
            notice = .keysChangedOutside
            publish()
            return
        }
        defer { onStoreWritten?() }
        do {
            try store.removePaired(key: key)
            devices = try store.loadPaired()
        } catch {
            // The key stays in the list shown, and stays trusted, if it cannot be deleted.
            notice = .storeFailed("The Keychain did not remove the key.")
            publish()
            return
        }
        publish()
        onPairedChanged?()
    }

    /// Call about every 100 ms: notices a window that timed out or closed.
    public func tick() {
        if let w = window, !w.isOpen {
            window = nil
            if notice == nil { notice = ledger.isLockedOut ? .closedAfterFailures : .expired }
            anotherDeviceTrying = false
            publish()
        } else if status != lastPublished {
            publish()  // a cooldown ran out
        }
    }

    // MARK: datagrams

    /// A pairing datagram (kinds 0x10 to 0x13). No window: no reply.
    public func receive(_ datagram: Data, from source: PeerAddress?) -> Data? {
        guard let w = window else { return nil }
        let reply = try? w.receive(datagram, from: source)
        if !w.isOpen {
            // The window shut itself (three failures, the cap, or its time).
            window = nil
            notice = ledger.isLockedOut || w.failures >= ChannelLimits.pairingMaxFailures ? .closedAfterFailures : .expired
        }
        publish()
        return reply
    }

    private func windowEvent(_ e: PairingWindowEvent) {
        switch e {
        case .anotherDeviceTrying, .sourceRefused: anotherDeviceTrying = true
        case .lockedOut: break
        }
    }

    private func publish() {
        track()
        let s = status
        guard s != lastPublished else { return }
        lastPublished = s
        onChange?(s)
    }
}
