import CryptoKit
import Foundation

public enum PairingError: Error, Equatable {
    case malformed
    case handshakeFailed
    case badDeviceName
    /// The revealed nonce does not match the commitment sent earlier.
    case commitMismatch
    case finished
    /// The pairing window is shut (expired, too many failures, or used).
    case closed
    /// A finished attempt is waiting for the person's decision.
    case busy
    /// A datagram that did not authenticate under this attempt's keys, or that
    /// is out of place for its stage. It is dropped and changes nothing: an
    /// off-path sender can forge these, so they never cost the attempt or the
    /// window anything.
    case unauthenticated
    /// Another source's datagram while an attempt from a different source is
    /// in flight. Dropped, never counted.
    case otherSource
    /// A new attempt asked for before `ChannelLimits.pairingMinStartSpacing`
    /// passed since the last one started.
    case tooSoon
    /// This source has used up its attempt starts for the window
    /// (`ChannelLimits.pairingMaxStartsPerSource`).
    case sourceExhausted
    /// Pairing is locked by the cross-window failure cap (`PairingFailureLedger`).
    case lockedOut
}

/// What the app should tell the person at the Mac about the window.
public enum PairingWindowEvent: Equatable, Sendable {
    /// A datagram from another address arrived while an attempt was in flight
    /// (or waiting for the person): "another device is trying to pair".
    case anotherDeviceTrying
    /// A source asked to start again after its allowed starts were used up.
    case sourceRefused
    /// The failure cap closed the window; pairing is locked for a while.
    case lockedOut
}

/// Plaintext tags of the pairing messages that follow the XX handshake.
/// Each is tag(1) || 32 random bytes, sealed under the XX transport keys.
public enum PairingTag {
    /// Linux -> Mac: H(r_L).
    public static let commit: UInt8 = 0x01
    /// Mac -> Linux: r_M, sent only after the commit arrived.
    public static let responderNonce: UInt8 = 0x02
    /// Linux -> Mac: r_L, sent only after r_M arrived.
    public static let reveal: UInt8 = 0x03
    public static let messageSize = 1 + 32
}

public func randomPairingNonce() -> Data {
    var g = SystemRandomNumberGenerator()
    return Data((0..<PairingCode.nonceSize).map { _ in UInt8.random(in: 0...255, using: &g) })
}

/// One pairing attempt (Noise XX plus commit-before-reveal), Mac side.
///
///   1. L -> M  XX message 1
///   2. M -> L  XX message 2
///   3. L -> M  XX message 3 (payload: device name)
///   4. L -> M  commit   H(r_L)                          (pairing transport)
///   5. M -> L  r_M, fresh random, only after the commit (pairing transport)
///   6. L -> M  r_L; the Mac checks it against the commit
///
/// Only then do `sas`, `deviceName` and `remoteStatic` exist, so the UI cannot
/// show a name or key whose code has not been fixed. The person compares `sas`
/// with the Linux screen; nothing is stored here.
public final class PairingResponder {
    private let hs: HandshakeState
    private let clock: () -> Double
    private let nonceSource: () -> Data
    private var stage = 0  // 0 m1, 1 m3, 2 commit, 3 reveal, 4 done, -1 dead
    private var session: TransportSession?
    private var transcript = Data()
    private var commitment = Data()
    private var responderNonce = Data()
    private var remoteKey: Data?
    private var name: String?
    public private(set) var sas: String?

