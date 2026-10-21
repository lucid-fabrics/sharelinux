import CryptoKit
import XCTest

@testable import HelperCore

/// Cacophony vectors for the two patterns we use (Noise_XX and Noise_IK, 25519,
/// ChaChaPoly, SHA256), copied unchanged from
/// https://github.com/centromere/cacophony/blob/master/vectors/cacophony.txt
final class NoiseVectorTests: XCTestCase {
    struct Message: Decodable { let payload: String; let ciphertext: String }
    struct Vector: Decodable {
        let protocol_name: String
        let init_prologue: String
        let init_static: String?
        let init_ephemeral: String
        let init_remote_static: String?
        let resp_prologue: String
        let resp_static: String?
        let resp_ephemeral: String
        let handshake_hash: String
        let messages: [Message]
    }
    struct File: Decodable { let vectors: [Vector] }

    func load() throws -> [Vector] {
        let url = try XCTUnwrap(Bundle.module.url(forResource: "noise-vectors", withExtension: "json"))
        return try JSONDecoder().decode(File.self, from: Data(contentsOf: url)).vectors
    }

    func key(_ hex: String?) throws -> Curve25519.KeyAgreement.PrivateKey {
        let d = try XCTUnwrap(hex.flatMap { Data(hex: $0) })
        return try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: d)
    }

    func runVector(_ v: Vector, pattern: NoisePattern) throws {
        XCTAssertEqual(v.protocol_name, pattern.protocolName)
        let initE = try key(v.init_ephemeral), respE = try key(v.resp_ephemeral)
        let initS = try key(v.init_static), respS = try key(v.resp_static)
        let remote: Data? = pattern == .ik ? respS.publicKey.rawRepresentation : nil
        if pattern == .ik {
            XCTAssertEqual(v.init_remote_static, respS.publicKey.rawRepresentation.hex)
        }
        let ini = try HandshakeState(
            pattern: pattern, initiator: true, staticKey: initS, remoteStatic: remote,
            prologue: Data(hex: v.init_prologue)!, ephemeralSource: { initE })
        let res = try HandshakeState(
            pattern: pattern, initiator: false, staticKey: respS,
            prologue: Data(hex: v.resp_prologue)!, ephemeralSource: { respE })

        var i = 0
        while !(ini.isComplete && res.isComplete) {
            let m = v.messages[i]
            let (writer, reader) = i % 2 == 0 ? (ini, res) : (res, ini)
            let ct = try writer.writeMessage(payload: Data(hex: m.payload)!)
            XCTAssertEqual(ct.hex, m.ciphertext, "handshake message \(i)")
            let pt = try reader.readMessage(ct)
            XCTAssertEqual(pt.hex, m.payload)
            i += 1
        }
        XCTAssertEqual(ini.handshakeHash.hex, v.handshake_hash)
        XCTAssertEqual(res.handshakeHash.hex, v.handshake_hash)
        XCTAssertEqual(ini.remoteStatic, respS.publicKey.rawRepresentation)
        XCTAssertEqual(res.remoteStatic, initS.publicKey.rawRepresentation)

        // Post-handshake transport messages alternate, initiator first when i is even.
        let ik = try ini.split(), rk = try res.split()
        var counters: [Bool: UInt64] = [true: 0, false: 0]
        while i < v.messages.count {
            let m = v.messages[i]
            let fromInitiator = i % 2 == 0
            let sendKey = fromInitiator ? ik.send : rk.send
            let recvKey = fromInitiator ? rk.receive : ik.receive
            let n = counters[fromInitiator]!
            let ct = try NoiseAEAD.seal(key: sendKey, counter: n, ad: Data(), plaintext: Data(hex: m.payload)!)
            XCTAssertEqual(ct.hex, m.ciphertext, "transport message \(i)")
            let pt = try NoiseAEAD.open(key: recvKey, counter: n, ad: Data(), sealed: ct)
            XCTAssertEqual(pt.hex, m.payload)
            counters[fromInitiator] = n + 1
            i += 1
        }
    }

    func testXXVector() throws {
        let v = try XCTUnwrap(load().first { $0.protocol_name == NoisePattern.xx.protocolName })
        try runVector(v, pattern: .xx)
    }

    func testIKVector() throws {
        let v = try XCTUnwrap(load().first { $0.protocol_name == NoisePattern.ik.protocolName })
        try runVector(v, pattern: .ik)
    }
}
