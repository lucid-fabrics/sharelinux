import Foundation
import HelperCore

/// Established TCP connections to a local port, read from the kernel's
/// `net.inet.tcp.pcblist_n` table (the data `netstat -an -p tcp` prints;
/// readable without root and without any privacy grant).
///
/// The table is a private ABI. Layout verified on macOS 27 (and unchanged for
/// many releases): an `xinpgen` header (24 bytes) followed by groups of
/// records, each `len(u32) kind(u32) ...`, padded to 8 bytes. Per connection:
/// `xinpcb_n` (kind 0x10, 104 bytes) then socket/buffer records and `xtcpcb_n`
/// (kind 0x20). From `xinpcb_n`: foreign port at 16, local port at 18 (both
/// big endian), `inp_vflag` at 44 (1 = IPv4, 2 = IPv6), foreign address at 52
/// (IPv4 in bytes 8..12 of the 16-byte field, IPv6 in all 16), and from
/// `xtcpcb_n` `t_state` at 36 (4 = ESTABLISHED). Anything that does not fit
/// the layout makes `parse` return nil (unknown), which the gate reads as "no
/// Screen Sharing session": a changed ABI fails closed, never open.
public enum TCPTable {
    static let established: Int32 = 4
    static let kindInpcb: UInt32 = 0x10
    static let kindTcpcb: UInt32 = 0x20

    public static func parse(_ data: Data, localPort: UInt16) -> Set<PeerAddress>? {
        let d = Data(data)  // re-base indices
        func u32(_ i: Int) -> UInt32 {
            UInt32(d[i]) | UInt32(d[i + 1]) << 8 | UInt32(d[i + 2]) << 16 | UInt32(d[i + 3]) << 24
        }
        guard d.count >= 24 else { return nil }
        let header = Int(u32(0))
        guard header >= 24, header <= 64, header <= d.count else { return nil }
        var off = header
        var out = Set<PeerAddress>()
        var candidate: PeerAddress?
        while off + 8 <= d.count {
            let len = Int(u32(off)), kind = u32(off + 4)
            // A zero or oversized length means the layout is not what we know.
            guard len >= 8, off + len <= d.count else { return nil }
            if kind == kindInpcb, len >= 84 {
                candidate = nil
                let fport = UInt16(d[off + 16]) << 8 | UInt16(d[off + 17])
                let lport = UInt16(d[off + 18]) << 8 | UInt16(d[off + 19])
                let vflag = d[off + 44]
                if lport == localPort, fport != 0 {
                    if vflag & 2 != 0 {
                        candidate = scoped(Array(d[(off + 52)..<(off + 68)]))
                    } else if vflag & 1 != 0 {
                        candidate = PeerAddress(bytes: Array(d[(off + 60)..<(off + 64)]))
                    }
                }
            } else if kind == kindTcpcb, len >= 40, let c = candidate {
                let state = Int32(bitPattern: u32(off + 36))
                if state == established { out.insert(c) }
                candidate = nil
            }
            off += (len + 7) & ~7
        }
        return out
    }

    /// Reads the live table.
    public static func read(localPort: UInt16) -> Set<PeerAddress>? {
        var size = 0
        for _ in 0..<4 {
            guard sysctlbyname("net.inet.tcp.pcblist_n", nil, &size, nil, 0) == 0 else { return nil }
            size += size / 4 + 4096  // the table can grow between the two calls
            var buf = [UInt8](repeating: 0, count: size)
            var got = size
            if sysctlbyname("net.inet.tcp.pcblist_n", &buf, &got, nil, 0) == 0 {
                return parse(Data(buf.prefix(got)), localPort: localPort)
            }
            if errno != ENOMEM { return nil }
        }
        return nil
    }

    /// The kernel embeds the interface scope in bytes 2-3 of link-local
    /// addresses (fe80:xxxx::); `recvfrom` does not. Clear it so both agree.
    static func scoped(_ bytes: [UInt8]) -> PeerAddress? {
        PeerAddress(bytes: clearLinkLocalScope(bytes))
    }
}

/// fe80::/10 addresses carry the interface index in bytes 2-3 inside the kernel.
public func clearLinkLocalScope(_ b: [UInt8]) -> [UInt8] {
    guard b.count == 16, b[0] == 0xfe, b[1] & 0xc0 == 0x80 else { return b }
    var c = b
    c[2] = 0; c[3] = 0
    return c
}
