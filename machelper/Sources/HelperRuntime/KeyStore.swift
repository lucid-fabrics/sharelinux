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
}

public final class MemoryKeyStore: KeyStore {
    private var identity: Curve25519.KeyAgreement.PrivateKey?
    private var paired: [PairedDevice] = []
    /// Tests: make the next writes fail.
    public var failWrites = false
    public var ignoredRecords = 0
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
