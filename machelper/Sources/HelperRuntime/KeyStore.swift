import CryptoKit
import Foundation
import HelperCore

/// A Linux device the owner paired with.
public struct PairedDevice: Equatable, Sendable {
    public var name: String
    /// The device's static Curve25519 public key (32 bytes).
    public var key: Data
    public init(name: String, key: Data) { self.name = name; self.key = key }
}

public enum KeyStoreError: Error, Equatable {
    /// The store refused or failed (Keychain status, locked, denied).
    case failed(String)
    /// An item in the store is not what the helper wrote (wrong size, key and
    /// account disagree): it is ignored when listing and reported when asked for directly.
    case corrupt(String)
}

/// Where the helper's own static private key and the pinned peer public keys
/// live. The app uses the login Keychain (`KeychainKeyStore`); tests and the
/// harness use memory. Keys are never logged.
public protocol KeyStore: AnyObject {
    /// The helper's static key, created on first use.
    func loadOrCreateIdentity() throws -> Curve25519.KeyAgreement.PrivateKey
    func loadPaired() throws -> [PairedDevice]
    /// Adds a device, or replaces the name of one already paired under the same key.
    func addPaired(_ device: PairedDevice) throws
    func removePaired(key: Data) throws
    /// How many items the last `loadPaired` skipped because this app did not write
    /// them (a foreign process planted them, or they were altered). They grant nothing.
    var ignoredRecords: Int { get }
    /// True when the last `loadPaired` found the list of removed devices damaged (present but not
    /// authentic): nothing is trusted and nothing can be added until `resetAll`.
    var revocationDamaged: Bool { get }
    /// What the store holds right now, as the helper can see it: the identity's public key and a
    /// digest of the paired records and the removed-devices list. The runtime compares this with
    /// what it saw after its own last write (`KeyWatch`).
    func snapshot() throws -> KeySnapshot
    /// Deletes the identity key, every paired record and the removed-devices list. The next
    /// `loadOrCreateIdentity` starts a fresh identity.
    func resetAll() throws
}

/// A point-in-time view of the key store, small enough to compare and to keep in memory
/// (public key and a hash, never a secret).
public struct KeySnapshot: Equatable, Sendable {
    /// The identity's public key, nil when the identity item is missing.
    public var identity: Data?
    /// SHA-256 over every paired item (account and payload, sorted) and the removed-devices item.
    public var trust: Data
    public init(identity: Data?, trust: Data) { self.identity = identity; self.trust = trust }
}

public final class MemoryKeyStore: KeyStore {
    private var identity: Curve25519.KeyAgreement.PrivateKey?
    private var paired: [PairedDevice] = []
    /// Tests: make the next writes fail.
    public var failWrites = false
    public var ignoredRecords = 0
    public var revocationDamaged = false
    /// Tests: what a program outside the app could do to the store.
    public var outsideIdentity: Curve25519.KeyAgreement.PrivateKey?
    public var outsideDeleteIdentity = false
    public var outsideTrustSalt = Data()
    public var failSnapshot = false
    public private(set) var snapshotCalls = 0
    public init(identity: Curve25519.KeyAgreement.PrivateKey? = nil, paired: [PairedDevice] = []) {
        self.identity = identity; self.paired = paired
    }
    public func loadOrCreateIdentity() throws -> Curve25519.KeyAgreement.PrivateKey {
        if let identity { return identity }
        let k = Curve25519.KeyAgreement.PrivateKey()
        identity = k
        return k
    }
    public func loadPaired() throws -> [PairedDevice] { paired }
    public func snapshot() throws -> KeySnapshot {
        snapshotCalls += 1
        if failSnapshot { throw KeyStoreError.failed("memory store set to fail") }
        let pub: Data?
        if outsideDeleteIdentity { pub = nil } else {
            pub = (outsideIdentity ?? identity)?.publicKey.rawRepresentation
        }
        var h = SHA256()
        for d in paired.sorted(by: { $0.key.hex < $1.key.hex }) { h.update(data: d.key); h.update(data: Data(d.name.utf8)) }
        h.update(data: outsideTrustSalt)
        return KeySnapshot(identity: pub, trust: Data(h.finalize()))
    }
    public func resetAll() throws {
        if failWrites { throw KeyStoreError.failed("memory store set to fail") }
        identity = nil; paired = []; revocationDamaged = false
        outsideIdentity = nil; outsideDeleteIdentity = false; outsideTrustSalt = Data()
    }
    public func addPaired(_ device: PairedDevice) throws {
        if failWrites { throw KeyStoreError.failed("memory store set to fail") }
        paired.removeAll { constantTimeEqual($0.key, device.key) }
        paired.append(device)
    }
    public func removePaired(key: Data) throws {
        if failWrites { throw KeyStoreError.failed("memory store set to fail") }
        paired.removeAll { constantTimeEqual($0.key, key) }
    }
}
