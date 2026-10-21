import CryptoKit
import Foundation

/// Noise Protocol Framework (revision 34) on CryptoKit primitives, restricted to the
/// two patterns this protocol uses: XX (pairing) and IK (sessions), both
/// 25519 / ChaChaPoly / SHA256. Nothing here is custom crypto: it is the spec's
/// state machines composed from Curve25519, ChaCha20-Poly1305, SHA-256 and HMAC.

public enum NoisePattern: Sendable {
    case xx
    case ik

    public var protocolName: String {
        switch self {
        case .xx: return "Noise_XX_25519_ChaChaPoly_SHA256"
        case .ik: return "Noise_IK_25519_ChaChaPoly_SHA256"
        }
    }

    fileprivate var messages: [[Token]] {
        switch self {
        case .xx: return [[.e], [.e, .ee, .s, .es], [.s, .se]]
        case .ik: return [[.e, .es, .s, .ss], [.e, .ee, .se]]
        }
    }
}

fileprivate enum Token { case e, s, ee, es, se, ss }

public enum NoiseError: Error, Equatable {
    /// Handshake already finished or already failed, or called out of turn.
    case invalidState
    case messageTooShort
    case messageTooLong
    case trailingData
    case authenticationFailed
    case invalidKey
    /// The DH result was all zeros (low-order public key).
    case lowOrderPoint
    case missingRemoteStatic
}

let noiseMaxMessage = 65535
let aeadTagLength = 16
let dhLength = 32

/// ChaCha20-Poly1305 with the Noise nonce layout: 32 zero bits then the 64-bit
/// counter, little endian.
enum NoiseAEAD {
    static func nonce(_ n: UInt64) -> ChaChaPoly.Nonce {
        var d = Data(count: 4)
        d.appendLE64(n)
        // 12 bytes always; cannot fail.
        return try! ChaChaPoly.Nonce(data: d)
    }

    static func seal(key: SymmetricKey, counter: UInt64, ad: Data, plaintext: Data) throws -> Data {
        let box = try ChaChaPoly.seal(plaintext, using: key, nonce: nonce(counter), authenticating: ad)
        return box.ciphertext + box.tag
    }

    static func open(key: SymmetricKey, counter: UInt64, ad: Data, sealed: Data) throws -> Data {
        guard sealed.count >= aeadTagLength else { throw NoiseError.messageTooShort }
        let split = sealed.endIndex - aeadTagLength
        do {
            let box = try ChaChaPoly.SealedBox(
                nonce: nonce(counter),
                ciphertext: sealed[sealed.startIndex..<split],
                tag: sealed[split..<sealed.endIndex])
            return try ChaChaPoly.open(box, using: key, authenticating: ad)
        } catch {
            throw NoiseError.authenticationFailed
        }
    }
}

private func hmac(_ key: Data, _ data: Data) -> Data {
    Data(HMAC<SHA256>.authenticationCode(for: data, using: SymmetricKey(data: key)))
}

/// Noise HKDF returning two outputs.
func noiseHKDF2(chainingKey: Data, input: Data) -> (Data, Data) {
    let temp = hmac(chainingKey, input)
    let o1 = hmac(temp, Data([1]))
    let o2 = hmac(temp, o1 + Data([2]))
    return (o1, o2)
}

private struct CipherState {
    var key: SymmetricKey?
    var n: UInt64 = 0

    mutating func encrypt(ad: Data, _ pt: Data) throws -> Data {
        guard let key else { return pt }
        guard n < UInt64.max else { throw NoiseError.invalidState }
        defer { n += 1 }
        return try NoiseAEAD.seal(key: key, counter: n, ad: ad, plaintext: pt)
    }

    mutating func decrypt(ad: Data, _ ct: Data) throws -> Data {
        guard let key else { return ct }
        guard n < UInt64.max else { throw NoiseError.invalidState }
        let pt = try NoiseAEAD.open(key: key, counter: n, ad: ad, sealed: ct)
        n += 1
        return pt
    }
}

/// Keys for the two transport directions, as produced by Split().
public struct TransportKeys {
    public let send: SymmetricKey
    public let receive: SymmetricKey
}

private struct SymmetricState {
    var ck: Data
    var h: Data
    var cs = CipherState()

    init(protocolName: String) {
        let name = Data(protocolName.utf8)
        if name.count <= 32 {
            h = name + Data(count: 32 - name.count)
        } else {
            h = Data(SHA256.hash(data: name))
        }
        ck = h
    }

    mutating func mixHash(_ d: Data) { h = Data(SHA256.hash(data: h + d)) }

    mutating func mixKey(_ ikm: Data) {
        let (c, k) = noiseHKDF2(chainingKey: ck, input: ikm)
        ck = c
        cs = CipherState(key: SymmetricKey(data: k), n: 0)
    }

    mutating func encryptAndHash(_ pt: Data) throws -> Data {
        let ct = try cs.encrypt(ad: h, pt)
        mixHash(ct)
        return ct
    }

    mutating func decryptAndHash(_ ct: Data) throws -> Data {
        let pt = try cs.decrypt(ad: h, ct)
        mixHash(ct)
        return pt
    }

    var hasKey: Bool { cs.key != nil }

    func split() -> (Data, Data) { noiseHKDF2(chainingKey: ck, input: Data()) }
}

public final class HandshakeState {
    public typealias PrivateKey = Curve25519.KeyAgreement.PrivateKey
    public typealias PublicKey = Curve25519.KeyAgreement.PublicKey

    private let pattern: NoisePattern
    private let initiator: Bool
    private let messages: [[Token]]
    private var ss: SymmetricState
    private let s: PrivateKey
    private var e: PrivateKey?
    private var rs: PublicKey?
    private var re: PublicKey?
    private var index = 0
    private var failed = false
    private let makeEphemeral: () -> PrivateKey

