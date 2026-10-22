import CryptoKit
import Foundation
import HelperCore

/// The helper's support directory: the handshake timestamps and the pairing
/// failure ledger live here as 0600 files. They hold no secret and grant no
/// trust. The identity key and the paired list are NOT here: they are in the
/// Keychain (`KeychainKeyStore`), and nothing in this directory is ever read
/// as a key or as a paired device (W3 audit: a user-writable paired file
/// imported on launch gave a foreign key a valid record MAC).
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

    /// Test hook: runs after the directory was vetted and before the stale files are unlinked, so a
    /// test can swap the directory for a symlink right there (the gap the fd closes).
    var afterVettingForTests: (() -> Void)?

    /// Makes sure the support directory is a real directory owned by us and
    /// closed to everyone else. A directory that already exists is not trusted
    /// on its word: a symlink (planted to redirect the key somewhere readable),
    /// a directory owned by another user, or a regular file is refused, and
    /// loose permissions are tightened to 0700.
    func prepareDirectory() throws {
        let fd = try openVettedDirectory()
        close(fd)
    }

    /// Opens the directory with `O_NOFOLLOW | O_DIRECTORY` (a symlink in its place fails instead of
    /// being followed) and vets the OPEN descriptor with `fstat`/`fchmod`, so what is checked is what
    /// is used afterwards: a swap between the check and the use cannot redirect later work, because
    /// later work goes through this descriptor (`unlinkat`), not through the path again.
    /// The caller closes the descriptor.
    private func openVettedDirectory() throws -> Int32 {
        var st = stat()
        if lstat(dir.path, &st) != 0 {
            guard errno == ENOENT else { throw StoreError.unsafe("cannot inspect \(dir.path)") }
            try FileManager.default.createDirectory(
                at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        }
        let fd = open(dir.path, O_RDONLY | O_DIRECTORY | O_NOFOLLOW | O_CLOEXEC)
        guard fd >= 0 else {
            throw StoreError.unsafe(errno == ENOTDIR || errno == ELOOP ? "not a plain directory (symlink or file)" : "cannot open \(dir.path)")
        }
        do {
            guard fstat(fd, &st) == 0 else { throw StoreError.unsafe("cannot inspect \(dir.path)") }
            guard st.st_mode & S_IFMT == S_IFDIR else { throw StoreError.unsafe("not a plain directory (symlink or file)") }
            guard st.st_uid == ownerUID else { throw StoreError.unsafe("owned by another user") }
            if st.st_mode & 0o7777 != 0o700 {
                guard fchmod(fd, 0o700) == 0 else { throw StoreError.unsafe("cannot restrict the directory to 0700") }
            }
        } catch {
            close(fd)
            throw error
        }
        return fd
    }

    /// Deletes the files an earlier build kept here (`identity.key`, `paired.json`) WITHOUT reading
    /// them: neither becomes the identity, and neither adds a trusted device. `unlinkat` relative to
    /// the vetted directory descriptor removes a symlink itself and never follows it, and cannot be
    /// redirected by replacing the directory's path after the check.
    public func removeStaleLegacyFiles() throws {
        let fd = try openVettedDirectory()
        defer { close(fd) }
        afterVettingForTests?()
        for name in [keyURL.lastPathComponent, pairedURL.lastPathComponent] {
            if unlinkat(fd, name, 0) != 0 && errno != ENOENT { throw StoreError.unsafe("cannot remove \(name)") }
        }
    }

    /// The launch sequence for the keys: stale files are dropped unread, then the identity
    /// comes from the Keychain (created there on first run). Nothing from disk is imported.
    public func openIdentity(keys: KeyStore) throws -> Curve25519.KeyAgreement.PrivateKey {
        try removeStaleLegacyFiles()
        return try keys.loadOrCreateIdentity()
    }

    /// The timestamp store and the pairing failure ledger live next to the key.
    public var timestampsURL: URL { dir.appendingPathComponent("handshake-timestamps.json") }
    public var pairingFailuresURL: URL { dir.appendingPathComponent("pairing-failures.json") }
}
