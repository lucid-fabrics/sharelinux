import CryptoKit
import Foundation
import HelperCore
import Security

/// The helper's static private key and the pinned Linux public keys as generic
/// password items in the login Keychain, not files.
///
///  * Not synchronised: `kSecAttrSynchronizable` is false, so nothing goes to iCloud Keychain.
///  * Access limited to this app: every item is created with an access object
///    whose trusted-application list holds only the calling application, so
///    another program asking for the key gets a prompt (or a refusal), and the
///    private key is never readable from a plain file.
///  * Integrity of the paired list: adding a trusted key is what authorises a
///    remote device to move the mouse, so the list lives where other programs
///    cannot quietly edit it. An item whose account (the hex key) and payload
///    disagree is ignored.
///
/// `keychain` is nil for the default (login) keychain; tests pass a throwaway
/// keychain file so nothing touches the owner's.
public final class KeychainKeyStore: KeyStore {
    public static let identityService = "io.github.sharelinux.mousehelper.identity"
    public static let pairedService = "io.github.sharelinux.mousehelper.paired"
    private static let identityAccount = "static-key"
    private let keychain: SecKeychain?

    public init(keychain: SecKeychain? = nil) { self.keychain = keychain }

    // MARK: query plumbing

    private func base(service: String) -> [String: Any] {
        var q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrSynchronizable as String: false,
        ]
        if let keychain { q[kSecMatchSearchList as String] = [keychain] }
        return q
    }

    /// An access object trusting only the application that makes this call.
    private func ownAccess(label: String) throws -> SecAccess {
        var me: SecTrustedApplication?
        guard SecTrustedApplicationCreateFromPath(nil, &me) == errSecSuccess, let me else {
            throw KeyStoreError.failed("cannot name this application for the access list")
        }
        var access: SecAccess?
        guard SecAccessCreate(label as CFString, [me] as CFArray, &access) == errSecSuccess, let access else {
            throw KeyStoreError.failed("cannot create the access list")
        }
        return access
    }

    private func add(service: String, account: String, label: String, data: Data) throws {
        var q = base(service: service)
        q[kSecAttrAccount as String] = account
        q[kSecAttrLabel as String] = label
        q[kSecValueData as String] = data
        q[kSecAttrAccess as String] = try ownAccess(label: label)
        if let keychain { q[kSecUseKeychain as String] = keychain }
        q[kSecMatchSearchList as String] = nil  // not valid for SecItemAdd
        let status = SecItemAdd(q as CFDictionary, nil)
        guard status == errSecSuccess else { throw KeyStoreError.failed("Keychain add failed (\(status))") }
    }

    private func read(service: String, account: String) throws -> Data? {
        var q = base(service: service)
        q[kSecAttrAccount as String] = account
        q[kSecReturnData as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitOne
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return nil }
        guard status == errSecSuccess, let data = out as? Data else {
            throw KeyStoreError.failed("Keychain read failed (\(status))")
        }
        return data
    }

    private func delete(service: String, account: String) throws {
        var q = base(service: service)
        q[kSecAttrAccount as String] = account
        let status = SecItemDelete(q as CFDictionary)
        guard status == errSecSuccess || status == errSecItemNotFound else {
            throw KeyStoreError.failed("Keychain delete failed (\(status))")
        }
    }

    // MARK: KeyStore

    public func loadOrCreateIdentity() throws -> Curve25519.KeyAgreement.PrivateKey {
        if var raw = try read(service: Self.identityService, account: Self.identityAccount) {
            defer { raw.wipe() }
            guard raw.count == 32, let key = try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw) else {
                throw KeyStoreError.corrupt("the stored identity key is not a Curve25519 key")
            }
            return key
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        // A real copy: wiping Data that shares the key's own buffer would zero the key itself.
        var raw = Data(Array(key.rawRepresentation))
        defer { raw.wipe() }
        try add(
            service: Self.identityService, account: Self.identityAccount, label: "OSS Mouse Helper identity key",
            data: raw)
        return key
    }

    private struct Record: Codable {
        var name: String
        var key: String
    }

    public func loadPaired() throws -> [PairedDevice] {
        var q = base(service: Self.pairedService)
        // File keychains do not return secret data for a multi-item query: list
        // the accounts first, then read each payload.
        q[kSecReturnAttributes as String] = true
        q[kSecMatchLimit as String] = kSecMatchLimitAll
        var out: CFTypeRef?
        let status = SecItemCopyMatching(q as CFDictionary, &out)
        if status == errSecItemNotFound { return [] }
        guard status == errSecSuccess, let items = out as? [[String: Any]] else {
            throw KeyStoreError.failed("Keychain list failed (\(status))")
        }
        var list: [PairedDevice] = []
        for item in items {
            guard let account = item[kSecAttrAccount as String] as? String,
                let data = try read(service: Self.pairedService, account: account),
                let rec = try? JSONDecoder().decode(Record.self, from: data),
                let key = Data(hex: rec.key), key.count == 32, rec.key == account,
                AuthorizationName.isPlain(rec.name)
            else { continue }  // not ours, or tampered: ignored
            list.append(PairedDevice(name: rec.name, key: key))
        }
        return list.sorted { $0.name < $1.name }
    }

    public func addPaired(_ device: PairedDevice) throws {
        guard device.key.count == 32 else { throw KeyStoreError.corrupt("a peer key is 32 bytes") }
        let account = device.key.hex
        let payload = try JSONEncoder().encode(Record(name: device.name, key: account))
        try delete(service: Self.pairedService, account: account)  // re-pairing replaces the name
        try add(service: Self.pairedService, account: account, label: "OSS Mouse Helper: " + device.name, data: payload)
    }

    public func removePaired(key: Data) throws {
        try delete(service: Self.pairedService, account: key.hex)
    }

    /// Moves the W2 file identity and paired list (0600 files) into this store
    /// and deletes the files. Nothing happens when there are none. A key already
    /// in the Keychain wins; the old file is still removed so no secret stays on disk.
    public func importLegacyFiles(from legacy: IdentityStore) throws {
        if let old = try legacy.legacyKey() {
            if try read(service: Self.identityService, account: Self.identityAccount) == nil {
                var raw = Data(Array(old.rawRepresentation))
                defer { raw.wipe() }
                try add(
                    service: Self.identityService, account: Self.identityAccount,
                    label: "OSS Mouse Helper identity key", data: raw)
            }
            legacy.removeLegacyKey()
        }
        for d in (try? legacy.loadPaired()) ?? [] {
            if let key = Data(hex: d.key), key.count == 32 {
                try addPaired(PairedDevice(name: d.name, key: key))
            }
        }
        legacy.removeLegacyPaired()
    }
}

enum AuthorizationName {
    /// Names read back from the Keychain go through the same rules as names off the wire.
    static func isPlain(_ s: String) -> Bool {
        PairingResponder.validateDeviceName(Data(s.utf8)) == s
    }
}
