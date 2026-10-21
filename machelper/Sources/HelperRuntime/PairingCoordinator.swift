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
    }
    public var phase: Phase = .closed
    /// Another address tried to start or finish a pairing while one was in progress.
    public var anotherDeviceTrying = false
    public var notice: Notice?
    public var devices: [PairedDevice] = []
    public init() {}
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
    private let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "pairing")

    /// Called on the runtime queue whenever the status changes.
    public var onChange: ((PairingStatus) -> Void)?
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
        s.devices = devices
        s.anotherDeviceTrying = anotherDeviceTrying
        s.notice = notice
        if let remaining = ledger.lockoutRemaining {
            s.phase = .lockedOut(seconds: remaining.rounded(.up))
        } else if let w = window, w.isOpen {
            if let p = w.pending, let code = p.sas, let name = p.deviceName {
                s.phase = .confirm(code: code, device: name)
            } else {
                s.phase = .waiting
            }
        }
        return s
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

    /// "Confirm": the person says the codes match. Pins the device in the key
    /// store; if the store refuses, nothing is paired and the notice says so.
    public func confirm() {
        guard let w = window, let paired = w.confirm() else { return }
        window = nil
        anotherDeviceTrying = false
        do {
            try store.addPaired(PairedDevice(name: paired.name, key: paired.key))
            devices = try store.loadPaired()
            notice = .paired(paired.name)
            log.info("paired with \(paired.key.prefix(4).hex, privacy: .public)")
            publish()
            onPairedChanged?()
        } catch {
            notice = .storeFailed("The Keychain did not accept the key.")
            log.error("could not store a paired key")
            publish()
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

    /// The owner clears the cooldown on purpose (the UI asks first).
    public func resetCooldown() {
        ledger.reset()
        publish()
    }

    /// Removes a paired device. The runtime then drops its live session.
    public func remove(key: Data) {
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
        let s = status
        guard s != lastPublished else { return }
        lastPublished = s
        onChange?(s)
    }
}