    // XX msg2 = e(32)+enc(s)(48)+tag(16) = 96. msg3 = enc(s)(48)+payload+tag(16).
    static let message1Size = 32
    static let message3Min = 48 + aeadTagLength
    static let message3Max = message3Min + ChannelLimits.maxDeviceNameBytes

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey,
        clock: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime },
        nonceSource: @escaping () -> Data = randomPairingNonce,
        ephemeralSource: @escaping () -> Curve25519.KeyAgreement.PrivateKey = { .init() }
    ) throws {
        hs = try HandshakeState(
            pattern: .xx, initiator: false, staticKey: staticKey, remoteStatic: nil,
            prologue: nil, ephemeralSource: ephemeralSource)
        self.clock = clock
        self.nonceSource = nonceSource
    }

    public var isComplete: Bool { sas != nil }
    /// The Mac's random value has been sent: from here on the other side holds
    /// what it needs to compute the code, so the attempt is one lottery ticket
    /// whatever happens next.
    var nonceSent: Bool { stage >= 3 }
    /// XX finished with a valid device name: from here on the attempt is a real
    /// pairing try (a code may follow), not stray traffic.
    var peerAuthenticated: Bool { session != nil }
    /// Shown to the person only once the code exists.
    public var remoteStatic: Data? { isComplete ? remoteKey : nil }
    public var deviceName: String? { isComplete ? name : nil }

    /// Feeds one datagram. Returns the reply to send, or nil when there is none.
    /// Any error ends the attempt (the handshake state is poisoned).
    public func receive(_ datagram: Data) throws -> Data? {
        do { return try step(datagram) } catch {
            // A datagram that proved nothing must not end an attempt: anyone
            // who can forge a source address could otherwise kill it.
            if (error as? PairingError) == .unauthenticated { throw error }
            if stage != 4 { stage = -1 }
            throw error
        }
    }

    private func step(_ datagram: Data) throws -> Data? {
        guard let (kind, body) = parseDatagram(datagram) else { throw PairingError.malformed }
        switch (stage, kind) {
        case (0, .xxMessage1):
            guard body.count == PairingResponder.message1Size else { throw PairingError.malformed }
            do {
                _ = try hs.readMessage(body)
                let m2 = try hs.writeMessage(payload: Data())
                stage = 1
                return Data([DatagramKind.xxMessage2.rawValue]) + m2
            } catch { throw PairingError.handshakeFailed }
        case (1, .xxMessage3):
            guard body.count >= PairingResponder.message3Min, body.count <= PairingResponder.message3Max
            else { throw PairingError.malformed }
            let payload: Data
            do { payload = try hs.readMessage(body) } catch { throw PairingError.handshakeFailed }
            guard let validated = PairingResponder.validateDeviceName(payload) else { throw PairingError.badDeviceName }
            do {
                transcript = hs.handshakeHash
                remoteKey = hs.remoteStatic
                session = TransportSession(keys: try hs.split(), now: clock(), kind: .pairingTransport)
            } catch { throw PairingError.handshakeFailed }
            name = validated
            stage = 2
            return nil
        case (2, .pairingTransport):
            let msg = try openMessage(datagram, tag: PairingTag.commit)
            commitment = msg
            responderNonce = nonceSource()
            guard responderNonce.count == PairingCode.nonceSize, let s = session else { throw PairingError.malformed }
            stage = 3
            let sealed = try s.seal(Data([PairingTag.responderNonce]) + responderNonce, now: clock())
            return sealed
        case (3, .pairingTransport):
            let revealed = try openMessage(datagram, tag: PairingTag.reveal)
            guard constantTimeEqual(PairingCode.commitment(of: revealed), commitment) else {
                throw PairingError.commitMismatch
            }
            sas = PairingCode.code(handshakeHash: transcript, initiatorNonce: revealed, responderNonce: responderNonce)
            stage = 4
            return nil
        default:
            // Plain handshake kinds or a transport packet out of stage carry no
            // proof of who sent them.
            throw stage == 4 ? PairingError.finished : PairingError.unauthenticated
        }
    }

    private func openMessage(_ datagram: Data, tag: UInt8) throws -> Data {
        guard let s = session else { throw PairingError.malformed }
        let pt: Data
        // A failure here (bad tag, replayed counter, wrong size) proves nothing
        // about who sent it: dropped, the attempt carries on.
        do { pt = try s.open(datagram, maxPlaintext: PairingTag.messageSize, now: clock()) } catch {
            throw PairingError.unauthenticated
        }
        // Only a packet that authenticated can be at fault below.
        guard pt.count == PairingTag.messageSize, pt[pt.startIndex] == tag else { throw PairingError.malformed }
        return Data(pt.dropFirst())
    }

    /// Characters that render as nothing or look like something else.
    private static let invisible: Set<UInt32> = [
        0x034F, 0x115F, 0x1160, 0x17B4, 0x17B5, 0x2800, 0x3164, 0xFFA0, 0xFFFC, 0xFFFD,
    ]

    /// Device names are shown to the person: valid UTF-8, normalised to NFC, at
    /// most 64 bytes, and made of visible characters only: no control, format
    /// (zero-width, bidi), separator (except one ASCII space between words),
    /// private-use or unassigned characters, and none of the known invisible
    /// fillers. Leading, trailing and doubled spaces are refused. The returned
    /// string is the NFC form, which is what the dialog shows.
    public static func validateDeviceName(_ raw: Data) -> String? {
        guard !raw.isEmpty, raw.count <= ChannelLimits.maxDeviceNameBytes,
            let decoded = String(data: raw, encoding: .utf8)
        else { return nil }
        let s = decoded.precomposedStringWithCanonicalMapping
        guard s.utf8.count <= ChannelLimits.maxDeviceNameBytes else { return nil }
        var previousSpace = true  // also refuses a leading space
        for u in s.unicodeScalars {
            switch u.properties.generalCategory {
            case .control, .format, .lineSeparator, .paragraphSeparator, .unassigned, .privateUse, .surrogate:
                return nil
            case .spaceSeparator:
                guard u.value == 0x20, !previousSpace else { return nil }
                previousSpace = true
                continue
            default: break
            }
            if invisible.contains(u.value) { return nil }
            previousSpace = false
        }
        return previousSpace ? nil : s  // empty after the loop or a trailing space
    }
}

