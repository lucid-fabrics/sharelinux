import CryptoKit
import Foundation

/// WireGuard-style cheap filter for IK initiations. The last 16 bytes of an
/// initiation are a keyed MAC over everything before them, under a key derived
/// from the Mac's static public key. Whoever does not know that key cannot make
/// a datagram that gets past this check, which costs one HMAC instead of two
/// Curve25519 operations, so junk never reaches the rate limiter or the
/// handshake. The key is not secret from paired devices or anyone who watched
/// pairing; that is why per-source limits exist as well.
public enum Mac1 {
    public static let size = 16
    private static let label = Data("OSS-mousehelper v2 mac1".utf8)

    public static func key(macPublic: Data) -> SymmetricKey {
        SymmetricKey(data: Data(SHA256.hash(data: label + macPublic)))
    }

    /// `message` is the datagram without the mac1 (kind byte included).
    public static func tag(over message: Data, key: SymmetricKey) -> Data {
        Data(HMAC<SHA256>.authenticationCode(for: message, using: key).prefix(size))
    }

    public static func append(to message: Data, macPublic: Data) -> Data {
        message + tag(over: message, key: key(macPublic: macPublic))
    }

    /// Constant-time check of a full initiation datagram (with mac1 at the end).
    public static func verify(_ datagram: Data, key: SymmetricKey) -> Bool {
        let d = Data(datagram)
        guard d.count > size else { return false }
        let split = d.count - size
        return constantTimeEqual(tag(over: Data(d[0..<split]), key: key), Data(d[split...]))
    }
}
