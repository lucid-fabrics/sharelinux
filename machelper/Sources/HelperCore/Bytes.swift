import Foundation

/// Compares two byte strings without an early exit on the first difference.
/// Differing lengths are unequal (lengths are not secret here).
public func constantTimeEqual(_ a: Data, _ b: Data) -> Bool {
    if a.count != b.count { return false }
    var diff: UInt8 = 0
    for (x, y) in zip(a, b) { diff |= x ^ y }
    return diff == 0
}

extension Data {
    /// Overwrites the bytes in place (the buffer is made unique first, so a
    /// shared copy elsewhere is left alone). Best effort: the optimiser cannot
    /// remove `memset_s`, but Swift may have made other copies we cannot reach.
    public mutating func wipe() {
        withUnsafeMutableBytes { raw in
            if let base = raw.baseAddress, raw.count > 0 { _ = memset_s(base, raw.count, 0, raw.count) }
        }
    }
}

func isAllZero(_ a: Data) -> Bool {
    var acc: UInt8 = 0
    for x in a { acc |= x }
    return acc == 0
}

extension Data {
    func le16(at i: Int) -> UInt16 {
        let s = startIndex + i
        return UInt16(self[s]) | UInt16(self[s + 1]) << 8
    }
    func le32(at i: Int) -> UInt32 {
        let s = startIndex + i
        var v: UInt32 = 0
        for k in 0..<4 { v |= UInt32(self[s + k]) << (8 * UInt32(k)) }
        return v
    }
    func le64(at i: Int) -> UInt64 {
        let s = startIndex + i
        var v: UInt64 = 0
        for k in 0..<8 { v |= UInt64(self[s + k]) << (8 * UInt64(k)) }
        return v
    }
    mutating func appendLE16(_ v: UInt16) { append(UInt8(v & 0xff)); append(UInt8(v >> 8)) }
    mutating func appendLE32(_ v: UInt32) { for k in 0..<4 { append(UInt8((v >> (8 * UInt32(k))) & 0xff)) } }
    mutating func appendLE64(_ v: UInt64) { for k in 0..<8 { append(UInt8((v >> (8 * UInt64(k))) & 0xff)) } }

    public var hex: String { map { String(format: "%02x", $0) }.joined() }

    public init?(hex: String) {
        let chars = Array(hex.utf8)
        guard chars.count % 2 == 0 else { return nil }
        var out = Data(capacity: chars.count / 2)
        func nib(_ c: UInt8) -> UInt8? {
            switch c {
            case 48...57: return c - 48
            case 97...102: return c - 87
            case 65...70: return c - 55
            default: return nil
            }
        }
        var i = 0
        while i < chars.count {
            guard let h = nib(chars[i]), let l = nib(chars[i + 1]) else { return nil }
            out.append(h << 4 | l)
            i += 2
        }
        self = out
    }
}
