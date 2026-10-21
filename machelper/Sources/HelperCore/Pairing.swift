import CryptoKit
import Foundation

public enum PairingError: Error, Equatable {
    case malformed
    case handshakeFailed
    case badDeviceName
    case finished
}

/// One pairing attempt (Noise XX), Mac side. The app creates it only when the
/// person clicked "Pair new device..." and discards it when the 2-minute window
/// ends or the attempt finishes. After message 3 the person compares `sas` with
/// the one on the Linux screen; nothing is stored here.
public final class PairingResponder {
    private let hs: HandshakeState
    private var stage = 0
    public private(set) var remoteStatic: Data?
    public private(set) var deviceName: String?
    public private(set) var sas: String?

    // XX msg2 = e(32)+enc(s)(48)+tag(16) = 96. msg3 = enc(s)(48)+payload+tag(16).
    static let message1Size = 32
    static let message3Min = 48 + aeadTagLength
    static let message3Max = message3Min + ChannelLimits.maxDeviceNameBytes

    public init(
        staticKey: Curve25519.KeyAgreement.PrivateKey,
        ephemeralSource: @escaping () -> Curve25519.KeyAgreement.PrivateKey = { .init() }
    ) throws {
        hs = try HandshakeState(
            pattern: .xx, initiator: false, staticKey: staticKey, remoteStatic: nil,
            prologue: Data(), ephemeralSource: ephemeralSource)
    }

    public var isComplete: Bool { sas != nil }

    /// Feeds one datagram. Returns the reply to send, or nil when there is none.
    /// Any error ends the attempt (the handshake state is poisoned).
    public func receive(_ datagram: Data) throws -> Data? {
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
            guard let name = PairingResponder.validateDeviceName(payload) else { throw PairingError.badDeviceName }
            stage = 2
            remoteStatic = hs.remoteStatic
            deviceName = name
            sas = SAS.derive(handshakeHash: hs.handshakeHash)
            return nil
        default:
            throw stage == 2 ? PairingError.finished : PairingError.malformed
        }
    }

    /// Device names are shown to the person: valid UTF-8, 1...64 bytes, no control
    /// or invisible-formatting characters (they could fake what the dialog says).
    public static func validateDeviceName(_ raw: Data) -> String? {
        guard !raw.isEmpty, raw.count <= ChannelLimits.maxDeviceNameBytes,
            let s = String(data: raw, encoding: .utf8)
        else { return nil }
        for u in s.unicodeScalars {
            switch u.properties.generalCategory {
            case .control, .format, .lineSeparator, .paragraphSeparator, .unassigned, .privateUse, .surrogate:
                return nil
            default: break
            }
        }
        return s.trimmingCharacters(in: .whitespaces).isEmpty ? nil : s
    }
}
