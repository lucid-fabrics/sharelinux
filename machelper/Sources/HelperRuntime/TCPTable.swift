import CTCPTable
import Darwin
import Foundation
import HelperCore

/// One established TCP connection to a local port, as the kernel table shows it.
public struct TCPConnection: Equatable, Sendable {
    /// The remote address, normalised like the UDP source (mapped IPv4 unwrapped, link-local scope cleared).
    public var peer: PeerAddress
    public var localPort: UInt16
    public var remotePort: UInt16
    /// `so_last_pid` and `so_e_pid` of the socket: the process that last used it
    /// and the one it works for. 0 when the table gave none.
    public var pid: Int32
    public var effectivePid: Int32
    /// The address came from an IPv6 pcb record (the layout of that slot is
    /// verified separately from the IPv4 one).
    public var fromIPv6Record = false
}

/// Byte offsets inside the `pcblist_n` records. `sdk` is computed from the
/// struct mirrors in the `CTCPTable` target (xnu's definitions plus the SDK's
/// `in_addr_4in6`), not typed in. Tests build a wrong layout to prove the
/// self-check notices.
public struct TCPLayout: Equatable, Sendable {
    var inpcbMinLen: Int, fport: Int, lport: Int, vflag: Int, faddr6: Int, faddr4: Int
    var socketMinLen: Int, lastPid: Int, ePid: Int
    var tcpcbMinLen: Int, state: Int
    var established: Int32

    public static let sdk = TCPLayout(
        inpcbMinLen: Int(oss_inpcb_min_len()), fport: Int(oss_inpcb_fport()), lport: Int(oss_inpcb_lport()),
        vflag: Int(oss_inpcb_vflag()), faddr6: Int(oss_inpcb_faddr6()), faddr4: Int(oss_inpcb_faddr4()),
        socketMinLen: Int(oss_socket_min_len()), lastPid: Int(oss_socket_last_pid()),
        ePid: Int(oss_socket_e_pid()), tcpcbMinLen: Int(oss_tcpcb_min_len()), state: Int(oss_tcpcb_state()),
        established: oss_tcps_established())
}

/// Established TCP connections to a local port, read from the kernel's
/// `net.inet.tcp.pcblist_n` table (the data `netstat -anv -p tcp` prints,
/// including the owning pid; readable without root and without any privacy
/// grant).
///
/// The table is a private ABI: an `xinpgen` header (24 bytes) followed by
/// groups of records, each `len(u32) kind(u32) ...`, padded to 8 bytes. Per
/// connection: `xinpcb_n` (kind 0x10), `xsocket_n` (0x1), buffer records and
/// `xtcpcb_n` (0x20). Field offsets come from `TCPLayout.sdk`. Anything that
/// does not fit makes `parse` return nil (unknown), which the gate reads as
/// "no Screen Sharing session": a changed ABI fails closed, never open.
///
/// `read` also runs `selfCheck` once per process: it makes real loopback
/// connections (IPv4 and IPv6), reads them back through this parser and
/// refuses the whole table if an address, port, state or owner pid is not what
/// the kernel was asked for. A layout that moved is therefore denied, not
/// guessed. If IPv6 loopback cannot be set up here, IPv6 peers are ignored.
public enum TCPTable {
    static let kindSocket: UInt32 = 0x1
    static let kindInpcb: UInt32 = 0x10
    static let kindTcpcb: UInt32 = 0x20

    public static func parse(_ data: Data, localPort: UInt16) -> Set<PeerAddress>? {
        parseConnections(data, localPort: localPort, layout: .sdk).map { Set($0.map(\.peer)) }
    }