/// The two-minute pairing window the person opens with a click on the Mac.
/// It runs one attempt at a time and closes itself after
/// `ChannelLimits.pairingMaxFailures` failures.
///
/// What a failure is. The moment the Mac sends r_M the other end can compute
/// the code, so an attempt is *charged* from then on: one lottery ticket
/// (1 in 10^6) for a man in the middle, who could otherwise read the code,
/// find it differs from the Linux one and silently start over. Every ending of
/// a charged attempt except the person confirming it counts as a failure:
/// abort, a new message 1, a bad or missing reveal, a timeout, the person
/// saying "codes differ", or the window closing. An attempt that dies before
/// r_M showed nobody anything and is free, except for a protocol error by a
/// peer that completed XX. So the attacker sees at most three codes per window.
///
/// Who may count. Datagrams carry no proof of their source address, so an
/// off-path sender can forge them; the window therefore binds each attempt to
/// the source of its message 1 and drops everything else for that attempt
/// without counting it. Within the attempt only a datagram that authenticates
/// under the XX transport keys can fail it: a forged or garbled packet is
/// dropped. A forger who copies the victim's own address can still restart or
/// stall an attempt that has not reached r_M (limited by the start spacing, the
/// attempt timeout and `pairingMaxStartsPerSource` starts per window) but can
/// never charge it: once r_M is out, a new message 1 from that source is dropped
/// until the attempt finishes or times out.
public final class PairingWindow {
    private let staticKey: Curve25519.KeyAgreement.PrivateKey
    private let clock: () -> Double
    private let nonceSource: () -> Data
    private let ephemeralSource: () -> Curve25519.KeyAgreement.PrivateKey
    private let openedAt: Double
    private var current: PairingResponder?
    private var currentSource: PeerAddress?
    private var currentStartedAt = 0.0
    private var charged = false
    private var lastStart: Double?
    private var shut = false
    private var startsBySource: [PeerAddress?: Int] = [:]
    private let ledger: PairingFailureLedger?
    public private(set) var failures = 0
    /// Called for the events above. Throttle on the app side: a hostile host
    /// can fire it as often as it can send datagrams.
    public var onEvent: ((PairingWindowEvent) -> Void)?

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey, clock: @escaping () -> Double,
        nonceSource: @escaping () -> Data = randomPairingNonce,
        ephemeralSource: @escaping () -> Curve25519.KeyAgreement.PrivateKey = { .init() },
        ledger: PairingFailureLedger? = nil
    ) {
        self.staticKey = staticKey; self.clock = clock; self.nonceSource = nonceSource
        self.ephemeralSource = ephemeralSource
        self.ledger = ledger
        openedAt = clock()
        // Locked out by earlier windows: born shut, the app shows the cooldown.
        if ledger?.isLockedOut == true { shut = true }
    }

    /// Seconds of cooldown left from the cross-window cap, nil when none.
    public var lockoutRemaining: Double? { ledger?.lockoutRemaining }

    public var isOpen: Bool {
        let now = clock()
        return !shut && now >= openedAt && now - openedAt < ChannelLimits.pairingWindow
    }

    /// The attempt waiting for the person's decision, if its code exists.
    public var pending: PairingResponder? { current?.isComplete == true ? current : nil }

    public func close() {
        settleCurrent()
        shut = true
        current = nil
    }

    /// - Parameter source: where the datagram came from. Pass the real UDP
    ///   source; nil only means "unknown" and is treated as one fixed source.
    public func receive(_ datagram: Data, from source: PeerAddress? = nil) throws -> Data? {
        guard isOpen else { close(); throw lockedOutOrClosed() }
        retireStaleAttempt()
        guard isOpen else { throw lockedOutOrClosed() }
        if current?.isComplete == true {
            if source != currentSource { onEvent?(.anotherDeviceTrying) }
            throw PairingError.busy
        }
        // Another source cannot touch an attempt in flight, not even to count it.
        if current != nil, source != currentSource {
            onEvent?(.anotherDeviceTrying)
            throw PairingError.otherSource
        }
        if let (kind, _) = parseDatagram(datagram), kind == .xxMessage1 {
            let now = clock()
            // Once r_M is out the attempt is charged and the source is fixed: a
            // new message 1 (forgeable from the victim's own address) must not
            // end it early and charge the honest device. It ends by finishing
            // or by the attempt timeout, which has already run above.
            if current != nil, charged { throw PairingError.unauthenticated }
            // A source gets a few starts per window, no more: restarting for
            // free every second would keep an honest device out for the window.
            if startsBySource[source, default: 0] >= ChannelLimits.pairingMaxStartsPerSource {
                onEvent?(.sourceRefused)
                throw PairingError.sourceExhausted
            }
            if let last = lastStart, now >= last, now - last < ChannelLimits.pairingMinStartSpacing {
                throw PairingError.tooSoon
            }
            // Starting over abandons the old attempt: free before r_M, a failure after it.
            settleCurrent()
            guard isOpen else { throw lockedOutOrClosed() }
            startsBySource[source, default: 0] += 1
            current = try PairingResponder(
                staticKey: staticKey, clock: clock, nonceSource: nonceSource, ephemeralSource: ephemeralSource)
            currentSource = source
            currentStartedAt = now
            lastStart = now
            charged = false
        }
        guard let attempt = current else { throw PairingError.malformed }
        do {
            let reply = try attempt.receive(datagram)
            if attempt.nonceSent { charged = true }
            return reply
        } catch PairingError.unauthenticated {
            throw PairingError.unauthenticated
        } catch {
            let counts = charged || attempt.peerAuthenticated
            current = nil
            charged = false
            if counts { fail() }
            throw error
        }
    }

    /// The person says the codes differ (or declines). Counts as a failure.
    public func reject() {
        current = nil
        charged = false
        fail()
    }

    /// The person says the codes match. Hands out the paired device and closes the
    /// window: one pairing per click. The only ending that does not count.
    public func confirm() -> (name: String, key: Data)? {
        guard isOpen, let a = current, let n = a.deviceName, let k = a.remoteStatic else { return nil }
        charged = false
        shut = true
        current = nil
        return (n, k)
    }

    /// Drops an attempt that stopped making progress. Charged ones count.
    private func retireStaleAttempt() {
        guard let a = current, !a.isComplete else { return }
        let age = clock() - currentStartedAt
        if age >= ChannelLimits.pairingAttemptTimeout || age < 0 { settleCurrent() }
    }

    /// Ends the current attempt without the person's confirm.
    private func settleCurrent() {
        guard current != nil else { return }
        let wasCharged = charged
        current = nil
        charged = false
        if wasCharged { fail() }
    }

    private func fail() {
        failures += 1
        ledger?.recordFailure()
        if ledger?.isLockedOut == true { onEvent?(.lockedOut) }
        if failures >= ChannelLimits.pairingMaxFailures || ledger?.isLockedOut == true {
            shut = true; current = nil
        }
    }

    private func lockedOutOrClosed() -> PairingError {
        ledger?.isLockedOut == true ? .lockedOut : .closed
    }
}