    /// - Parameters:
    ///   - remoteStatic: required for the IK initiator (pre-message), forbidden otherwise.
    ///   - ephemeralSource: tests only; production always uses a fresh random key.
    init(
        pattern: NoisePattern, initiator: Bool, staticKey: PrivateKey,
        remoteStatic: Data? = nil, prologue: Data = Data(),
        ephemeralSource: @escaping () -> PrivateKey = { PrivateKey() }
    ) throws {
        self.pattern = pattern
        self.initiator = initiator
        self.messages = pattern.messages
        self.s = staticKey
        self.makeEphemeral = ephemeralSource
        ss = SymmetricState(protocolName: pattern.protocolName)
        ss.mixHash(prologue)
        switch pattern {
        case .xx:
            guard remoteStatic == nil else { throw NoiseError.invalidState }
        case .ik:
            if initiator {
                guard let remoteStatic else { throw NoiseError.missingRemoteStatic }
                guard let k = try? PublicKey(rawRepresentation: remoteStatic) else {
                    throw NoiseError.invalidKey
                }
                rs = k
                ss.mixHash(remoteStatic)
            } else {
                guard remoteStatic == nil else { throw NoiseError.invalidState }
                // Responder's own static is the pre-message.
                ss.mixHash(staticKey.publicKey.rawRepresentation)
            }
        }
    }

    public convenience init(
        pattern: NoisePattern, initiator: Bool, staticKey: PrivateKey,
        remoteStatic: Data? = nil, prologue: Data = Data()
    ) throws {
        try self.init(
            pattern: pattern, initiator: initiator, staticKey: staticKey,
            remoteStatic: remoteStatic, prologue: prologue, ephemeralSource: { PrivateKey() })
    }

    public var isComplete: Bool { !failed && index == messages.count }
    public var handshakeHash: Data { ss.h }
    /// The peer's static public key once it has been received (or pinned, for IK initiator).
    public var remoteStatic: Data? { rs?.rawRepresentation }

    private func dh(_ priv: PrivateKey?, _ pub: PublicKey?) throws -> Data {
        guard let priv, let pub else { throw NoiseError.invalidState }
        let secret: Data
        do {
            secret = try priv.sharedSecretFromKeyAgreement(with: pub).withUnsafeBytes { Data($0) }
        } catch {
            throw NoiseError.lowOrderPoint
        }
        if secret.count != dhLength || isAllZero(secret) { throw NoiseError.lowOrderPoint }
        return secret
    }

    private func myTurn() -> Bool { (index % 2 == 0) == initiator }

    private func dhToken(_ t: Token) throws -> Data {
        switch t {
        case .ee: return try dh(e, re)
        case .ss: return try dh(s, rs)
        case .es: return initiator ? try dh(e, rs) : try dh(s, re)
        case .se: return initiator ? try dh(s, re) : try dh(e, rs)
        default: throw NoiseError.invalidState
        }
    }

    public func writeMessage(payload: Data = Data()) throws -> Data {
        guard !failed, index < messages.count, myTurn() else { throw NoiseError.invalidState }
        do {
            var out = Data()
            for t in messages[index] {
                switch t {
                case .e:
                    let k = makeEphemeral()
                    e = k
                    let pub = k.publicKey.rawRepresentation
                    out += pub
                    ss.mixHash(pub)
                case .s:
                    out += try ss.encryptAndHash(s.publicKey.rawRepresentation)
                default:
                    ss.mixKey(try dhToken(t))
                }
            }
            out += try ss.encryptAndHash(payload)
            guard out.count <= noiseMaxMessage else { throw NoiseError.messageTooLong }
            index += 1
            return out
        } catch {
            failed = true
            throw error
        }
    }

    /// Returns the decrypted payload. Any failure poisons the handshake.
    public func readMessage(_ message: Data) throws -> Data {
        guard !failed, index < messages.count, !myTurn() else { throw NoiseError.invalidState }
        guard message.count <= noiseMaxMessage else { failed = true; throw NoiseError.messageTooLong }
        do {
            var rest = Data(message)  // re-based copy: indices start at 0
            func take(_ n: Int) throws -> Data {
                guard rest.count >= n else { throw NoiseError.messageTooShort }
                let part = Data(rest.prefix(n))
                rest = Data(rest.dropFirst(n))
                return part
            }
            for t in messages[index] {
                switch t {
                case .e:
                    guard re == nil else { throw NoiseError.invalidState }
                    let raw = try take(dhLength)
                    guard let k = try? PublicKey(rawRepresentation: raw) else { throw NoiseError.invalidKey }
                    re = k
                    ss.mixHash(raw)
                case .s:
                    guard rs == nil else { throw NoiseError.invalidState }
                    let n = dhLength + (ss.hasKey ? aeadTagLength : 0)
                    let raw = try ss.decryptAndHash(try take(n))
                    guard let k = try? PublicKey(rawRepresentation: raw) else { throw NoiseError.invalidKey }
                    rs = k
                default:
                    ss.mixKey(try dhToken(t))
                }
            }
            let payload = try ss.decryptAndHash(rest)
            index += 1
            return payload
        } catch {
            failed = true
            throw error
        }
    }

    /// Final step once `isComplete`. Consumes nothing but derives the two transport keys.
    public func split() throws -> TransportKeys {
        guard isComplete else { throw NoiseError.invalidState }
        let (k1, k2) = ss.split()
        let a = SymmetricKey(data: k1), b = SymmetricKey(data: k2)
        return initiator ? TransportKeys(send: a, receive: b) : TransportKeys(send: b, receive: a)
    }
}
