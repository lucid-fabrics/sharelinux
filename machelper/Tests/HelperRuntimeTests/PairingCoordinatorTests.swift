import CryptoKit
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// The Linux side of pairing, written from the protocol description only, so
/// these tests do not share code with the Mac side under test.
final class LinuxPairer {
    let hs: HandshakeState
    let nonce: Data
    let name: String
    let key: PrivKey
    private(set) var session: TransportSession?
    private(set) var transcript = Data()
    private(set) var code: String?

    init(key: PrivKey = PrivKey(), name: String = "Linux box", nonce: Data = randomPairingNonce()) throws {
        hs = try HandshakeState(pattern: .xx, initiator: true, staticKey: key)
        self.key = key; self.name = name; self.nonce = nonce
    }

    func message1() throws -> Data { Data([DatagramKind.xxMessage1.rawValue]) + (try hs.writeMessage()) }

    func afterMessage2(_ m2: Data) throws -> (m3: Data, commit: Data) {
        _ = try hs.readMessage(m2.dropFirst())
        let m3 = Data([DatagramKind.xxMessage3.rawValue]) + (try hs.writeMessage(payload: Data(name.utf8)))
        transcript = hs.handshakeHash
        let s = TransportSession(keys: try hs.split(), now: 0, kind: .pairingTransport)
        session = s
        return (m3, try s.seal(Data([PairingTag.commit]) + PairingCode.commitment(of: nonce), now: 0))
    }

    func afterResponderNonce(_ dg: Data) throws -> Data {
        let pt = try XCTUnwrap(session).open(dg, maxPlaintext: PairingTag.messageSize, now: 0)
        code = PairingCode.code(handshakeHash: transcript, initiatorNonce: nonce, responderNonce: Data(pt.dropFirst()))
        return try XCTUnwrap(session).seal(Data([PairingTag.reveal]) + nonce, now: 0)
    }
}

final class PairingCoordinatorTests: XCTestCase {
    let phone = PeerAddress(bytes: [192, 0, 2, 7])!
    let other = PeerAddress(bytes: [192, 0, 2, 9])!

    /// Runs a Linux pairer through the runtime's datagram entry point, up to the
    /// point where both sides hold a code.
    @discardableResult
    func pair(_ rig: RuntimeRig, _ l: LinuxPairer, from src: PeerAddress? = nil) throws -> Bool {
        let src = src ?? phone
        guard let m2 = rig.runtime.handle(datagram: try l.message1(), from: src) else { return false }
        let (m3, commit) = try l.afterMessage2(m2)
        _ = rig.runtime.handle(datagram: m3, from: src)
        guard let rm = rig.runtime.handle(datagram: commit, from: src) else { return false }
        _ = rig.runtime.handle(datagram: try l.afterResponderNonce(rm), from: src)
        return true
    }

    /// The person looks at the code for a moment, then clicks Confirm on what is on screen.
    @discardableResult
    func confirmDisplayed(_ rig: RuntimeRig, wait: Double = 1.1) -> ConfirmRefusal? {
        advance(rig, wait)
        let s = rig.coordinator!.status
        guard let code = s.phaseCode, let key = s.pendingKey else { return .noAttempt }
        return rig.coordinator!.confirm(code: code, peerKey: key)
    }

    func advance(_ rig: RuntimeRig, _ s: Double) { rig.mono += s; rig.wall = rig.wall.addingTimeInterval(s); rig.runtime.tick() }

    /// Pairing is something only the owner's local click starts.
    func testPairingDatagramsDoNothingWhileNoWindowIsOpen() throws {
        let rig = RuntimeRig(pairing: true)
        let l = try LinuxPairer()
        XCTAssertNil(rig.runtime.handle(datagram: try l.message1(), from: phone), "no reply, so a port scan sees nothing")
        XCTAssertFalse(try pair(rig, try LinuxPairer()))
        XCTAssertEqual(rig.coordinator?.status.phase, .closed)
        XCTAssertEqual(rig.coordinator?.status.devices, [])
        XCTAssertEqual(rig.statuses, [], "the UI is not even told")
    }

