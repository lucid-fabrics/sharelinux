import CryptoKit
import Foundation
import HelperCore
import Security
import os

/// The helper's static private key and the pinned Linux public keys as generic
/// password items in the login Keychain, not files.
///
///  * Not synchronised: `kSecAttrSynchronizable` is false, so nothing goes to iCloud Keychain.
///  * Secret access limited to this app: every item is created with an access
///    object whose trusted-application list holds only the calling application,
///    so another program asking for the identity key gets a prompt (or a
///    refusal), and the private key is never readable from a plain file.
///  * What the Keychain does NOT do: it does not stop another program of the
///    same user from adding a generic-password item under our service name, or
///    from listing the attributes of ours. Adding a trusted key is what lets a
///    remote device move the mouse, so the access list is not enough.
///  * Authenticity of the paired list: each paired record carries an HMAC-SHA256
///    over (device key, name, created-at). The HMAC key is derived (HKDF) from
///    the helper's identity private key, which only this app can read without
///    a prompt. A record without a valid HMAC (written by anything else, or
///    altered) is never trusted; it is counted in `ignoredRecords` and logged by
///    account prefix. Seeing the paired items, even reading all of them, gives
///    no way to forge one. The residual risk (design 3.7): a program that
///    plants the identity item before the first launch, or that the owner lets
///    read it ("Always Allow"), holds the HMAC key.
///
///  * Revocation: removing a device writes the removed record's MAC into an
///    authenticated revocation set (HMAC'd like a record) BEFORE the record is
///    deleted, and every record carries the set's size at the time it was
///    written (`gen`). A record whose MAC is in the set is never trusted, so
///    putting the exact bytes of a removed record back grants nothing. A record
///    newer than the set (`gen` above the set's count) means the set was
///    deleted or rolled back, so it is not trusted either. A set that fails
///    its own MAC is treated as unknown and nothing is trusted. Re-pairing a
///    removed device writes a fresh record (new nonce), which is not revoked.
///
/// `keychain` is nil for the default (login) keychain; tests pass a throwaway
/// keychain file so nothing touches the owner's.
public final class KeychainKeyStore: KeyStore {
    public static let identityService = "io.github.sharelinux.mousehelper.identity"
    public static let pairedService = "io.github.sharelinux.mousehelper.paired"
    /// One authenticated item holding the MACs of removed records (see "Revocation" below).
    public static let revokedService = "io.github.sharelinux.mousehelper.revoked"
    private static let revokedAccount = "set"
    private static let identityAccount = "static-key"
    private let keychain: SecKeychain?
    private let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "keystore")
    /// Derived from the identity key; held in memory once the identity is loaded.
    private var macKeyCache: SymmetricKey?
    public private(set) var ignoredRecords = 0
    /// Accounts (hex device keys, or whatever a foreign item used) skipped by the last `loadPaired`.
    public private(set) var ignoredAccounts: [String] = []

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
            macKeyCache = Self.macKey(from: raw)
            return key
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        // A real copy: wiping Data that shares the key's own buffer would zero the key itself.
        var raw = Data(Array(key.rawRepresentation))
        defer { raw.wipe() }
        try add(
            service: Self.identityService, account: Self.identityAccount, label: "OSS Mouse Helper identity key",
            data: raw)
        macKeyCache = Self.macKey(from: raw)
        return key
    }

    // MARK: authenticated paired records

    private struct Record: Codable {
        var v: Int
        var name: String
        var key: String
        /// Seconds since 1970 when the device was paired.
        var created: Int64
        /// Random per record, so a re-pairing in the same second never repeats a revoked MAC.
        var nonce: String
        /// The size of the revocation set when this record was written.
        var gen: Int
        /// HMAC-SHA256 (hex) over `message(key:name:created:nonce:gen:)`.
        var mac: String
    }

    private struct RevokedSet: Codable {
        var v: Int
        /// How many removals have happened.
        var count: Int
        /// MACs (hex) of removed records.
        var macs: [String]
        var mac: String
    }

    static func macKey(from identityRaw: Data) -> SymmetricKey {
        HKDF<SHA256>.deriveKey(
            inputKeyMaterial: SymmetricKey(data: identityRaw), salt: Data("oss-mouse-helper".utf8),
            info: Data("paired-record-mac-v1".utf8), outputByteCount: 32)
    }

    private static func be(_ v: some FixedWidthInteger) -> [UInt8] {
        withUnsafeBytes(of: v.bigEndian) { Array($0) }
    }

    /// Length-prefixed so no two different records share a message.
    static func message(key: Data, name: String, created: Int64, nonce: Data, gen: Int) -> Data {
        var m = Data("oss-paired-record-v3".utf8)
        m.append(0)
        m.append(key)
        let n = Data(name.utf8)
        m.append(contentsOf: be(UInt16(n.count)))
        m.append(n)
        m.append(contentsOf: be(created))
        m.append(nonce)  // 16 bytes
        m.append(contentsOf: be(Int64(gen)))
        return m
    }

    static func revokedMessage(count: Int, macs: [Data]) -> Data {
        var m = Data("oss-revoked-set-v1".utf8)
        m.append(0)
        m.append(contentsOf: be(Int64(count)))
        m.append(contentsOf: be(UInt32(macs.count)))
        for x in macs { m.append(x) }
        return m
    }

    /// The MAC key for reading, or nil when this keychain has no identity yet (then
    /// nothing in it can be authentic).
    private func macKeyForReading() throws -> SymmetricKey? {
        if let macKeyCache { return macKeyCache }
        guard var raw = try read(service: Self.identityService, account: Self.identityAccount) else { return nil }
        defer { raw.wipe() }
        let k = Self.macKey(from: raw)
        macKeyCache = k
        return k
    }

    /// The revocation set: nil when the item is absent (nothing revoked, count 0), `.damaged` when
    /// something that is not authentic sits there.
    private enum Revoked {
        case set(count: Int, macs: Set<Data>)
        case damaged
    }

    private func loadRevoked(macKey: SymmetricKey) throws -> Revoked {
        guard let data = try read(service: Self.revokedService, account: Self.revokedAccount) else {
            return .set(count: 0, macs: [])
        }
        guard let r = try? JSONDecoder().decode(RevokedSet.self, from: data), r.v == 1, r.count >= r.macs.count,
            let mac = Data(hex: r.mac)
        else { return .damaged }
        let macs = r.macs.compactMap { Data(hex: $0) }
        guard macs.count == r.macs.count, macs.allSatisfy({ $0.count == 32 }),
            HMAC<SHA256>.isValidAuthenticationCode(
                mac, authenticating: Self.revokedMessage(count: r.count, macs: macs), using: macKey)
        else { return .damaged }
        return .set(count: r.count, macs: Set(macs))
    }

    private func storeRevoked(count: Int, macs: [Data], macKey: SymmetricKey) throws {
        let tag = Data(
            HMAC<SHA256>.authenticationCode(for: Self.revokedMessage(count: count, macs: macs), using: macKey))
        let payload = try JSONEncoder().encode(
            RevokedSet(v: 1, count: count, macs: macs.map(\.hex), mac: tag.hex))
        try delete(service: Self.revokedService, account: Self.revokedAccount)
        try add(service: Self.revokedService, account: Self.revokedAccount, label: "OSS Mouse Helper removed devices", data: payload)
    }

    private func authentic(_ data: Data, account: String, macKey: SymmetricKey, revoked: Revoked) -> PairedDevice? {
        guard case .set(let count, let revokedMacs) = revoked else { return nil }
        guard let rec = try? JSONDecoder().decode(Record.self, from: data), rec.v == 3,
            let key = Data(hex: rec.key), key.count == 32, rec.key == account,
            let nonce = Data(hex: rec.nonce), nonce.count == 16, rec.gen >= 0,
            AuthorizationName.isPlain(rec.name), let mac = Data(hex: rec.mac)
        else { return nil }
        let msg = Self.message(key: key, name: rec.name, created: rec.created, nonce: nonce, gen: rec.gen)
        guard HMAC<SHA256>.isValidAuthenticationCode(mac, authenticating: msg, using: macKey) else { return nil }
        // Removed once: its bytes are no longer welcome. Newer than the set: the set was deleted or rolled back.
        guard !revokedMacs.contains(mac), rec.gen <= count else { return nil }
        return PairedDevice(name: rec.name, key: key)
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
        let macKey = try macKeyForReading()
        let revoked: Revoked = try macKey.map { try loadRevoked(macKey: $0) } ?? .damaged
        var list: [PairedDevice] = []
        var ignored: [String] = []
        for item in items {
            let account = (item[kSecAttrAccount as String] as? String) ?? "?"
            if let macKey, let data = try read(service: Self.pairedService, account: account),
                let device = authentic(data, account: account, macKey: macKey, revoked: revoked)
            {
                list.append(device)
            } else {
                ignored.append(account)  // not written by this app, or altered: never trusted
            }
        }
        ignoredAccounts = ignored
        ignoredRecords = ignored.count
        for a in ignored { log.error("ignored an unrecognised paired item \(String(a.prefix(8)), privacy: .public)") }
        return list.sorted { $0.name < $1.name }
    }

    public func addPaired(_ device: PairedDevice) throws {
        guard device.key.count == 32 else { throw KeyStoreError.corrupt("a peer key is 32 bytes") }
        let account = device.key.hex
        _ = try loadOrCreateIdentity()  // makes sure the MAC key is there
        guard let macKey = macKeyCache else { throw KeyStoreError.failed("no identity key to authenticate the record") }
        guard case .set(let count, _) = try loadRevoked(macKey: macKey) else {
            throw KeyStoreError.corrupt("the list of removed devices is damaged")
        }
        let created = Int64(Date().timeIntervalSince1970)
        var nonce = Data(count: 16)
        guard nonce.withUnsafeMutableBytes({ SecRandomCopyBytes(kSecRandomDefault, 16, $0.baseAddress!) }) == errSecSuccess else {
            throw KeyStoreError.failed("no random bytes for the record")
        }
        let mac = Data(
            HMAC<SHA256>.authenticationCode(
                for: Self.message(key: device.key, name: device.name, created: created, nonce: nonce, gen: count),
                using: macKey))
        let payload = try JSONEncoder().encode(
            Record(v: 3, name: device.name, key: account, created: created, nonce: nonce.hex, gen: count, mac: mac.hex))
        try delete(service: Self.pairedService, account: account)  // re-pairing replaces the name
        try add(service: Self.pairedService, account: account, label: "OSS Mouse Helper: " + device.name, data: payload)
    }

    /// Revokes the record's MAC first, then deletes the record. If the revocation cannot be written
    /// the record stays (and stays trusted), and the caller is told.
    public func removePaired(key: Data) throws {
        let account = key.hex
        if let macKey = try macKeyForReading(), let data = try read(service: Self.pairedService, account: account),
            let rec = try? JSONDecoder().decode(Record.self, from: data), let mac = Data(hex: rec.mac), mac.count == 32
        {
            guard case .set(let count, let macs) = try loadRevoked(macKey: macKey) else {
                throw KeyStoreError.corrupt("the list of removed devices is damaged")
            }
            if !macs.contains(mac) {
                try storeRevoked(count: count + 1, macs: macs.sorted { $0.hex < $1.hex } + [mac], macKey: macKey)
            }
        }
        try delete(service: Self.pairedService, account: account)
    }
}

enum AuthorizationName {
    /// Names read back from the Keychain go through the same rules as names off the wire.
    static func isPlain(_ s: String) -> Bool {
        PairingResponder.validateDeviceName(Data(s.utf8)) == s
    }
}
