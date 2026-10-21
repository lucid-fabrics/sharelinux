import XCTest

@testable import HelperCore

/// Random and mutated bytes must never crash a decoder and never be accepted
/// as authenticated input. Deterministic seeds so failures reproduce.
final class FuzzTests: XCTestCase {
    func randomData(_ rng: inout SplitMix, max: Int) -> Data {
        let n = Int(rng.next() % UInt64(max + 1))
        return Data((0..<n).map { _ in UInt8(truncatingIfNeeded: rng.next()) })
    }

    func testDecodersNeverCrash() {
        var rng = SplitMix(seed: 1)
        var accepted = 0
        for _ in 0..<200_000 {
            let d = randomData(&rng, max: 40)
            if (try? InputPacket.decode(d)) != nil { accepted += 1 }
            _ = try? StatusPacket.decode(d)
            _ = parseDatagram(d)
            _ = TAI64N.decode(d)
            _ = PairingResponder.validateDeviceName(d)
        }
        // A random 20-byte string is a valid packet with probability ~2^-40.
        XCTAssertEqual(accepted, 0)
    }

    func testStructuredMutationsOfValidPackets() {
        var rng = SplitMix(seed: 2)
        let base = InputPacket(type: .input, buttons: .left, dx: 3, dy: 4).encode()
        for _ in 0..<100_000 {
            var d = base
            for _ in 0..<(1 + Int(rng.next() % 4)) { d[Int(rng.next() % UInt64(d.count))] = UInt8(truncatingIfNeeded: rng.next()) }
            if let p = try? InputPacket.decode(d) {
                // Whatever decodes must re-encode to the same bytes (no lossy accept).
                XCTAssertEqual(p.encode(), d)
            }
        }
    }

    func testChannelRejectsRandomDatagramsSilently() throws {
        var rng = SplitMix(seed: 3)
        var mono = 1000.0
        let ch = HelperChannel(
            staticKey: PrivKey(), pairedKeys: { [] },
            environment: { _, _ in fatalError("must not be reached") },
            clock: ChannelClock(monotonic: { mono }, wall: { Date() }))
        for i in 0..<30_000 {
            var d = randomData(&rng, max: 600)
            if !d.isEmpty, i % 2 == 0 { d[0] = [0x01, 0x02, 0x03, 0x10, 0x11, 0x12][Int(rng.next() % 6)] }
            if i % 4 == 0, d.count > 109 { d = d.prefix(109) }
            let r = ch.receive(d, from: nil)
            XCTAssertNil(r.reply)
            for e in r.events {
                if case .dropped = e { continue }
                XCTFail("accepted garbage: \(e)")
            }
            mono += 0.01
        }
    }

    func testPairingResponderSurvivesGarbage() throws {
        var rng = SplitMix(seed: 4)
        for _ in 0..<5000 {
            let r = try PairingResponder(staticKey: PrivKey())
            var d = randomData(&rng, max: 130)
            if !d.isEmpty { d[0] = [0x10, 0x12, 0x11][Int(rng.next() % 3)] }
            _ = try? r.receive(d)
            XCTAssertFalse(r.isComplete)
        }
    }

    func testTransportSessionRejectsRandomCiphertext() throws {
        let a = PrivKey(), b = PrivKey()
        let i = try HandshakeState(pattern: .ik, initiator: true, staticKey: a, remoteStatic: b.publicKey.rawRepresentation)
        let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        _ = try r.readMessage(try i.writeMessage())
        _ = try i.readMessage(try r.writeMessage())
        let s = TransportSession(keys: try r.split(), now: 0)
        var rng = SplitMix(seed: 5)
        for _ in 0..<20_000 {
            var d = randomData(&rng, max: 80)
            if !d.isEmpty { d[0] = 0x03 }
            XCTAssertThrowsError(try s.open(d, maxPlaintext: 20, now: 1))
        }
    }
}
