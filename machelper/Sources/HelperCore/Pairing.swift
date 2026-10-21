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
    /// passed since the last one from the same source (or before the global
    /// `pairingMinGlobalStartSpacing` passed since any start).
    case tooSoon
    /// Every attempt slot is held by a source that already answered message 2.
    case tooManyAttempts
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
/// It closes itself after `ChannelLimits.pairingMaxFailures` failures.
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
/// dropped.
///
/// Slots. Before r_M several attempts from different sources run side by side
/// (`pairingMaxConcurrentAttempts`, at most one per source). An off-path
/// spoofer cannot finish XX (message 2 goes to the address it forged), so its
/// attempts stay *unconfirmed*; when the slots are full the oldest unconfirmed
/// attempt makes room for a new one, and every attempt that has not reached
/// r_M dies after `pairingUnchargedTimeout`. An honest device therefore gets a
/// slot no matter how many spoofed message 1s arrive, unless thousands per
/// second push it out in the few milliseconds before its own message 3. The
/// first attempt to reach r_M becomes the only one: the others are dropped for
/// free, and from then on a new message 1 from anyone is refused until it
/// finishes or times out. A forger copying the victim's own address can still
/// restart that victim's attempt before r_M (limited per source by
/// `pairingMinStartSpacing` and `pairingMaxStartsPerSource`) but can never
/// charge it.
public final class PairingWindow {
    private struct Slot {
        var responder: PairingResponder
        var startedAt: Double
        /// r_M has been sent: the other end can compute the code.
        var charged = false
        /// Waiting for the person, or past r_M: the only attempt left.
        var isLeader: Bool { charged || responder.isComplete }
    }

    private let staticKey: Curve25519.KeyAgreement.PrivateKey
    private let clock: () -> Double
    private let nonceSource: () -> Data
    private let ephemeralSource: () -> Curve25519.KeyAgreement.PrivateKey
    private let openedAt: Double
    private var slots: [PeerAddress?: Slot] = [:]
    private var lastStart: Double?
    private var lastStartBySource: [PeerAddress?: Double] = [:]
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

    /// Attempts currently in flight (before r_M, or the one leader).
    var attemptsInFlight: Int { slots.count }
    var trackedSourceCount: Int { max(startsBySource.count, lastStartBySource.count) }

    private var leader: (source: PeerAddress?, slot: Slot)? {
        slots.first { $0.value.isLeader }.map { ($0.key, $0.value) }
    }

    /// The attempt waiting for the person's decision, if its code exists.
    public var pending: PairingResponder? {
        guard let l = leader, l.slot.responder.isComplete else { return nil }
        return l.slot.responder
    }

    public func close() {
        settleAll()
        shut = true
        slots = [:]
    }

    /// - Parameter source: where the datagram came from. Pass the real UDP
    ///   source; nil only means "unknown" and is treated as one fixed source.
    public func receive(_ datagram: Data, from source: PeerAddress? = nil) throws -> Data? {
        guard isOpen else { close(); throw lockedOutOrClosed() }
        retireStaleAttempts()
        guard isOpen else { throw lockedOutOrClosed() }
        let isMessage1 = parseDatagram(datagram).map { $0.0 == .xxMessage1 } ?? false

        if let l = leader {
            if l.slot.responder.isComplete {
                if source != l.source { onEvent?(.anotherDeviceTrying) }
                throw PairingError.busy
            }
            // Past r_M the attempt is charged and its source is fixed: a new
            // message 1 (forgeable from the victim's own address) must not end
            // it early and charge the honest device. It ends by finishing or by
            // the attempt timeout, which has already run above.
            if source != l.source {
                onEvent?(.anotherDeviceTrying)
                throw PairingError.otherSource
            }
            if isMessage1 { throw PairingError.unauthenticated }
        } else if isMessage1 {
            try startAttempt(from: source)
        } else if slots[source] == nil {
            // Not part of any attempt in flight: another device, or stray junk.
            guard !slots.isEmpty else { throw PairingError.malformed }
            onEvent?(.anotherDeviceTrying)
            throw PairingError.otherSource
        }

        guard var slot = slots[source] else { throw PairingError.malformed }
        do {
            let reply = try slot.responder.receive(datagram)
            if slot.responder.nonceSent {
                slot.charged = true
                slots = [source: slot]  // the others die for free; this one is the attempt now
            } else {
                slots[source] = slot
            }
            return reply
        } catch PairingError.unauthenticated {
            throw PairingError.unauthenticated
        } catch {
            let counts = slot.charged || slot.responder.peerAuthenticated
            slots[source] = nil
            if counts { fail() }
            throw error
        }
    }

