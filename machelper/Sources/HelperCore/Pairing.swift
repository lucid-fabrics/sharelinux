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
            throw stage == 4 ? PairingError.finished : PairingError.malformed
        }
    }

    private func openMessage(_ datagram: Data, tag: UInt8) throws -> Data {
        guard let s = session else { throw PairingError.malformed }
        let pt: Data
        do { pt = try s.open(datagram, maxPlaintext: PairingTag.messageSize, now: clock()) } catch {
            throw PairingError.malformed
        }
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
/// It runs one attempt at a time, counts failures and closes itself after
/// `ChannelLimits.pairingMaxFailures` of them, because each attempt that gets
/// as far as a finished XX handshake is one lottery ticket for a man in the
/// middle (1 in 10^6). A failure is a protocol error or bad commitment after
/// that point, or the person saying the codes differ. An attempt that dies
/// earlier never showed a code and is free, so stray datagrams cannot lock the
/// window.
public final class PairingWindow {
    private let staticKey: Curve25519.KeyAgreement.PrivateKey
    private let clock: () -> Double
    private let nonceSource: () -> Data
    private let ephemeralSource: () -> Curve25519.KeyAgreement.PrivateKey
    private let openedAt: Double
    private var current: PairingResponder?
    private var shut = false
    public private(set) var failures = 0

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey, clock: @escaping () -> Double,
        nonceSource: @escaping () -> Data = randomPairingNonce,
        ephemeralSource: @escaping () -> Curve25519.KeyAgreement.PrivateKey = { .init() }
    ) {
        self.staticKey = staticKey; self.clock = clock; self.nonceSource = nonceSource
        self.ephemeralSource = ephemeralSource
        openedAt = clock()
    }

    public var isOpen: Bool {
        let now = clock()
        return !shut && now >= openedAt && now - openedAt < ChannelLimits.pairingWindow
    }

    /// The attempt waiting for the person's decision, if its code exists.
    public var pending: PairingResponder? { current?.isComplete == true ? current : nil }

    public func close() { shut = true; current = nil }

    public func receive(_ datagram: Data) throws -> Data? {
        guard isOpen else { close(); throw PairingError.closed }
        if current?.isComplete == true { throw PairingError.busy }
        if let (kind, _) = parseDatagram(datagram), kind == .xxMessage1 {
            current = try PairingResponder(
                staticKey: staticKey, clock: clock, nonceSource: nonceSource, ephemeralSource: ephemeralSource)
        }
        guard let attempt = current else { throw PairingError.malformed }
        do { return try attempt.receive(datagram) } catch {
            current = nil
            if attempt.peerAuthenticated { fail() }
            throw error
        }
    }

    /// The person says the codes differ (or declines). Counts as a failure.
    public func reject() { current = nil; fail() }

    /// The person says the codes match. Hands out the paired device and closes the
    /// window: one pairing per click.
    public func confirm() -> (name: String, key: Data)? {
        guard isOpen, let a = current, let n = a.deviceName, let k = a.remoteStatic else { return nil }
        close()
        return (n, k)
    }

    private func fail() {
        failures += 1
        if failures >= ChannelLimits.pairingMaxFailures { close() }
    }
}
