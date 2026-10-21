import CryptoKit
import XCTest

@testable import HelperCore

/// The Linux side of pairing, written from the protocol description only, so
/// these tests do not share code with the Mac side under test.
final class LinuxPairer {
    let hs: HandshakeState
    let nonce: Data
    let name: String
    private(set) var session: TransportSession?
    private(set) var transcript = Data()
    private(set) var code: String?

    init(key: PrivKey, name: String = "Linux box", nonce: Data = randomPairingNonce(), prologue: Data? = nil) throws {
        hs = try HandshakeState(pattern: .xx, initiator: true, staticKey: key, prologue: prologue)
        self.name = name
        self.nonce = nonce
    }

    func message1() throws -> Data { Data([DatagramKind.xxMessage1.rawValue]) + (try hs.writeMessage()) }

    /// Reads message 2, returns message 3 and the commit.
    func afterMessage2(_ m2: Data, nameBytes: Data? = nil) throws -> (m3: Data, commit: Data) {
        _ = try hs.readMessage(m2.dropFirst())
        let m3 = Data([DatagramKind.xxMessage3.rawValue]) + (try hs.writeMessage(payload: nameBytes ?? Data(name.utf8)))
        transcript = hs.handshakeHash
        let s = TransportSession(keys: try hs.split(), now: 0, kind: .pairingTransport)
        session = s
        let commit = try s.seal(Data([PairingTag.commit]) + PairingCode.commitment(of: nonce), now: 0)
        return (m3, commit)
    }

    /// Reads the Mac's nonce, returns the reveal; the code exists from here on.
    func afterResponderNonce(_ dg: Data, revealing: Data? = nil) throws -> Data {
        let pt = try XCTUnwrap(session).open(dg, maxPlaintext: PairingTag.messageSize, now: 0)
        XCTAssertEqual(pt.count, PairingTag.messageSize)
        XCTAssertEqual(pt.first, PairingTag.responderNonce)
        code = PairingCode.code(handshakeHash: transcript, initiatorNonce: nonce, responderNonce: Data(pt.dropFirst()))
        return try XCTUnwrap(session).seal(Data([PairingTag.reveal]) + (revealing ?? nonce), now: 0)
    }
}

final class PairingTests: XCTestCase {
    let macKey = PrivKey()

    func responder(nonce: @escaping () -> Data = randomPairingNonce) throws -> PairingResponder {
        try PairingResponder(staticKey: macKey, clock: { 0 }, nonceSource: nonce)
    }