    /// Message 1 from `source` with no leader: gives it a slot (or refuses).
    ///
    /// Accepted residual: a LAN host that forges message 1 from many addresses
    /// can fill the slots and delay an honest device for as long as it keeps up
    /// (about 8 starts inside the honest round trip to message 3). It can
    /// neither pair nor charge a failure, the window is local-only, opened by
    /// the owner and ends after 2 minutes, and the owner sees "another device
    /// is trying to pair". A cookie round trip before the Diffie-Hellman would
    /// need a new datagram kind on both sides; not worth it for a denial that
    /// the owner can see and that costs nothing but a retry.
    private func startAttempt(from source: PeerAddress?) throws {
        let now = clock()
        // A source gets a few starts per window, no more: restarting for
        // free every second would keep an honest device out for the window.
        if startsBySource[source, default: 0] >= ChannelLimits.pairingMaxStartsPerSource {
            onEvent?(.sourceRefused)
            throw PairingError.sourceExhausted
        }
        if let last = lastStartBySource[source], now >= last, now - last < ChannelLimits.pairingMinStartSpacing {
            throw PairingError.tooSoon
        }
        if let last = lastStart, now >= last, now - last < ChannelLimits.pairingMinGlobalStartSpacing {
            throw PairingError.tooSoon
        }
        // Starting over abandons this source's own earlier attempt (free before r_M).
        slots[source] = nil
        if slots.count >= ChannelLimits.pairingMaxConcurrentAttempts {
            // Room is made by the oldest attempt whose source never answered
            // message 2. If every slot belongs to a source that did, refuse.
            guard
                let victim = slots.filter({ !$0.value.responder.peerAuthenticated })
                    .min(by: { $0.value.startedAt < $1.value.startedAt })?.key
            else {
                onEvent?(.anotherDeviceTrying)
                throw PairingError.tooManyAttempts
            }
            slots[victim] = nil
        }
        if !slots.isEmpty { onEvent?(.anotherDeviceTrying) }
        let responder = try PairingResponder(
            staticKey: staticKey, clock: clock, nonceSource: nonceSource, ephemeralSource: ephemeralSource)
        startsBySource[source, default: 0] += 1
        if startsBySource.count > ChannelLimits.pairingMaxTrackedSources, let any = startsBySource.keys.first(where: { $0 != source }) {
            startsBySource[any] = nil
        }
        lastStartBySource[source] = now
        if lastStartBySource.count > ChannelLimits.pairingMaxTrackedSources {
            lastStartBySource = lastStartBySource.filter { now < $0.value || now - $0.value < ChannelLimits.pairingMinStartSpacing }
        }
        lastStart = now
        slots[source] = Slot(responder: responder, startedAt: now)
    }

    /// The person says the codes differ (or declines). Counts as a failure.
    public func reject() {
        if let l = leader { slots[l.source] = nil }
        fail()
    }

    /// The person says the codes match. Hands out the paired device and closes the
    /// window: one pairing per click. The only ending that does not count.
    public func confirm() -> (name: String, key: Data)? {
        guard isOpen, let l = leader, l.slot.responder.isComplete,
            let n = l.slot.responder.deviceName, let k = l.slot.responder.remoteStatic
        else { return nil }
        shut = true
        slots = [:]
        return (n, k)
    }

    /// Drops attempts that stopped making progress. An attempt that has not
    /// reached r_M dies after the short `pairingUnchargedTimeout`, for free;
    /// a charged one lives `pairingAttemptTimeout` and then counts as a failure.
    /// One waiting for the person has no timeout (the window's own end closes it).
    private func retireStaleAttempts() {
        let now = clock()
        for (key, slot) in slots where !slot.responder.isComplete {
            let age = now - slot.startedAt
            if slot.charged {
                if age >= ChannelLimits.pairingAttemptTimeout || age < 0 { slots[key] = nil; fail() }
            } else if age >= ChannelLimits.pairingUnchargedTimeout || age < 0 {
                slots[key] = nil
            }
        }
    }

    /// Ends every attempt without the person's confirm (the window closes).
    private func settleAll() {
        let charged = slots.values.filter(\.charged).count
        slots = [:]
        for _ in 0..<charged { fail() }
    }

    private func fail() {
        failures += 1
        ledger?.recordFailure()
        if ledger?.isLockedOut == true { onEvent?(.lockedOut) }
        if failures >= ChannelLimits.pairingMaxFailures || ledger?.isLockedOut == true {
            shut = true; slots = [:]
        }
    }

    private func lockedOutOrClosed() -> PairingError {
        ledger?.isLockedOut == true ? .lockedOut : .closed
    }
}