    public static func parseConnections(_ data: Data, localPort: UInt16, layout: TCPLayout) -> [TCPConnection]? {
        let d = Data(data)  // re-base indices
        func u32(_ i: Int) -> UInt32 {
            UInt32(d[i]) | UInt32(d[i + 1]) << 8 | UInt32(d[i + 2]) << 16 | UInt32(d[i + 3]) << 24
        }
        func u16be(_ i: Int) -> UInt16 { UInt16(d[i]) << 8 | UInt16(d[i + 1]) }
        guard d.count >= 24 else { return nil }
        let header = Int(u32(0))
        guard header >= 24, header <= 64, header <= d.count else { return nil }
        var off = header
        var out: [TCPConnection] = []
        var candidate: TCPConnection?
        while off + 8 <= d.count {
            let len = Int(u32(off)), kind = u32(off + 4)
            // A zero or oversized length means the layout is not what we know.
            guard len >= 8, off + len <= d.count else { return nil }
            if kind == kindInpcb, len >= layout.inpcbMinLen {
                candidate = nil
                let fport = u16be(off + layout.fport)
                let lport = u16be(off + layout.lport)
                let vflag = d[off + layout.vflag]
                if lport == localPort, fport != 0 {
                    var peer: PeerAddress?
                    let v6 = vflag & 2 != 0
                    if v6 {
                        peer = scoped(Array(d[(off + layout.faddr6)..<(off + layout.faddr6 + 16)]))
                    } else if vflag & 1 != 0 {
                        peer = PeerAddress(bytes: Array(d[(off + layout.faddr4)..<(off + layout.faddr4 + 4)]))
                    }
                    if let peer {
                        candidate = TCPConnection(
                            peer: peer, localPort: lport, remotePort: fport, pid: 0, effectivePid: 0,
                            fromIPv6Record: v6)
                    }
                }
            } else if kind == kindSocket, len >= layout.socketMinLen, candidate != nil {
                candidate?.pid = Int32(bitPattern: u32(off + layout.lastPid))
                candidate?.effectivePid = Int32(bitPattern: u32(off + layout.ePid))
            } else if kind == kindTcpcb, len >= layout.tcpcbMinLen, let c = candidate {
                let state = Int32(bitPattern: u32(off + layout.state))
                if state == layout.established { out.append(c) }
                candidate = nil
            }
            off += (len + 7) & ~7
        }
        return out
    }

    /// Reads the live table, after the layout passed the self-check. nil: could
    /// not read, or the layout is not trusted.
    public static func read(localPort: UInt16) -> Set<PeerAddress>? {
        readConnections(localPort: localPort).map { Set($0.map(\.peer)) }
    }

    public static func readConnections(localPort: UInt16) -> [TCPConnection]? {
        guard case .trusted(let v6) = trust(), let raw = readRaw() else { return nil }
        guard let all = parseConnections(raw, localPort: localPort, layout: .sdk) else { return nil }
        return v6 ? all : all.filter { !$0.fromIPv6Record }
    }

    static func readRaw() -> Data? {
        var size = 0
        for _ in 0..<4 {
            guard sysctlbyname("net.inet.tcp.pcblist_n", nil, &size, nil, 0) == 0 else { return nil }
            size += size / 4 + 4096  // the table can grow between the two calls
            var buf = [UInt8](repeating: 0, count: size)
            var got = size
            if sysctlbyname("net.inet.tcp.pcblist_n", &buf, &got, nil, 0) == 0 {
                return Data(buf.prefix(got))
            }
            if errno != ENOMEM { return nil }
        }
        return nil
    }

    // MARK: layout self-check

    public enum Trust: Equatable, Sendable {
        /// The layout reproduced real loopback connections. `ipv6` is false when
        /// IPv6 loopback could not be set up on this Mac (IPv6 peers are then ignored).
        case trusted(ipv6: Bool)
        case untrusted
    }

    private final class TrustCache: @unchecked Sendable {
        let lock = NSLock()
        var value: Trust?
    }
    private static let cache = TrustCache()

    /// The cached verdict for the SDK layout; computed on first use. A check
    /// that could not even run (no sockets, table unreadable) is not cached.
    static func trust() -> Trust {
        cache.lock.lock()
        defer { cache.lock.unlock() }
        if let t = cache.value { return t }
        guard let t = selfCheck(layout: .sdk) else { return .untrusted }
        cache.value = t
        return t
    }