    func testPairConfirmThenTheDeviceCanConnect() throws {
        let rig = RuntimeRig(pairing: true)
        XCTAssertNil(try rig.connectIfAnswered(), "not paired yet")
        XCTAssertTrue(rig.coordinator!.open())
        XCTAssertEqual(rig.coordinator?.status.phase, .waiting)
        let l = try LinuxPairer(key: rig.linuxKey, name: "Desk PC")
        XCTAssertTrue(try pair(rig, l))
        let code = try XCTUnwrap(l.code)
        XCTAssertEqual(rig.coordinator?.status.phase, .confirm(code: code, device: "Desk PC"))
        confirmDisplayed(rig)
        XCTAssertEqual(rig.coordinator?.status.phase, .closed)
        XCTAssertEqual(rig.coordinator?.status.notice, .paired("Desk PC"))
        XCTAssertEqual(rig.coordinator?.status.devices, [PairedDevice(name: "Desk PC", key: rig.linuxKey.publicKey.rawRepresentation)])
        XCTAssertEqual(try rig.keyStore?.loadPaired().count, 1, "stored through the key store")
        // The pinned key opens a session.
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.2)
        _ = try rig.connect()
        XCTAssertEqual(rig.runtime.state.device, "Desk PC")
        // One pairing per click.
        XCTAssertFalse(try pair(rig, try LinuxPairer()))
    }

    func testRejectingTheCodeStoresNothingAndTheDeviceCannotConnect() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        let l = try LinuxPairer(key: rig.linuxKey)
        try pair(rig, l)
        rig.coordinator?.reject()
        XCTAssertEqual(rig.coordinator?.status.devices, [])
        XCTAssertEqual(rig.coordinator?.status.notice, .rejected)
        XCTAssertEqual(rig.coordinator?.status.phase, .waiting, "window open for another try")
        XCTAssertEqual(try rig.keyStore?.loadPaired(), [])
        XCTAssertNil(try rig.connectIfAnswered())
        // It counted as a failure.
        XCTAssertEqual(try? JSONDecoder().decode([Double].self, from: JSONEncoder().encode(try rig.ledgerStore.load())).count, 1)
    }

    func testThreeRejectsCloseTheWindowAndNineLockPairingWithACooldownTheOwnerCanReset() throws {
        let rig = RuntimeRig(pairing: true)
        for window in 0..<3 {
            XCTAssertTrue(rig.coordinator!.open(), "window \(window)")
            for _ in 0..<3 {
                advance(rig, 2)
                try pair(rig, try LinuxPairer())
                rig.coordinator?.reject()
            }
            XCTAssertEqual(rig.coordinator?.status.notice, .closedAfterFailures)
            if window < 2 { XCTAssertEqual(rig.coordinator?.status.phase, .closed) }
        }
        XCTAssertFalse(rig.coordinator!.open())
        guard case .lockedOut(let seconds) = rig.coordinator!.status.phase else { return XCTFail("\(rig.coordinator!.status.phase)") }
        XCTAssertGreaterThan(seconds, 23 * 3600)
        XCTAssertFalse(try pair(rig, try LinuxPairer()))
        rig.coordinator?.resetCooldown()
        XCTAssertTrue(rig.coordinator!.open())
    }

    /// Follow-up audit, finding 8: unlocking runs from a dialog whose clicks could be the live session's.
    func testTheCooldownCannotBeResetWhileASessionIsLive() throws {
        let rig = RuntimeRig(pairing: true)
        for _ in 0..<3 {
            XCTAssertTrue(rig.coordinator!.open())
            for _ in 0..<3 {
                advance(rig, 2)
                try pair(rig, try LinuxPairer())
                rig.coordinator?.reject()
            }
        }
        var live = true
        rig.coordinator?.isSessionActive = { live }
        XCTAssertEqual(rig.coordinator?.resetCooldown(), false)
        guard case .lockedOut = rig.coordinator!.status.phase else { return XCTFail("unlocked during a live session") }
        XCTAssertEqual(rig.coordinator?.status.notice, .unlockRefusedSessionActive)
        live = false
        XCTAssertEqual(rig.coordinator?.resetCooldown(), true)
        XCTAssertTrue(rig.coordinator!.open())
    }

    func testTheWindowClosesByItselfAfterTwoMinutes() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        advance(rig, ChannelLimits.pairingWindow - 1)
        XCTAssertEqual(rig.coordinator?.status.phase, .waiting)
        advance(rig, 2)
        XCTAssertEqual(rig.coordinator?.status.phase, .closed)
        XCTAssertEqual(rig.coordinator?.status.notice, .expired)
        XCTAssertFalse(try pair(rig, try LinuxPairer()))
    }

    func testAnotherDeviceTryingIsReported() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(name: "First"))
        XCTAssertFalse(rig.coordinator!.status.anotherDeviceTrying)
        advance(rig, 1)
        XCTAssertNil(rig.runtime.handle(datagram: try LinuxPairer().message1(), from: other))
        XCTAssertTrue(rig.coordinator!.status.anotherDeviceTrying)
        XCTAssertEqual(rig.coordinator?.status.phase, .confirm(code: rig.coordinator!.status.phaseCode!, device: "First"), "the first device's code stays")
    }

    func testAKeychainThatRefusesMeansNothingIsPaired() throws {
        let rig = RuntimeRig(pairing: true)
        rig.keyStore?.failWrites = true
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey))
        confirmDisplayed(rig)
        XCTAssertEqual(rig.coordinator?.status.devices, [])
        guard case .storeFailed = rig.coordinator!.status.notice else { return XCTFail("no store failure notice") }
        XCTAssertNil(try rig.connectIfAnswered())
    }

    /// Removing a device ends its live session at once, whatever the runtime queue is doing.
    func testRemovingADeviceEndsItsLiveSessionImmediately() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        confirmDisplayed(rig)
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.2)
        let c = try rig.connect()
        rig.send(c, InputPacket(type: .input, buttons: [.left], dx: 16, dy: 0))
        XCTAssertEqual(rig.runtime.guardedPoster.buttonsDown, [.left])
        rig.poster.log.removeAll()
        rig.runtime.queue.sync { _ = rig.runtime.removePairedDevice(rig.linuxKey.publicKey.rawRepresentation) }
        rig.runtime.queue.sync {}
        XCTAssertEqual(rig.runtime.guardedPoster.buttonsDown, [], "button released")
        XCTAssertTrue(rig.poster.log.contains { $0.hasPrefix("up") })
        XCTAssertNil(rig.runtime.state.device, "session over")
        XCTAssertEqual(rig.coordinator?.status.devices, [])
        XCTAssertNil(rig.send(c, InputPacket(type: .input, dx: 16, dy: 0)), "its packets get no answer any more")
        XCTAssertEqual(rig.poster.log.filter { $0.hasPrefix("move") }, [])
        XCTAssertNil(try rig.connectIfAnswered(), "and it cannot connect again")
    }

    func testRemovalReleasesOnTheCallingThreadEvenWhenTheQueueIsBusy() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey))
        confirmDisplayed(rig)
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.2)
        let c = try rig.connect()
        rig.send(c, InputPacket(type: .input, buttons: [.right], dx: 16, dy: 0))
        let gate = DispatchSemaphore(value: 0)
        rig.runtime.queue.async { gate.wait() }
        rig.runtime.removePairedDevice(rig.linuxKey.publicKey.rawRepresentation)
        XCTAssertEqual(rig.runtime.guardedPoster.buttonsDown, [], "released before the queue got to it")
        XCTAssertTrue(rig.runtime.guardedPoster.isStopped, "and posting stays blocked until the queue finished")
        gate.signal()
        rig.runtime.queue.sync {}
        XCTAssertFalse(rig.runtime.guardedPoster.isStopped)
        XCTAssertEqual(rig.coordinator?.status.devices, [])
    }

    func testRePairingAfterRemovalWorks() throws {
        let rig = RuntimeRig(pairing: true)
        for round in 1...2 {
            advance(rig, 3)
            XCTAssertTrue(rig.coordinator!.open(), "round \(round)")
            let l = try LinuxPairer(key: rig.linuxKey, name: "Desk PC")
            try pair(rig, l)
            confirmDisplayed(rig)
            XCTAssertEqual(rig.coordinator?.status.devices.count, 1, "round \(round)")
            rig.runtime.queue.sync { _ = rig.runtime.removePairedDevice(rig.linuxKey.publicKey.rawRepresentation) }
            rig.runtime.queue.sync {}
            XCTAssertEqual(rig.coordinator?.status.devices, [], "round \(round)")
        }
    }

    // MARK: Confirm must match what is on screen (security audit W3, finding 1)

    /// A click aimed at one attempt must never pair another one: the attempt on screen
    /// was replaced while the person was moving the mouse.
    func testAConfirmForAnAttemptThatIsNoLongerOnScreenPairsNothing() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        advance(rig, 2)
        let first = rig.coordinator!.status
        let staleCode = try XCTUnwrap(first.phaseCode), staleKey = try XCTUnwrap(first.pendingKey)
        // The first attempt is rejected and another device (Mallory) takes its place.
        rig.coordinator?.reject()
        advance(rig, 2)
        let mallory = PrivKey()
        try pair(rig, try LinuxPairer(key: mallory, name: "Desk PC"), from: other)
        advance(rig, 2)
        let shown = rig.coordinator!.status
        XCTAssertNotEqual(shown.pendingKey, staleKey)
        // The old tuple (code and key of attempt 1) must not confirm attempt 2.
        XCTAssertEqual(rig.coordinator?.confirm(code: staleCode, peerKey: staleKey), .staleAttempt)
        // Right code, wrong key; right key, wrong code.
        XCTAssertEqual(rig.coordinator?.confirm(code: try XCTUnwrap(shown.phaseCode), peerKey: staleKey), .staleAttempt)
        XCTAssertEqual(rig.coordinator?.confirm(code: staleCode, peerKey: try XCTUnwrap(shown.pendingKey)), .staleAttempt)
        XCTAssertEqual(rig.coordinator?.status.devices, [], "nothing was paired")
        XCTAssertEqual(try rig.keyStore?.loadPaired(), [])
        XCTAssertEqual(rig.coordinator?.status.notice, .refused(.staleAttempt))
        if case .confirm = rig.coordinator!.status.phase {} else { XCTFail("the attempt is still open for the right click") }
        // The matching tuple works.
        XCTAssertNil(rig.coordinator?.confirm(code: try XCTUnwrap(shown.phaseCode), peerKey: try XCTUnwrap(shown.pendingKey)))
        XCTAssertEqual(rig.coordinator?.status.devices.map(\.key), [mallory.publicKey.rawRepresentation])
    }

    func testAConfirmWithNothingWaitingIsRefused() {
        let rig = RuntimeRig(pairing: true)
        XCTAssertEqual(rig.coordinator?.confirm(code: "123456", peerKey: Data(count: 32)), .noAttempt)
        rig.coordinator?.open()
        XCTAssertEqual(rig.coordinator?.confirm(code: "123456", peerKey: Data(count: 32)), .noAttempt)
        XCTAssertEqual(rig.coordinator?.status.devices, [])
    }

    func testAClickWithinOneSecondOfTheCodeAppearingIsIgnoredAndTheClockRestartsOnANewCode() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        XCTAssertEqual(confirmDisplayed(rig, wait: 0.5), .tooSoon)
        XCTAssertEqual(rig.coordinator?.status.devices, [])
        XCTAssertEqual(rig.coordinator?.status.notice, .refused(.tooSoon))
        XCTAssertNil(confirmDisplayed(rig, wait: 0.6), "1.1 s after the code appeared")
        XCTAssertEqual(rig.coordinator?.status.devices.count, 1)

        // A new code after a long wait starts the second again.
        let rig2 = RuntimeRig(pairing: true)
        rig2.coordinator?.open()
        try pair(rig2, try LinuxPairer(name: "First"))
        advance(rig2, 60)
        rig2.coordinator?.reject()
        advance(rig2, 3)
        try pair(rig2, try LinuxPairer(name: "Second"))
        XCTAssertEqual(confirmDisplayed(rig2, wait: 0.3), .tooSoon, "the new code has been up for 0.3 s, not 60")
    }

    // MARK: a live session blocks pairing (finding 6)

    func testWhileASessionIsLiveNoWindowOpensAndNoConfirmWorks() throws {
        let rig = RuntimeRig(pairing: true)
        // Pair the first device and start its session.
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        XCTAssertNil(confirmDisplayed(rig))
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.2)
        _ = try rig.connect()
        XCTAssertEqual(rig.runtime.state.device, "Desk PC")
        // A click from that session cannot open a second pairing.
        XCTAssertFalse(rig.coordinator!.open())
        XCTAssertEqual(rig.coordinator?.status.notice, .refused(.sessionActive))
        XCTAssertEqual(rig.coordinator?.status.phase, .closed)
        XCTAssertFalse(try pair(rig, try LinuxPairer(name: "Intruder")), "no window, no reply")
    }

    func testAnAlreadyOpenWindowRefusesConfirmOnceASessionStarts() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        XCTAssertNil(confirmDisplayed(rig))
        // Window two, with a second device waiting; then the first device starts a session.
        advance(rig, 3)
        XCTAssertTrue(rig.coordinator!.open())
        let second = PrivKey()
        try pair(rig, try LinuxPairer(key: second, name: "Second PC"), from: other)
        advance(rig, 1.1)  // the code has been up long enough, so only the session can refuse
        rig.runtime.setAllowRawMouse(true)
        rig.run(0.2)
        _ = try rig.connect()
        let shown = rig.coordinator!.status
        XCTAssertEqual(rig.coordinator?.confirm(code: try XCTUnwrap(shown.phaseCode), peerKey: try XCTUnwrap(shown.pendingKey)), .sessionActive)
        XCTAssertEqual(rig.coordinator?.status.devices.count, 1, "the second device was not paired")
    }

    // MARK: look-alike names (finding 5)

    func testALookAlikeOfAPairedDeviceCannotBePaired() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        XCTAssertNil(confirmDisplayed(rig))
        advance(rig, 3)
        rig.coordinator?.open()
        let impostor = PrivKey()
        try pair(rig, try LinuxPairer(key: impostor, name: "Desk \u{0420}\u{0421}"), from: other)  // Cyrillic P and C
        XCTAssertEqual(rig.coordinator?.status.nameCollision, "Desk PC", "the window can say why before the click")
        XCTAssertEqual(confirmDisplayed(rig), .nameCollision("Desk PC"))
        XCTAssertEqual(rig.coordinator?.status.devices.count, 1)
        XCTAssertNotNil(PairingText.notice(rig.coordinator!.status))
        // The same device (same key) coming back with the same name is not a collision.
        rig.coordinator?.reject()
        advance(rig, 3)
        try pair(rig, try LinuxPairer(key: rig.linuxKey, name: "Desk PC"))
        XCTAssertNil(rig.coordinator?.status.nameCollision)
        XCTAssertNil(confirmDisplayed(rig))
    }

    func testRecordsTheStoreIgnoredAreShownToTheOwner() {
        let rig = RuntimeRig(pairing: true)
        rig.keyStore?.ignoredRecords = 2
        XCTAssertEqual(rig.coordinator?.status.ignoredRecords, 2)
        XCTAssertTrue(PairingText.notice(rig.coordinator!.status)!.contains("2 unrecognised"))
    }

    // MARK: man in the middle

    /// Mallory sits between a Linux device and the Mac. He runs one pairing with
    /// each, so the two people see different codes; whichever of them rejects,
    /// nothing is stored. If the Mac's owner were careless, the key stored would
    /// be Mallory's, never the Linux device's: the code check is the only defence,
    /// and it must fail here.
    func testAManInTheMiddleShowsDifferentCodesAndNothingIsStoredOnReject() throws {
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        let malloryKey = PrivKey()
        let realLinux = try LinuxPairer(key: rig.linuxKey, name: "Desk PC")
        // Mallory's Mac-facing leg and Linux-facing leg.
        let towardMac = try LinuxPairer(key: malloryKey, name: "Desk PC")
        let towardLinux = try PairingResponder(staticKey: malloryKey, clock: { 0 })

        // Linux -> (Mallory) m1; Mallory answers with his own m2, and starts his own leg to the Mac.
        let m2L = try XCTUnwrap(try towardLinux.receive(try realLinux.message1()))
        let m2M = try XCTUnwrap(rig.runtime.handle(datagram: try towardMac.message1(), from: phone))
        let (m3L, commitL) = try realLinux.afterMessage2(m2L)
        _ = try towardLinux.receive(m3L)
        let rML = try XCTUnwrap(try towardLinux.receive(commitL))
        _ = try towardLinux.receive(try realLinux.afterResponderNonce(rML))
        let (m3M, commitM) = try towardMac.afterMessage2(m2M)
        _ = rig.runtime.handle(datagram: m3M, from: phone)
        let rMM = try XCTUnwrap(rig.runtime.handle(datagram: commitM, from: phone))
        _ = rig.runtime.handle(datagram: try towardMac.afterResponderNonce(rMM), from: phone)

        let macCode = try XCTUnwrap(rig.coordinator?.status.phaseCode)
        let linuxCode = try XCTUnwrap(realLinux.code)
        XCTAssertNotEqual(macCode, linuxCode, "the two people compare different codes")
        guard case .confirm(_, let who) = rig.coordinator!.status.phase else { return XCTFail() }
        XCTAssertEqual(who, "Desk PC", "Mallory can copy the name, so the name proves nothing")
        rig.coordinator?.reject()
        XCTAssertEqual(rig.coordinator?.status.devices, [])
        XCTAssertEqual(try rig.keyStore?.loadPaired(), [])
        XCTAssertNil(try rig.connectIfAnswered(), "the real device was never paired")
    }

    func testIfOnlyTheLinuxPersonRejectsTheMacOwnerStillSeesTheCodeAndCanReject() throws {
        // No message crosses between the people: the Mac keeps waiting for its owner.
        let rig = RuntimeRig(pairing: true)
        rig.coordinator?.open()
        try pair(rig, try LinuxPairer(key: rig.linuxKey))
        guard case .confirm = rig.coordinator!.status.phase else { return XCTFail() }
        advance(rig, 60)
        guard case .confirm = rig.coordinator!.status.phase else { return XCTFail("a pending code waits for the person") }
        rig.coordinator?.reject()
        XCTAssertEqual(rig.coordinator?.status.devices, [])
    }
}

extension PairingStatus {
    var phaseCode: String? {
        if case .confirm(let c, _) = phase { return c }
        return nil
    }
}
