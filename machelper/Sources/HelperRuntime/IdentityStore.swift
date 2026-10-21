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
    public init(directory: URL) { dir = directory }

    public static func defaultDirectory() -> URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("OSS Mouse Helper", isDirectory: true)
    }

    private var keyURL: URL { dir.appendingPathComponent("identity.key") }
    private var pairedURL: URL { dir.appendingPathComponent("paired.json") }

    /// Loads the key, creating a fresh one (and the 0700 directory) on first run.
    public func loadOrCreateKey() throws -> Curve25519.KeyAgreement.PrivateKey {
        try FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        if FileManager.default.fileExists(atPath: keyURL.path) {
            let raw = try Data(contentsOf: keyURL)
            return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: raw)
        }
        let key = Curve25519.KeyAgreement.PrivateKey()
        try AtomicFile.write(key.rawRepresentation, to: keyURL)
        return key
    }

    public struct Paired: Codable, Equatable {
        public var name: String
        public var key: String  // hex
    }

    /// The paired devices. A missing file is empty; an unreadable one throws
    /// (the app then runs with nobody paired rather than guessing).
    public func loadPaired() throws -> [Paired] {
        guard FileManager.default.fileExists(atPath: pairedURL.path) else { return [] }
        let list = try JSONDecoder().decode([Paired].self, from: Data(contentsOf: pairedURL))
        return list.filter { Data(hex: $0.key)?.count == 32 }
    }

    public func savePaired(_ list: [Paired]) throws {
        try AtomicFile.write(try JSONEncoder().encode(list), to: pairedURL)
    }

    /// The timestamp store and the pairing failure ledger live next to the key.
    public var timestampsURL: URL { dir.appendingPathComponent("handshake-timestamps.json") }
    public var pairingFailuresURL: URL { dir.appendingPathComponent("pairing-failures.json") }
}