    /// Makes loopback TCP connections and parses the live table with `layout`.
    /// nil: the check could not run. A layout that reads wrong values is `.untrusted`.
    public static func selfCheck(layout: TCPLayout) -> Trust? {
        guard let v4 = probe(family: AF_INET, layout: layout) else { return nil }
        guard v4 else { return .untrusted }
        switch probe(family: AF_INET6, layout: layout) {
        case .none: return .trusted(ipv6: false)  // no IPv6 loopback here
        case .some(true): return .trusted(ipv6: true)
        case .some(false): return .untrusted
        }
    }

    /// true: the layout read the probe connection correctly. false: it did not.
    /// nil: the probe could not be set up.
    private static func probe(family: Int32, layout: TCPLayout) -> Bool? {
        var fds: [Int32] = []
        defer { fds.forEach { close($0) } }
        let listener = socket(family, SOCK_STREAM, 0)
        guard listener >= 0 else { return nil }
        fds.append(listener)
        var storage = sockaddr_storage()
        var length = socklen_t(0)
        let expected: [UInt8]
        if family == AF_INET {
            var a = sockaddr_in()
            a.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
            a.sin_family = sa_family_t(AF_INET)
            a.sin_addr.s_addr = UInt32(0x7f00_0001).bigEndian
            length = socklen_t(MemoryLayout<sockaddr_in>.size)
            withUnsafeBytes(of: &a) { _ = memcpy(&storage, $0.baseAddress!, $0.count) }
            expected = [127, 0, 0, 1]
        } else {
            var a = sockaddr_in6()
            a.sin6_len = UInt8(MemoryLayout<sockaddr_in6>.size)
            a.sin6_family = sa_family_t(AF_INET6)
            a.sin6_addr = in6addr_loopback
            length = socklen_t(MemoryLayout<sockaddr_in6>.size)
            withUnsafeBytes(of: &a) { _ = memcpy(&storage, $0.baseAddress!, $0.count) }
            expected = [UInt8](repeating: 0, count: 15) + [1]
        }
        func sa(_ body: (UnsafePointer<sockaddr>) -> Int32) -> Int32 {
            withUnsafePointer(to: &storage) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { body($0) } }
        }
        guard sa({ Darwin.bind(listener, $0, length) }) == 0, listen(listener, 1) == 0 else { return nil }
        var bound = sockaddr_storage()
        var boundLength = socklen_t(MemoryLayout<sockaddr_storage>.size)
        let named = withUnsafeMutablePointer(to: &bound) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(listener, $0, &boundLength) }
        }
        guard named == 0 else { return nil }
        let port = withUnsafeBytes(of: &bound) { UInt16(bigEndian: $0.load(fromByteOffset: 2, as: UInt16.self)) }
        storage = bound
        let client = socket(family, SOCK_STREAM, 0)
        guard client >= 0 else { return nil }
        fds.append(client)
        guard sa({ Darwin.connect(client, $0, length) }) == 0 else { return nil }
        let accepted = accept(listener, nil, nil)
        guard accepted >= 0 else { return nil }
        fds.append(accepted)
        var clientName = sockaddr_storage()
        var clientLength = socklen_t(MemoryLayout<sockaddr_storage>.size)
        _ = withUnsafeMutablePointer(to: &clientName) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(client, $0, &clientLength) }
        }
        let clientPort = withUnsafeBytes(of: &clientName) { UInt16(bigEndian: $0.load(fromByteOffset: 2, as: UInt16.self)) }

        guard let raw = readRaw() else { return nil }
        guard let found = parseConnections(raw, localPort: port, layout: layout) else { return false }
        let me = getpid()
        guard found.count == 1, let c = found.first else { return false }
        return c.peer.bytes == expected && c.remotePort == clientPort && c.localPort == port
            && c.pid == me
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