    /// Runs an honest pairing; returns both ends.
    func honest(_ r: PairingResponder, _ l: LinuxPairer) throws {
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, commit) = try l.afterMessage2(m2)
        XCTAssertNil(try r.receive(m3))
        let rm = try XCTUnwrap(try r.receive(commit))
        XCTAssertNil(try r.receive(try l.afterResponderNonce(rm)))
    }

    func testHonestPairingShowsTheSameCodeAndKeys() throws {
        let key = PrivKey()
        let r = try responder(), l = try LinuxPairer(key: key)
        try honest(r, l)
        XCTAssertEqual(l.code, r.sas)
        XCTAssertEqual(r.sas?.count, 6)
        XCTAssertTrue(r.sas!.allSatisfy(\.isASCII) && r.sas!.allSatisfy(\.isNumber))
        XCTAssertEqual(r.remoteStatic, key.publicKey.rawRepresentation)
        XCTAssertEqual(r.deviceName, "Linux box")
        XCTAssertEqual(l.hs.remoteStatic, macKey.publicKey.rawRepresentation)
        XCTAssertThrowsError(try r.receive(try LinuxPairer(key: PrivKey()).message1())) {
            XCTAssertEqual($0 as? PairingError, .finished)
        }
    }

    func testNothingIsExposedBeforeTheCodeExists() throws {
        let r = try responder(), l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = try r.receive(m3)
        XCTAssertNil(r.deviceName); XCTAssertNil(r.remoteStatic); XCTAssertNil(r.sas)
        let rm = try XCTUnwrap(try r.receive(commit))
        XCTAssertNil(r.deviceName); XCTAssertNil(r.remoteStatic); XCTAssertNil(r.sas)
        _ = try r.receive(try l.afterResponderNonce(rm))
        XCTAssertNotNil(r.sas)
    }

    /// Commit before reveal: the Mac's random value must not exist, let alone
    /// leave, until the commitment arrived. This is what stops a man in the
    /// middle from choosing his last handshake message after seeing it.
    func testResponderNonceIsDrawnAndSentOnlyAfterTheCommit() throws {
        var draws = 0
        let r = try responder(nonce: { draws += 1; return Data(repeating: 7, count: 32) })
        let l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, commit) = try l.afterMessage2(m2)
        XCTAssertNil(try r.receive(m3), "nothing may answer message 3")
        XCTAssertEqual(draws, 0)
        let rm = try XCTUnwrap(try r.receive(commit))
        XCTAssertEqual(draws, 1)
        XCTAssertEqual(rm[0], DatagramKind.pairingTransport.rawValue)
    }

    func testRevealWithoutCommitIsRefused() throws {
        let r = try responder(), l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, _) = try l.afterMessage2(m2)
        _ = try r.receive(m3)
        let early = try XCTUnwrap(l.session).seal(Data([PairingTag.reveal]) + l.nonce, now: 0)
        XCTAssertThrowsError(try r.receive(early)) { XCTAssertEqual($0 as? PairingError, .malformed) }
        XCTAssertNil(r.sas)
    }

    func testRevealThatDoesNotMatchTheCommitIsRefused() throws {
        let r = try responder(), l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = try r.receive(m3)
        let rm = try XCTUnwrap(try r.receive(commit))
        let lie = try l.afterResponderNonce(rm, revealing: Data(repeating: 9, count: 32))
        XCTAssertThrowsError(try r.receive(lie)) { XCTAssertEqual($0 as? PairingError, .commitMismatch) }
        XCTAssertNil(r.sas)
        XCTAssertNil(r.remoteStatic)
        // The attempt is dead: even the honest reveal now fails.
        let ok = try XCTUnwrap(l.session).seal(Data([PairingTag.reveal]) + l.nonce, now: 0)
        XCTAssertThrowsError(try r.receive(ok))
        XCTAssertNil(r.sas)
    }

    func testPairingMessagesOnlyWorkUnderTheXXKeysAndKind() throws {
        let r = try responder(), l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = try r.receive(m3)
        var asSession = commit
        asSession[0] = DatagramKind.transport.rawValue
        XCTAssertThrowsError(try r.receive(asSession))
    }

    func testCodeKnownAnswers() {
        // Computed independently with python hmac/hashlib (same values in Go).
        XCTAssertEqual(PairingCode.commitment(of: Data(repeating: 1, count: 32)).hex,
            "dd391c60e44cd686917444d96dfbf2cd9782bc0ed157a1c4d3b963d598c41b95")
        XCTAssertEqual(
            PairingCode.code(handshakeHash: Data(count: 32), initiatorNonce: Data(repeating: 1, count: 32),
                responderNonce: Data(repeating: 2, count: 32)), "401886")
    }

    func testCodeDependsOnEveryInputAndIsUniform() {
        let h = Data(count: 32), a = Data(repeating: 1, count: 32), b = Data(repeating: 2, count: 32)
        let base = PairingCode.code(handshakeHash: h, initiatorNonce: a, responderNonce: b)
        var h2 = h; h2[31] ^= 1
        var a2 = a; a2[0] ^= 1
        var b2 = b; b2[15] ^= 1
        XCTAssertNotEqual(PairingCode.code(handshakeHash: h2, initiatorNonce: a, responderNonce: b), base)
        XCTAssertNotEqual(PairingCode.code(handshakeHash: h, initiatorNonce: a2, responderNonce: b), base)
        XCTAssertNotEqual(PairingCode.code(handshakeHash: h, initiatorNonce: a, responderNonce: b2), base)
        XCTAssertNotEqual(PairingCode.code(handshakeHash: h, initiatorNonce: b, responderNonce: a), base)
        var rng = SplitMix(seed: 11)
        var firstDigit = [Int](repeating: 0, count: 10)
        for _ in 0..<20_000 {
            let x = Data((0..<32).map { _ in UInt8(truncatingIfNeeded: rng.next()) })
            let c = PairingCode.code(handshakeHash: h, initiatorNonce: x, responderNonce: b)
            XCTAssertEqual(c.count, 6)
            firstDigit[Int(c.first!.wholeNumberValue!)] += 1
        }
        for n in firstDigit { XCTAssertTrue((1700...2300).contains(n), "\(firstDigit)") }
    }

    // MARK: man in the middle

    /// The strongest strategy there is against commit-before-reveal: start the
    /// Mac-side handshake only after learning r_L from the honest Linux side,
    /// then grind the last handshake message (fresh static key and device-name
    /// variants) for the best match. Whatever it does, the Mac's r_M is drawn
    /// after the attacker's final choice, so the codes agree only by luck:
    /// 10^-6 per attempt, regardless of how many variants were ground.
    func mitmAttempt(grind tries: Int, linuxNonce: Data, macNonce: Data) throws -> Bool {
        let alice = try LinuxPairer(key: PrivKey(), nonce: linuxNonce)
        // Side A: the attacker plays Mac towards Linux and must answer the commit
        // with an r_M before it can learn r_L.
        let mA = try PairingResponder(staticKey: PrivKey(), clock: { 0 }, nonceSource: { Data(repeating: 0xAA, count: 32) })
        let m2 = try XCTUnwrap(try mA.receive(try alice.message1()))
        let (m3, commit) = try alice.afterMessage2(m2)
        _ = try mA.receive(m3)
        let rmToAlice = try XCTUnwrap(try mA.receive(commit))
        let reveal = try alice.afterResponderNonce(rmToAlice)
        _ = try mA.receive(reveal)  // the attacker now holds r_L
        let target = try XCTUnwrap(alice.code)
        // Side B: against the real Mac code. Grind for the variant whose code
        // would match if r_M were all zeros (the only guess available).
        var best: (key: PrivKey, name: String)?
        for i in 0..<max(1, tries) {
            let key = PrivKey()
            let name = "Linux box" + String(repeating: "\u{00B7}", count: i % 3) + "\(i)"
            let guess = try pairWithMac(key: key, name: name, rL: linuxNonce, rM: Data(count: 32), dryRun: true)
            if guess == target { best = (key, name); break }
            if best == nil { best = (key, name) }
        }
        let (key, name) = try XCTUnwrap(best)
        let real = try pairWithMac(key: key, name: name, rL: linuxNonce, rM: macNonce, dryRun: false)
        return real == target
    }

    /// Plays the attacker against a Mac whose nonce is `rM`; returns the code the Mac shows.
    func pairWithMac(key: PrivKey, name: String, rL: Data, rM: Data, dryRun: Bool) throws -> String? {
        let mac = try PairingResponder(staticKey: macKey, clock: { 0 }, nonceSource: { rM })
        let l = try LinuxPairer(key: key, name: name, nonce: rL)
        let m2 = try XCTUnwrap(try mac.receive(try l.message1()))
        let (m3, commit) = try l.afterMessage2(m2)
        _ = try mac.receive(m3)
        let rm = try XCTUnwrap(try mac.receive(commit))
        _ = try mac.receive(try l.afterResponderNonce(rm))
        return mac.sas
    }

    func testGrindingMitmCannotSteerTheCode() throws {
        var rng = SplitMix(seed: 5)
        func bytes() -> Data { Data((0..<32).map { _ in UInt8(truncatingIfNeeded: rng.next()) }) }
        for tries in [1, 40] {
            var hits = 0
            for _ in 0..<150 {
                if try mitmAttempt(grind: tries, linuxNonce: bytes(), macNonce: bytes()) { hits += 1 }
            }
            // Expected 150 * 10^-6; a single hit would already be a red flag.
            XCTAssertEqual(hits, 0, "grinding \(tries) variants matched \(hits) of 150 attempts")
        }
    }

    /// The reason grinding fails, stated directly: with every attacker-controlled
    /// input fixed (both handshakes, the commitment), the code on each screen
    /// still takes a new unpredictable value for every honest random value.
    func testCodesAreUnpredictableFromAttackerControlledState() throws {
        var rng = SplitMix(seed: 6)
        func bytes() -> Data { Data((0..<32).map { _ in UInt8(truncatingIfNeeded: rng.next()) }) }
        let h = bytes(), fixedNonce = bytes()
        var macCodes = Set<String>(), linuxCodes = Set<String>()
        for _ in 0..<2000 {
            macCodes.insert(PairingCode.code(handshakeHash: h, initiatorNonce: fixedNonce, responderNonce: bytes()))
            linuxCodes.insert(PairingCode.code(handshakeHash: h, initiatorNonce: bytes(), responderNonce: fixedNonce))
        }
        XCTAssertGreaterThan(macCodes.count, 1990)
        XCTAssertGreaterThan(linuxCodes.count, 1990)
    }

    // MARK: device names

    func testDeviceNameRules() {
        func v(_ s: String) -> String? { PairingResponder.validateDeviceName(Data(s.utf8)) }
        XCTAssertEqual(v("Wassim's PC"), "Wassim's PC")
        XCTAssertEqual(v("B\u{FC}ro Laptop 2"), "B\u{FC}ro Laptop 2")
        XCTAssertEqual(v("Rechner \u{1F4BB}"), "Rechner \u{1F4BB}")
        // Normalised to NFC: e + combining acute becomes the single scalar.
        XCTAssertEqual(v("Cafe\u{301}"), "Caf\u{E9}")
        XCTAssertEqual(v("Caf\u{E9}"), "Caf\u{E9}")
        let refused = [
            "", " ", "a\nb", "a\tb", "a\u{0}b",
            "a\u{200B}b",  // zero width space
            "a\u{200D}b",  // zero width joiner
            "a\u{FEFF}b",  // BOM / zero width no-break space
            "a\u{2060}b",  // word joiner
            "a\u{202E}b", "a\u{202A}b", "a\u{2066}b", "a\u{200E}b", "a\u{061C}b",  // bidi controls
            "a\u{E0041}b",  // tag character
            "a\u{3164}b", "a\u{115F}b", "a\u{2800}b", "a\u{034F}b",  // invisible fillers
            "a\u{00A0}b", "a\u{3000}b", "a\u{2003}b",  // non-ASCII spaces
            "a\u{2028}b", "a\u{2029}b",
            "a\u{E000}b",  // private use
            "a\u{0378}b",  // unassigned
            " a", "a ", "a  b",
            String(repeating: "x", count: 65),
        ]
        for s in refused { XCTAssertNil(v(s), "\(s.unicodeScalars.map { String($0.value, radix: 16) })") }
        XCTAssertNotNil(v(String(repeating: "x", count: 64)))
        XCTAssertNil(PairingResponder.validateDeviceName(Data([0xff, 0xfe])))
        XCTAssertNil(PairingResponder.validateDeviceName(Data([0x61, 0xC0, 0x80])))  // overlong NUL
    }

    func testPairingWithInvisibleNameIsRefusedAtMessageThree() throws {
        let r = try responder(), l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try r.receive(try l.message1()))
        let (m3, _) = try l.afterMessage2(m2, nameBytes: Data("Linux\u{200B}box".utf8))
        XCTAssertThrowsError(try r.receive(m3)) { XCTAssertEqual($0 as? PairingError, .badDeviceName) }
    }

    func testShapeAndStageErrors() throws {
        let r = try responder()
        XCTAssertThrowsError(try r.receive(Data([DatagramKind.xxMessage3.rawValue]) + Data(count: 64)))
        let r2 = try responder()
        XCTAssertThrowsError(try r2.receive(Data([DatagramKind.xxMessage1.rawValue]) + Data(count: 31)))
        XCTAssertThrowsError(try responder().receive(Data()))
        XCTAssertThrowsError(try responder().receive(Data([DatagramKind.pairingTransport.rawValue]) + Data(count: 40)))
    }

    // MARK: protocol binding

    func testPrologueBindsNameVersionAndRoles() throws {
        XCTAssertEqual(
            String(data: NoisePattern.xx.standardPrologue, encoding: .utf8),
            "OSS-mousehelper v2 pairing Noise_XX_25519_ChaChaPoly_SHA256 initiator=linux responder=mac")
        XCTAssertEqual(
            String(data: NoisePattern.ik.standardPrologue, encoding: .utf8),
            "OSS-mousehelper v2 session Noise_IK_25519_ChaChaPoly_SHA256 initiator=linux responder=mac")
        // A peer with another version or a swapped role fails at the first message.
        for other in ["OSS-mousehelper v1 pairing Noise_XX_25519_ChaChaPoly_SHA256 initiator=linux responder=mac",
            "OSS-mousehelper v2 pairing Noise_XX_25519_ChaChaPoly_SHA256 initiator=mac responder=linux"] {
            let r = try responder(), l = try LinuxPairer(key: PrivKey(), prologue: Data(other.utf8))
            let m2 = try XCTUnwrap(try r.receive(try l.message1()))  // message 1 carries no tag yet
            XCTAssertThrowsError(try l.afterMessage2(m2))
        }
        XCTAssertThrowsError(try HandshakeState(pattern: .xx, initiator: true, staticKey: PrivKey(), prologue: Data())) {
            XCTAssertEqual($0 as? NoiseError, .emptyPrologue)
        }
    }

    // MARK: key material

    func testHandshakeSecretsAreWipedAfterSplitAndFailure() throws {
        var d = Data([1, 2, 3, 4])
        d.wipe()
        XCTAssertEqual(d, Data(count: 4))

        let a = PrivKey(), b = PrivKey()
        let i = try HandshakeState(pattern: .ik, initiator: true, staticKey: a, remoteStatic: b.publicKey.rawRepresentation)
        let r = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        XCTAssertFalse(i.secretsWiped)
        _ = try r.readMessage(try i.writeMessage())
        _ = try i.readMessage(try r.writeMessage())
        let hash = i.handshakeHash
        _ = try i.split()
        XCTAssertTrue(i.secretsWiped)
        XCTAssertEqual(i.handshakeHash, hash, "the handshake hash is public and stays")
        XCTAssertThrowsError(try i.split(), "a second split would hand out keys derived from a zeroed chaining key")

        let bad = try HandshakeState(pattern: .ik, initiator: false, staticKey: b)
        XCTAssertThrowsError(try bad.readMessage(Data(count: 108)))
        XCTAssertTrue(bad.secretsWiped)
    }
}

