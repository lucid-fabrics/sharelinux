import CryptoKit
import XCTest

@testable import HelperCore

typealias PrivKey = Curve25519.KeyAgreement.PrivateKey

final class HandshakeTests: XCTestCase {
    func ik(_ a: PrivKey, _ b: PrivKey) throws -> (HandshakeState, HandshakeState) {
        (
            try HandshakeState(pattern: .ik, initiator: true, staticKey: a, remoteStatic: b.publicKey.rawRepresentation),
            try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        )
    }

    func testIKRoundTripAndTransport() throws {
        let a = PrivKey(), b = PrivKey()
        let (i, r) = try ik(a, b)
        let m1 = try i.writeMessage(payload: Data("ts".utf8))
        XCTAssertEqual(try r.readMessage(m1), Data("ts".utf8))
        XCTAssertEqual(r.remoteStatic, a.publicKey.rawRepresentation)
        let m2 = try r.writeMessage()
        XCTAssertEqual(try i.readMessage(m2), Data())
        XCTAssertTrue(i.isComplete && r.isComplete)
        XCTAssertEqual(i.handshakeHash, r.handshakeHash)
        let ki = try i.split(), kr = try r.split()
        let sealed = try NoiseAEAD.seal(key: ki.send, counter: 0, ad: Data(), plaintext: Data([1, 2, 3]))
        XCTAssertEqual(try NoiseAEAD.open(key: kr.receive, counter: 0, ad: Data(), sealed: sealed), Data([1, 2, 3]))
        XCTAssertThrowsError(try NoiseAEAD.open(key: kr.receive, counter: 1, ad: Data(), sealed: sealed))
        XCTAssertThrowsError(try NoiseAEAD.open(key: kr.send, counter: 0, ad: Data(), sealed: sealed))
    }

    func testIKWrongPinnedKeyFailsAtResponder() throws {
        let a = PrivKey(), b = PrivKey(), wrong = PrivKey()
        let i = try HandshakeState(pattern: .ik, initiator: true, staticKey: a, remoteStatic: wrong.publicKey.rawRepresentation)
        let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        let m1 = try i.writeMessage()
        XCTAssertThrowsError(try r.readMessage(m1))
        // Poisoned: nothing works afterwards.
        XCTAssertThrowsError(try r.writeMessage())
        XCTAssertFalse(r.isComplete)
    }

    func testEveryBitFlipInIKMessageOneIsRejected() throws {
        let a = PrivKey(), b = PrivKey()
        let m1 = try ik(a, b).0.writeMessage(payload: Data(count: 12))
        for bit in 0..<(m1.count * 8) {
            var t = m1
            t[bit / 8] ^= 1 << UInt8(bit % 8)
            let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
            // Flipping the ephemeral key bits changes the DH so decryption fails too.
            XCTAssertThrowsError(try r.readMessage(t), "bit \(bit)")
        }
    }

    func testTruncatedAndExtendedMessagesRejected() throws {
        let a = PrivKey(), b = PrivKey()
        let m1 = try ik(a, b).0.writeMessage(payload: Data(count: 12))
        for n in 0..<m1.count {
            let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
            XCTAssertThrowsError(try r.readMessage(m1.prefix(n)), "len \(n)")
        }
        let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        XCTAssertThrowsError(try r.readMessage(m1 + Data([0])))
    }

    func testLowOrderEphemeralAborts() throws {
        let b = PrivKey()
        let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        // All-zero public key is a low-order point: DH output is all zeros.
        XCTAssertThrowsError(try r.readMessage(Data(count: 108))) { e in
            XCTAssertTrue([NoiseError.lowOrderPoint, .invalidKey].contains(e as? NoiseError), "\(e)")
        }
    }

    func testOutOfTurnAndDoubleUseRejected() throws {
        let (i, r) = try ik(PrivKey(), PrivKey())
        XCTAssertThrowsError(try r.writeMessage())  // responder cannot speak first
        XCTAssertThrowsError(try i.readMessage(Data(count: 48)))
        _ = try i.writeMessage()
        XCTAssertThrowsError(try i.writeMessage())  // not its turn again
        XCTAssertThrowsError(try i.split())  // incomplete
    }

    func testIKInitiatorNeedsValidRemoteKey() {
        XCTAssertThrowsError(try HandshakeState(pattern: .ik, initiator: true, staticKey: PrivKey()))
        XCTAssertThrowsError(
            try HandshakeState(pattern: .ik, initiator: true, staticKey: PrivKey(), remoteStatic: Data(count: 5)))
        XCTAssertThrowsError(try HandshakeState(pattern: .xx, initiator: true, staticKey: PrivKey(), remoteStatic: Data(count: 32)))
    }

    func testPrologueMismatchFails() throws {
        let a = PrivKey(), b = PrivKey()
        let i = try HandshakeState(pattern: .ik, initiator: true, staticKey: a, remoteStatic: b.publicKey.rawRepresentation, prologue: Data("x".utf8))
        let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b, prologue: Data("y".utf8))
        XCTAssertThrowsError(try r.readMessage(try i.writeMessage()))
    }

    func testPairingRejectsBadNamesAndShapes() throws {
        XCTAssertNil(PairingResponder.validateDeviceName(Data()))
        XCTAssertNil(PairingResponder.validateDeviceName(Data(count: 65)))
        XCTAssertNil(PairingResponder.validateDeviceName(Data("a\nb".utf8)))
        XCTAssertNil(PairingResponder.validateDeviceName(Data("a\u{202E}b".utf8)))
        XCTAssertNil(PairingResponder.validateDeviceName(Data([0xff, 0xfe])))
        XCTAssertNil(PairingResponder.validateDeviceName(Data("   ".utf8)))
        XCTAssertEqual(PairingResponder.validateDeviceName(Data("Alice's PC".utf8)), "Alice's PC")
        let r = try PairingResponder(staticKey: PrivKey())
        XCTAssertThrowsError(try r.receive(Data([DatagramKind.xxMessage3.rawValue]) + Data(count: 64)))  // wrong stage
        XCTAssertThrowsError(try r.receive(Data([DatagramKind.xxMessage1.rawValue]) + Data(count: 31)))
        XCTAssertThrowsError(try r.receive(Data()))
    }

    func testTAI64N() {
        let t = Date(timeIntervalSince1970: 1_800_000_000.25)
        let e = TAI64N.encode(t)
        XCTAssertEqual(e.count, 12)
        XCTAssertEqual(e.prefix(8).hex, "40000000" + String(format: "%08x", 1_800_000_000))
        XCTAssertEqual(TAI64N.decode(e)!, 1_800_000_000.25, accuracy: 1e-6)
        let later = TAI64N.encode(Date(timeIntervalSince1970: 1_800_000_000.26))
        XCTAssertTrue(TAI64N.isNewer(later, than: e))
        XCTAssertFalse(TAI64N.isNewer(e, than: later))
        XCTAssertFalse(TAI64N.isNewer(e, than: e))
        XCTAssertNil(TAI64N.decode(Data(count: 12)))
        XCTAssertNil(TAI64N.decode(Data(count: 11)))
    }
}
