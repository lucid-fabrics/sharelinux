import Foundation

/// Wire format of the helper channel. Everything is fixed size or length-checked
/// before use, integers are little endian, and anything unexpected is rejected.
///
/// UDP datagram:
///   handshake  kind(1) || noise message          kinds 0x01/0x02 (IK), 0x10/0x11/0x12 (XX)
///   transport  0x03 || counter(8, LE) || AEAD(plaintext) || tag(16)
/// The 64-bit counter is the Noise nonce and the replay-window sequence number.
public enum DatagramKind: UInt8, Sendable {
    case ikInitiation = 0x01
    case ikResponse = 0x02
    case transport = 0x03
    case xxMessage1 = 0x10
    case xxMessage2 = 0x11
    case xxMessage3 = 0x12
}

public enum WireLimits {
    /// Largest datagram of any kind we are willing to look at.
    public static let maxDatagram = 512
    public static let transportOverhead = 1 + 8 + aeadTagLength
    /// Per-packet clamps (design 3.4). dx/dy travel as i16 in points*16, so the
    /// representable maximum (32767) is below the design's 4000*16 and wins.
    public static let maxMotion: Int16 = 32767
    public static let maxScroll: Int16 = 2000
}

public enum WireError: Error, Equatable {
    case badLength
    case badType
    case reservedBitsSet
    case nonZeroPadding
    case unexpectedFields
    case badState
}

public enum PacketType: UInt8, Sendable {
    case input = 1
    case keepalive = 2
    case bye = 3
}

public struct ButtonMask: OptionSet, Sendable {
    public let rawValue: UInt8
    public init(rawValue: UInt8) { self.rawValue = rawValue }
    public static let left = ButtonMask(rawValue: 1 << 0)
    public static let right = ButtonMask(rawValue: 1 << 1)
    public static let middle = ButtonMask(rawValue: 1 << 2)
    public static let back = ButtonMask(rawValue: 1 << 3)
    public static let forward = ButtonMask(rawValue: 1 << 4)
    static let valid: UInt8 = 0b0001_1111
}

/// Linux -> Mac, 20 bytes inside the Noise transport.
///   0 type, 1 buttons, 2 dx(i16), 4 dy(i16), 6 sx(i16), 8 sy(i16),
///   10 seqLocal(u32), 14 flags, 15..19 zero padding
public struct InputPacket: Equatable, Sendable {
    public static let size = 20
    public static let flagPointerLocked: UInt8 = 1

    public var type: PacketType
    public var buttons: ButtonMask
    public var dx: Int16
    public var dy: Int16
    public var sx: Int16
    public var sy: Int16
    public var seqLocal: UInt32
    public var flags: UInt8

    public init(
        type: PacketType, buttons: ButtonMask = [], dx: Int16 = 0, dy: Int16 = 0,
        sx: Int16 = 0, sy: Int16 = 0, seqLocal: UInt32 = 0, flags: UInt8 = 0
    ) {
        self.type = type; self.buttons = buttons; self.dx = dx; self.dy = dy
        self.sx = sx; self.sy = sy; self.seqLocal = seqLocal; self.flags = flags
    }

    public var pointerLocked: Bool { flags & InputPacket.flagPointerLocked != 0 }

    public func encode() -> Data {
        var d = Data(capacity: InputPacket.size)
        d.append(type.rawValue)
        d.append(buttons.rawValue)
        d.appendLE16(UInt16(bitPattern: dx))
        d.appendLE16(UInt16(bitPattern: dy))
        d.appendLE16(UInt16(bitPattern: sx))
        d.appendLE16(UInt16(bitPattern: sy))
        d.appendLE32(seqLocal)
        d.append(flags)
        d.append(Data(count: 5))
        return d
    }

    public static func decode(_ raw: Data) throws -> InputPacket {
        guard raw.count == size else { throw WireError.badLength }
        let d = Data(raw)  // re-base indices
        guard let type = PacketType(rawValue: d[0]) else { throw WireError.badType }
        guard d[1] & ~ButtonMask.valid == 0 else { throw WireError.reservedBitsSet }
        guard d[14] & ~flagPointerLocked == 0 else { throw WireError.reservedBitsSet }
        guard d[15..<20].allSatisfy({ $0 == 0 }) else { throw WireError.nonZeroPadding }
        let p = InputPacket(
            type: type, buttons: ButtonMask(rawValue: d[1]),
            dx: Int16(bitPattern: d.le16(at: 2)), dy: Int16(bitPattern: d.le16(at: 4)),
            sx: Int16(bitPattern: d.le16(at: 6)), sy: Int16(bitPattern: d.le16(at: 8)),
            seqLocal: d.le32(at: 10), flags: d[14])
        if type != .input, !(p.buttons.isEmpty && p.dx == 0 && p.dy == 0 && p.sx == 0 && p.sy == 0) {
            throw WireError.unexpectedFields
        }
        return p
    }

    /// Applies the per-packet clamps of design 3.4.
    public func clamped() -> InputPacket {
        func c(_ v: Int16, _ m: Int16) -> Int16 { Swift.max(-m, Swift.min(m, v)) }
        var p = self
        p.dx = c(dx, WireLimits.maxMotion)
        p.dy = c(dy, WireLimits.maxMotion)
        p.sx = c(sx, WireLimits.maxScroll)
        p.sy = c(sy, WireLimits.maxScroll)
        return p
    }
}

/// Why the helper does or does not act on input (design 3.2). Raw values are wire values.
public enum HelperState: UInt8, Sendable, CaseIterable {
    case active = 0
    case disabled = 1
    case locked = 2
    case noSession = 3
    case notConsole = 4
    case rateLimited = 5
    case needsGrant = 6
}

/// Mac -> Linux, 2 bytes inside the Noise transport.
public struct StatusPacket: Equatable, Sendable {
    public static let size = 2
    public static let typeStatus: UInt8 = 10
    public var state: HelperState
    public init(state: HelperState) { self.state = state }

    public func encode() -> Data { Data([StatusPacket.typeStatus, state.rawValue]) }

    public static func decode(_ raw: Data) throws -> StatusPacket {
        guard raw.count == size else { throw WireError.badLength }
        let d = Data(raw)
        guard d[0] == typeStatus else { throw WireError.badType }
        guard let s = HelperState(rawValue: d[1]) else { throw WireError.badState }
        return StatusPacket(state: s)
    }
}

/// Splits a datagram into kind and body after length checks. Never throws on
/// garbage, just returns nil, so callers drop silently.
public func parseDatagram(_ raw: Data) -> (kind: DatagramKind, body: Data)? {
    guard raw.count >= 1, raw.count <= WireLimits.maxDatagram else { return nil }
    let d = Data(raw)
    guard let kind = DatagramKind(rawValue: d[0]) else { return nil }
    return (kind, Data(d.dropFirst()))
}