// MARK: pairing window

final class PairingWindowTests: XCTestCase {
    let macKey = PrivKey()
    var now = 100.0
    var window: PairingWindow!

    override func setUp() {
        now = 100
        window = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now })
    }

    /// An attempt that finishes XX (so it counts) and then breaks the protocol.
    func failingAttempt() throws {
        let l = try LinuxPairer(key: PrivKey())
        let m2 = try XCTUnwrap(try window.receive(try l.message1()))
        let (m3, _) = try l.afterMessage2(m2)
        _ = try window.receive(m3)
        let early = try XCTUnwrap(l.session).seal(Data([PairingTag.reveal]) + l.nonce, now: 0)
        XCTAssertThrowsError(try window.receive(early))
    }

    func testClosesAfterThreeFailures() throws {
        for n in 1...3 {
            XCTAssertTrue(window.isOpen, "before failure \(n)")
            try failingAttempt()
        }
        XCTAssertFalse(window.isOpen)
        XCTAssertEqual(window.failures, 3)
        let l = try LinuxPairer(key: PrivKey())
        XCTAssertThrowsError(try window.receive(try l.message1())) { XCTAssertEqual($0 as? PairingError, .closed) }
    }

    func testAnswerThatTheCodesDifferCountsAsAFailure() throws {
        for _ in 0..<3 {
            let l = try LinuxPairer(key: PrivKey())
            let m2 = try XCTUnwrap(try window.receive(try l.message1()))
            let (m3, commit) = try l.afterMessage2(m2)
            _ = try window.receive(m3)
            let rm = try XCTUnwrap(try window.receive(commit))
            _ = try window.receive(try l.afterResponderNonce(rm))
            XCTAssertNotNil(window.pending)
            window.reject()
        }
        XCTAssertFalse(window.isOpen)
    }

    func testStrayDatagramsAndAbandonedAttemptsAreFree() throws {
        for i in 0..<20 {
            XCTAssertThrowsError(try window.receive(Data([DatagramKind.xxMessage3.rawValue]) + Data(count: 64 + i)))
            // Message 1 starts an attempt; starting another one abandons it before any code existed.
            let l = try LinuxPairer(key: PrivKey())
            _ = try window.receive(try l.message1())
            // Garbage that fails inside XX never showed a code either.
            XCTAssertThrowsError(try window.receive(Data([DatagramKind.xxMessage3.rawValue]) + Data(count: 80)))
        }
        XCTAssertEqual(window.failures, 0)
        XCTAssertTrue(window.isOpen)
    }

    func testClosesAfterTwoMinutesAndWhenConfirmed() throws {
        now += ChannelLimits.pairingWindow - 1
        XCTAssertTrue(window.isOpen)
        now += 1
        XCTAssertFalse(window.isOpen)
        let l = try LinuxPairer(key: PrivKey())
        XCTAssertThrowsError(try window.receive(try l.message1()))

        let w2 = PairingWindow(staticKey: macKey, clock: { [unowned self] in self.now })
        let key = PrivKey()
        let l2 = try LinuxPairer(key: key)
        let m2 = try XCTUnwrap(try w2.receive(try l2.message1()))
        let (m3, commit) = try l2.afterMessage2(m2)
        _ = try w2.receive(m3)
        XCTAssertNil(w2.confirm(), "no code yet, nothing to confirm")
        let rm = try XCTUnwrap(try w2.receive(commit))
        _ = try w2.receive(try l2.afterResponderNonce(rm))
        // A finished attempt waits for the person; another message 1 cannot replace it.
        XCTAssertThrowsError(try w2.receive(try LinuxPairer(key: PrivKey()).message1())) {
            XCTAssertEqual($0 as? PairingError, .busy)
        }
        let paired = try XCTUnwrap(w2.confirm())
        XCTAssertEqual(paired.key, key.publicKey.rawRepresentation)
        XCTAssertEqual(paired.name, "Linux box")
        XCTAssertFalse(w2.isOpen, "one pairing per click")
    }
}
