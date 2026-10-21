import XCTest

@testable import HelperCore

final class WireTests: XCTestCase {
    func testInputEncodingLayout() {
        let p = InputPacket(
            type: .input, buttons: [.left, .middle], dx: -2, dy: 0x0102, sx: 7, sy: -7,
            seqLocal: 0xA1B2C3D4, flags: InputPacket.flagPointerLocked)
        let want: [UInt8] = [
            1, 0b101, 0xFE, 0xFF, 0x02, 0x01, 7, 0, 0xF9, 0xFF, 0xD4, 0xC3, 0xB2, 0xA1, 1, 0, 0, 0, 0, 0,
        ]
        XCTAssertEqual(Array(p.encode()), want)
        XCTAssertEqual(try InputPacket.decode(Data(want)), p)
    }

    func testRoundTripAcrossTheRange() throws {
        for v: Int16 in [Int16.min, -1, 0, 1, Int16.max] {
            let p = InputPacket(type: .input, buttons: ButtonMask(rawValue: 0x1f), dx: v, dy: v, sx: v, sy: v, seqLocal: .max)
            XCTAssertEqual(try InputPacket.decode(p.encode()), p)
        }
    }

    func testDecodeRejectsEveryWrongLength() {
        let ok = InputPacket(type: .keepalive).encode()
        for n in 0..<64 where n != InputPacket.size {
            let d = n < ok.count ? ok.prefix(n) : ok + Data(count: n - ok.count)
            XCTAssertThrowsError(try InputPacket.decode(Data(d)), "length \(n)")
        }
    }

    func testDecodeRejectsBadTypeReservedBitsAndPadding() {
        var d = InputPacket(type: .input).encode()
        for t: UInt8 in [0, 4, 10, 255] {
            d[0] = t
            XCTAssertThrowsError(try InputPacket.decode(d))
        }
        d = InputPacket(type: .input).encode()
        for bit in 5..<8 {
            var x = d; x[1] = 1 << UInt8(bit)
            XCTAssertThrowsError(try InputPacket.decode(x), "button bit \(bit)")
        }
        for bit in 1..<8 {
            var x = d; x[14] = 1 << UInt8(bit)
            XCTAssertThrowsError(try InputPacket.decode(x), "flag bit \(bit)")
        }
        for i in 15..<20 {
            var x = d; x[i] = 1
            XCTAssertThrowsError(try InputPacket.decode(x), "pad \(i)")
        }
    }

    func testKeepaliveAndByeMustNotCarryMotion() {
        for t in [PacketType.keepalive, .bye] {
            for field in [1, 2, 4, 6, 8] {
                var x = InputPacket(type: t).encode()
                x[field] = 1
                XCTAssertThrowsError(try InputPacket.decode(x), "type \(t) field \(field)")
            }
            XCTAssertNoThrow(try InputPacket.decode(InputPacket(type: t, seqLocal: 9, flags: 1).encode()))
        }
    }

    func testClamp() {
        let p = InputPacket(type: .input, dx: .min, dy: .max, sx: 5000, sy: -5000).clamped()
        XCTAssertEqual(p.dx, -32767)
        XCTAssertEqual(p.dy, 32767)
        XCTAssertEqual(p.sx, 2000)
        XCTAssertEqual(p.sy, -2000)
        let q = InputPacket(type: .input, dx: 100, dy: -100, sx: 1999, sy: -2000)
        XCTAssertEqual(q.clamped(), q)
    }

    func testStatus() throws {
        for s in HelperState.allCases {
            XCTAssertEqual(try StatusPacket.decode(StatusPacket(state: s).encode()), StatusPacket(state: s))
        }
        XCTAssertEqual(Array(StatusPacket(state: .locked).encode()), [10, 2])
        XCTAssertThrowsError(try StatusPacket.decode(Data([10, 7])))
        XCTAssertThrowsError(try StatusPacket.decode(Data([11, 0])))
        XCTAssertThrowsError(try StatusPacket.decode(Data([10])))
        XCTAssertThrowsError(try StatusPacket.decode(Data([10, 0, 0])))
    }

    func testParseDatagram() {
        XCTAssertNil(parseDatagram(Data()))
        XCTAssertNil(parseDatagram(Data([0])))
        XCTAssertNil(parseDatagram(Data([0x04, 1, 2])))
        XCTAssertNil(parseDatagram(Data([0x03]) + Data(count: WireLimits.maxDatagram)))
        let r = parseDatagram(Data([0x03, 9, 8]))
        XCTAssertEqual(r?.kind, .transport)
        XCTAssertEqual(r?.body, Data([9, 8]))
        XCTAssertNotNil(parseDatagram(Data([0x03]) + Data(count: WireLimits.maxDatagram - 1)))
    }

    func testGateTruthTableMatchesReference() {
        let a = PeerAddress(bytes: [192, 0, 2, 1])!
        let b = PeerAddress(bytes: [192, 0, 2, 2])!
        for mask in 0..<(1 << 8) {
            func f(_ i: Int) -> Bool { mask & (1 << i) != 0 }
            let inputs = GateInputs(
                toggleOn: f(0), peerPaired: f(1), udpPeer: a,
                screenSharingPeers: f(2) ? [a, b] : [b], consoleUID: f(3) ? 501 : 502, ownUID: 501,
                screenLocked: f(4) ? false : (f(5) ? true : nil), accessibilityGranted: f(6), rateLimited: f(7))
            let want: GateDecision
            if !f(1) { want = .dropSilently }
            else if !f(0) { want = .deny(.disabled) }
            else if !f(3) { want = .deny(.notConsole) }
            else if !f(4) { want = .deny(.locked) }
            else if !f(2) { want = .deny(.noSession) }
            else if !f(6) { want = .deny(.needsGrant) }
            else if f(7) { want = .deny(.rateLimited) }
            else { want = .accept }
            XCTAssertEqual(Gate.evaluate(inputs), want, "mask \(mask)")
        }
    }

    func testReplayWindowAgainstReferenceModel() {
        // Reference: accept iff never seen and within 2048 of the highest seen.
        var rng = SplitMix(seed: 7)
        var w = ReplayWindow()
        var seen = Set<UInt64>()
        var highest: UInt64?
        for _ in 0..<20000 {
            let base = highest ?? 0
            let c = rng.next() % 3 == 0 ? base &+ rng.next() % 5000 : (base > 3000 ? base - rng.next() % 3000 : rng.next() % 3000)
            let ok = !seen.contains(c) && (highest.map { c > $0 || $0 - c < 2048 } ?? true)
            XCTAssertEqual(w.check(c), ok)
            XCTAssertEqual(w.commit(c), ok)
            if ok { seen.insert(c); highest = max(highest ?? 0, c) }
        }
    }
}

/// Small deterministic PRNG so failures reproduce.
struct SplitMix: RandomNumberGenerator {
    var s: UInt64
    init(seed: UInt64) { s = seed }
    mutating func next() -> UInt64 {
        s &+= 0x9E3779B97F4A7C15
        var z = s
        z = (z ^ (z >> 30)) &* 0xBF58476D1CE4E5B9
        z = (z ^ (z >> 27)) &* 0x94D049BB133111EB
        return z ^ (z >> 31)
    }
}
