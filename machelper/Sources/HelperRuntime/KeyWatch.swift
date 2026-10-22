import CryptoKit
import Foundation
import HelperCore

/// A tripwire on the helper's own key material (design 2.10, 3.7). Best effort, and honest about it:
///
///  * Another program running as the same user can delete and replace the helper's Keychain items
///    (identity, paired records, removed-devices list) and forge trust. The owner accepted that
///    (same trust model as `~/.ssh` keys). Nothing here prevents it.
///  * What this does is notice. It remembers how the three kinds of item looked when the helper
///    started and after each of its OWN writes. Before each new session and on wake the runtime asks
///    `check()`: an identity item that is missing or is not the one used in this run, or records or a
///    removed-devices list that changed without the app doing it, is a `changed` verdict. The
///    runtime then ends all sessions, switches the toggle off and tells the owner.
///  * It compares item attributes (dates, persistent references), never secrets, so it never causes a
///    Keychain prompt. A change made between the app's write and the baseline taken right after it
///    is absorbed. A program that edits the baseline in memory (it is the same user) beats this.
///  * The last-seen fingerprints are also kept in UserDefaults so a change made while the helper was
///    not running shows at the next launch. A same-user program can edit that too: a tripwire, not a
///    guarantee.
public final class KeyWatch {
    public enum Cause: Equatable, Sendable {
        case identityMissing
        case identityDifferent
        case trustListChanged
    }
    public enum Verdict: Equatable, Sendable {
        case intact
        case changed(Cause)
        /// The store could not be read (locked keychain, error). Nothing is concluded.
        case unreadable
    }

    private let store: KeyStore
    private let defaults: UserDefaults?
    private let identityKey: String
    private let trustKey: String
    private var identityStamp: Data?
    private var trustStamp: Data
    private var latched: Cause?
    private var retired = false
    /// The menu reads `trustFingerprint` on the main thread while the runtime queue rebases.
    private let lock = NSLock()

    /// This helper's own public key: its fingerprint is the first line in the menu.
    public let ownKey: Data
    /// Fingerprint of the Keychain's paired list and removed-devices list (changes when the app
    /// pairs or removes a device). Second line in the menu.
    public var trustFingerprint: String { lock.lock(); defer { lock.unlock() }; return trustFingerprintValue }
    private var trustFingerprintValue: String
    public var identityFingerprint: String { DeviceIdentity.fingerprint(ownKey) }
    /// At launch the fingerprints were not what the last run left behind.
    public let changedSinceLastLaunch: Bool

    /// - Parameter defaults: where the last-seen fingerprints are kept (nil: not kept).
    public init(
        store: KeyStore, ownKey: Data, defaults: UserDefaults? = nil, defaultsPrefix: String = "keys.lastSeen"
    ) throws {
        self.store = store; self.ownKey = ownKey; self.defaults = defaults
        identityKey = defaultsPrefix + ".identity"; trustKey = defaultsPrefix + ".trust"
        let snap = try store.snapshot()
        identityStamp = snap.identity
        trustStamp = snap.trust
        trustFingerprintValue = DeviceIdentity.fingerprint(snap.trust)
        let id = DeviceIdentity.fingerprint(ownKey)
        let prevID = defaults?.string(forKey: identityKey), prevTrust = defaults?.string(forKey: trustKey)
        changedSinceLastLaunch = (prevID != nil && prevID != id) || (prevTrust != nil && prevTrust != trustFingerprintValue)
        defaults?.set(id, forKey: identityKey)
        defaults?.set(trustFingerprintValue, forKey: trustKey)
    }

    /// The app wrote to the store itself (pair, remove): the new state is the baseline.
    public func rebase() {
        guard !retired, latched == nil, let snap = try? store.snapshot() else { return }
        trustStamp = snap.trust
        trustFingerprintValue = DeviceIdentity.fingerprint(snap.trust)
        defaults?.set(trustFingerprintValue, forKey: trustKey)
    }

    /// Compares the store with the baseline. Once something changed the verdict stays `changed`
    /// until the helper is restarted: the key in memory is no longer the one in the Keychain.
    public func check() -> Verdict {
        lock.lock(); defer { lock.unlock() }
        if retired { return .intact }
        if let latched { return .changed(latched) }
        guard let snap = try? store.snapshot() else { return .unreadable }
        let cause: Cause?
        if snap.identity == nil {
            cause = .identityMissing
        } else if snap.identity != identityStamp {
            cause = .identityDifferent
        } else if snap.trust != trustStamp {
            cause = .trustListChanged
        } else {
            cause = nil
        }
        guard let cause else { return .intact }
        latched = cause
        return .changed(cause)
    }

    /// After "Reset helper keys...": the old state is gone on purpose, nothing to watch, and the
    /// remembered fingerprints are dropped so the next launch starts clean.
    public func retire() {
        lock.lock(); defer { lock.unlock() }
        retired = true
        defaults?.removeObject(forKey: identityKey)
        defaults?.removeObject(forKey: trustKey)
    }
}
