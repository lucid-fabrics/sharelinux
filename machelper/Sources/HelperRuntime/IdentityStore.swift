import CryptoKit
import Foundation
import HelperCore

/// The Mac's long-term identity and the paired list, as 0600 files in the
/// helper's support directory. W2 only reads them (an empty paired list means
/// nobody can connect yet); the pairing flow (W3) writes the list and may move
/// the private key to the Keychain.
///
/// The static private key is never logged and never leaves this file.
public final class IdentityStore {
    private let dir: URL
    private let ownerUID: uid_t
    /// - Parameter ownerUID: the only user that may own the directory and the
    ///   key (tests pass another uid to prove the refusal).
    public init(directory: URL, ownerUID: uid_t = geteuid()) { dir = directory; self.ownerUID = ownerUID }

    public enum StoreError: Error, Equatable {
        /// The support directory or the key is not a plain object of ours (symlink, other owner).
        case unsafe(String)
    }

    public static func defaultDirectory() -> URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("OSS Mouse Helper", isDirectory: true)
    }

    private var keyURL: URL { dir.appendingPathComponent("identity.key") }
    private var pairedURL: URL { dir.appendingPathComponent("paired.json") }

    /// Loads the key, creating a fresh one (and the 0700 directory) on first run.
    public func loadOrCreateKey() throws -> Curve25519.KeyAgreement.PrivateKey {
        try prepareDirectory()
        if FileManager.default.fileExists(atPath: keyURL.path) {
            try checkPrivateFile(keyURL)
            let raw = try Data(contentsOf: keyURL)
            return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw)
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        try AtomicFile.write(key.rawRepresentation, to: keyURL)
        return key
    }

    /// Makes sure the support directory is a real directory owned by us and
    /// closed to everyone else. A directory that already exists is not trusted
    /// on its word: a symlink (planted to redirect the key somewhere readable),
    /// a directory owned by another user, or a regular file is refused, and
    /// loose permissions are tightened to 0700.
    func prepareDirectory() throws {
        var st = stat()
        if lstat(dir.path, &st) != 0 {
            guard errno == ENOENT else { throw StoreError.unsafe("cannot inspect \(dir.path)") }
            try FileManager.default.createDirectory(
                at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            guard lstat(dir.path, &st) == 0 else { throw StoreError.unsafe("cannot inspect \(dir.path)") }
        }
        guard st.st_mode & S_IFMT == S_IFDIR else { throw StoreError.unsafe("not a plain directory (symlink or file)") }
        guard st.st_uid == ownerUID else { throw StoreError.unsafe("owned by another user") }
        if st.st_mode & 0o7777 != 0o700 {
            guard chmod(dir.path, 0o700) == 0 else { throw StoreError.unsafe("cannot restrict the directory to 0700") }
        }
    }

    /// The key must be a regular file of ours, not a symlink; loose modes are tightened to 0600.
    private func checkPrivateFile(_ url: URL) throws {
        var st = stat()
        guard lstat(url.path, &st) == 0, st.st_mode & S_IFMT == S_IFREG else {
            throw StoreError.unsafe("\(url.lastPathComponent) is not a regular file")
        }
        guard st.st_uid == ownerUID else { throw StoreError.unsafe("\(url.lastPathComponent) is owned by another user") }
        if st.st_mode & 0o7777 != 0o600 { _ = chmod(url.path, 0o600) }
    }

    public struct Paired: Codable, Equatable {
        public var name: String
        public var key: String  // hex
    }

    /// The paired devices. A missing file is empty; an unreadable one throws
    /// (the app then runs with nobody paired rather than guessing).
    public func loadPaired() throws -> [Paired] {
        try prepareDirectory()
        guard FileManager.default.fileExists(atPath: pairedURL.path) else { return [] }
        let list = try JSONDecoder().decode([Paired].self, from: Data(contentsOf: pairedURL))
        return list.filter { Data(hex: $0.key)?.count == 32 }
    }

    public func savePaired(_ list: [Paired]) throws {
        try prepareDirectory()
        try AtomicFile.write(try JSONEncoder().encode(list), to: pairedURL)
    }

    /// The key of the W2 file layout, or nil when there is none. Used once, to
    /// move it into the Keychain (`KeychainKeyStore.importLegacyFiles`).
    func legacyKey() throws -> Curve25519.KeyAgreement.PrivateKey? {
        try prepareDirectory()
        guard FileManager.default.fileExists(atPath: keyURL.path) else { return nil }
        try checkPrivateFile(keyURL)
        return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: Data(contentsOf: keyURL))
    }

    /// Overwrites and deletes the old key file once the Keychain holds the key.
    func removeLegacyKey() {
        if let h = FileHandle(forWritingAtPath: keyURL.path) {
            try? h.write(contentsOf: Data(count: 32))
            try? h.synchronize()
            try? h.close()
        }
        try? FileManager.default.removeItem(at: keyURL)
    }

    func removeLegacyPaired() { try? FileManager.default.removeItem(at: pairedURL) }

    /// The timestamp store and the pairing failure ledger live next to the key.
    public var timestampsURL: URL { dir.appendingPathComponent("handshake-timestamps.json") }
    public var pairingFailuresURL: URL { dir.appendingPathComponent("pairing-failures.json") }
}
