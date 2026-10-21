import CryptoKit
import Foundation

public enum SessionError: Error, Equatable {
    case malformed
    case replayOrOld
    case authenticationFailed
    /// Session hit its age or message limit; a new handshake is required.
    case expired
}

/// Noise transport with explicit 64-bit counters (WireGuard style) so datagrams
/// can be lost or reordered. One instance per handshake.
public final class TransportSession {
    private let keys: TransportKeys
    private let startedAt: Double
    private var sendCounter: UInt64 = 0
    private var window = ReplayWindow()

    public init(keys: TransportKeys, now: Double) {
        self.keys = keys
        self.startedAt = now
    }

    public func expired(now: Double) -> Bool {
        now - startedAt >= ChannelLimits.maxSessionAge || now < startedAt
    }

    public func seal(_ plaintext: Data, now: Double) throws -> Data {
        guard !expired(now: now), sendCounter < ChannelLimits.maxMessages else { throw SessionError.expired }
        guard plaintext.count + WireLimits.transportOverhead <= WireLimits.maxDatagram else {
            throw SessionError.malformed
        }
        let n = sendCounter
        sendCounter += 1
        var out = Data([DatagramKind.transport.rawValue])
        out.appendLE64(n)
        out += try NoiseAEAD.seal(key: keys.send, counter: n, ad: Data(), plaintext: plaintext)
        return out
    }

    /// Authenticates and decrypts one datagram. The replay window is only
    /// advanced after the tag verified.
    public func open(_ datagram: Data, maxPlaintext: Int, now: Double) throws -> Data {
        let d = Data(datagram)
        guard d.count >= WireLimits.transportOverhead,
            d.count <= WireLimits.transportOverhead + maxPlaintext,
            d[0] == DatagramKind.transport.rawValue
        else { throw SessionError.malformed }
        guard !expired(now: now) else { throw SessionError.expired }
        let counter = d.le64(at: 1)
        guard counter < ChannelLimits.maxMessages else { throw SessionError.expired }
        guard window.check(counter) else { throw SessionError.replayOrOld }
        let plaintext: Data
        do {
            plaintext = try NoiseAEAD.open(
                key: keys.receive, counter: counter, ad: Data(), sealed: Data(d.dropFirst(9)))
        } catch {
            throw SessionError.authenticationFailed
        }
        guard window.commit(counter) else { throw SessionError.replayOrOld }
        return plaintext
    }
}

/// TAI64N timestamp (12 bytes, big endian) carried in the IK initiation so a
/// replayed first message is refused. Byte-wise comparison orders time.
public enum TAI64N {
    public static let size = 12

    public static func encode(_ date: Date) -> Data {
        let t = date.timeIntervalSince1970
        let secs = UInt64(max(0, t.rounded(.down)))
        let nanos = UInt32(max(0, min(999_999_999, (t - t.rounded(.down)) * 1e9)))
        var d = Data()
        let tai = (UInt64(1) << 62) + secs
        for k in (0..<8).reversed() { d.append(UInt8((tai >> (8 * UInt64(k))) & 0xff)) }
        for k in (0..<4).reversed() { d.append(UInt8((nanos >> (8 * UInt32(k))) & 0xff)) }
        return d
    }

    /// Unix seconds (fractional) or nil if malformed.
    public static func decode(_ d: Data) -> Double? {
        guard d.count == size else { return nil }
        let b = Data(d)
        var tai: UInt64 = 0
        for k in 0..<8 { tai = tai << 8 | UInt64(b[k]) }
        var nanos: UInt32 = 0
        for k in 8..<12 { nanos = nanos << 8 | UInt32(b[k]) }
        guard tai >= (UInt64(1) << 62), tai < (UInt64(1) << 62) + (UInt64(1) << 40), nanos < 1_000_000_000 else {
            return nil
        }
        return Double(tai - (UInt64(1) << 62)) + Double(nanos) / 1e9
    }

    /// Strictly newer, comparing the raw big-endian bytes.
    public static func isNewer(_ a: Data, than b: Data) -> Bool {
        a.lexicographicallyPrecedes(b) == false && !constantTimeEqual(a, b)
    }
}

/// Short authentication string for pairing: six decimal digits derived from the
/// XX handshake hash (identical on both sides, differs under a MITM).
public enum SAS {
    public static func derive(handshakeHash: Data) -> String {
        let mac = HMAC<SHA256>.authenticationCode(
            for: handshakeHash, using: SymmetricKey(data: Data("OSS-mousehelper-SAS-v1".utf8)))
        let b = Array(mac.prefix(4))
        let v = b.reduce(UInt32(0)) { $0 << 8 | UInt32($1) }
        return String(format: "%06d", v % 1_000_000)
    }
}
