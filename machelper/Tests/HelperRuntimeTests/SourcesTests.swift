import Darwin
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// Environment sources with injected data: no Accessibility, no window server.
final class SourcesTests: XCTestCase {
    // MARK: Screen Sharing peers from the kernel table

    /// Builds the part of an `xinpcb_n` + `xtcpcb_n` pair the parser reads.
    func connection(
        fport: UInt16, lport: UInt16, vflag: UInt8, foreign: [UInt8], state: Int32, extraRecords: Bool = true,
        pid: Int32 = 0
    ) -> Data {
        var inpcb = Data(count: 104)
        inpcb.putLE32(104, at: 0); inpcb.putLE32(0x10, at: 4)
        inpcb[16] = UInt8(fport >> 8); inpcb[17] = UInt8(fport & 0xff)
        inpcb[18] = UInt8(lport >> 8); inpcb[19] = UInt8(lport & 0xff)
        inpcb[44] = vflag
        if foreign.count == 4 {
            inpcb.replaceSubrange(60..<64, with: foreign)
        } else {
            inpcb.replaceSubrange(48..<64, with: foreign)  // inp6_foreign, as dumped from a live ::1 connection
        }
        var out = inpcb
        if extraRecords {
            var sock = Data(count: 104)  // a socket record in between, which the parser must skip
            sock.putLE32(104, at: 0); sock.putLE32(0x1, at: 4)
            sock.putLE32(UInt32(bitPattern: pid), at: 68)  // so_last_pid
            out += sock
        }
        var tcp = Data(count: 208)
        tcp.putLE32(204, at: 0); tcp.putLE32(0x20, at: 4)
        tcp.putLE32(UInt32(bitPattern: state), at: 36)
        return out + tcp
    }

    func table(_ conns: [Data]) -> Data {
        var h = Data(count: 24)
        h.putLE32(24, at: 0)
        h.putLE32(UInt32(conns.count), at: 4)
        return conns.reduce(h) { $0 + $1 }
    }

    func testOnlyEstablishedConnectionsToThePortCount() {
        let linux: [UInt8] = [192, 168, 1, 50]
        let data = table([
            connection(fport: 40000, lport: 5900, vflag: 1, foreign: linux, state: 4),
            connection(fport: 40001, lport: 5900, vflag: 1, foreign: [10, 0, 0, 9], state: 6),  // TIME_WAIT
            connection(fport: 40002, lport: 22, vflag: 1, foreign: [10, 0, 0, 8], state: 4),  // other port
            connection(fport: 0, lport: 5900, vflag: 1, foreign: [0, 0, 0, 0], state: 1),  // listener
        ])
        XCTAssertEqual(TCPTable.parse(data, localPort: 5900), [PeerAddress(bytes: linux)!])
    }

    func testIPv6AndMappedAddressesNormaliseLikeTheUDPSource() {
        var v6 = [UInt8](repeating: 0, count: 16)
        v6[0] = 0x20; v6[1] = 0x01; v6[15] = 7
        var mapped = [UInt8](repeating: 0, count: 16)
        mapped[10] = 0xff; mapped[11] = 0xff; mapped[12] = 192; mapped[13] = 168; mapped[14] = 1; mapped[15] = 50
        var linkLocal = [UInt8](repeating: 0, count: 16)
        linkLocal[0] = 0xfe; linkLocal[1] = 0x80; linkLocal[2] = 0; linkLocal[3] = 4  // kernel-embedded scope
        linkLocal[15] = 9
        let peers = TCPTable.parse(
            table([
                connection(fport: 1, lport: 5900, vflag: 2, foreign: v6, state: 4),
                connection(fport: 2, lport: 5900, vflag: 2, foreign: mapped, state: 4),
                connection(fport: 3, lport: 5900, vflag: 2, foreign: linkLocal, state: 4),
            ]), localPort: 5900)
        var userland = linkLocal
        userland[3] = 0  // what recvfrom reports
        XCTAssertEqual(
            peers, [PeerAddress(bytes: v6)!, PeerAddress(bytes: [192, 168, 1, 50])!, PeerAddress(bytes: userland)!])
    }

    func testAnUnknownLayoutIsNotMistakenForAnEmptyTable() {
        XCTAssertNil(TCPTable.parse(Data(), localPort: 5900))
        XCTAssertNil(TCPTable.parse(Data(count: 10), localPort: 5900))
        var bad = table([connection(fport: 1, lport: 5900, vflag: 1, foreign: [1, 2, 3, 4], state: 4)])
        bad.putLE32(0, at: 24)  // a record with length 0
        XCTAssertNil(TCPTable.parse(bad, localPort: 5900))
        var huge = table([connection(fport: 1, lport: 5900, vflag: 1, foreign: [1, 2, 3, 4], state: 4)])
        huge.putLE32(1 << 30, at: 24)  // a record longer than the data
        XCTAssertNil(TCPTable.parse(huge, localPort: 5900))
        var header = table([])
        header.putLE32(9000, at: 0)
        XCTAssertNil(TCPTable.parse(header, localPort: 5900))
    }

    func testGarbageNeverCrashesTheParser() {
        var rng = SystemRandomNumberGenerator()
        for _ in 0..<500 {
            let n = Int.random(in: 0..<600, using: &rng)
            var d = Data((0..<n).map { _ in UInt8.random(in: 0...255, using: &rng) })
            if d.count >= 4 { d.putLE32(24, at: 0) }
            _ = TCPTable.parse(d, localPort: 5900)
        }
    }

    /// A connected loopback TCP pair on `family`, kept open until `close()`.
    final class Loopback {
        let fds: [Int32]
        let port: UInt16
        let clientPort: UInt16
        init?(family: Int32) {
            let listener = socket(family, SOCK_STREAM, 0)
            guard listener >= 0 else { return nil }
            var ss = sockaddr_storage()
            var len = socklen_t(0)
            if family == AF_INET {
                var a = sockaddr_in()
                a.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
                a.sin_family = sa_family_t(AF_INET)
                a.sin_addr.s_addr = UInt32(0x7f00_0001).bigEndian
                len = socklen_t(MemoryLayout<sockaddr_in>.size)
                withUnsafeBytes(of: &a) { _ = memcpy(&ss, $0.baseAddress!, $0.count) }
            } else {
                var a = sockaddr_in6()
                a.sin6_len = UInt8(MemoryLayout<sockaddr_in6>.size)
                a.sin6_family = sa_family_t(AF_INET6)
                a.sin6_addr = in6addr_loopback
                len = socklen_t(MemoryLayout<sockaddr_in6>.size)
                withUnsafeBytes(of: &a) { _ = memcpy(&ss, $0.baseAddress!, $0.count) }
            }
            let bound = withUnsafePointer(to: &ss) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(listener, $0, len) }
            }
            guard bound == 0, listen(listener, 1) == 0 else { close(listener); return nil }
            var got = sockaddr_storage()
            var gotLen = socklen_t(MemoryLayout<sockaddr_storage>.size)
            _ = withUnsafeMutablePointer(to: &got) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(listener, $0, &gotLen) }
            }
            port = withUnsafeBytes(of: &got) { UInt16(bigEndian: $0.load(fromByteOffset: 2, as: UInt16.self)) }
            let client = socket(family, SOCK_STREAM, 0)
            let connected = withUnsafePointer(to: &got) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(client, $0, len) }
            }
            guard client >= 0, connected == 0 else { close(listener); return nil }
            let accepted = accept(listener, nil, nil)
            var mine = sockaddr_storage()
            var mineLen = socklen_t(MemoryLayout<sockaddr_storage>.size)
            _ = withUnsafeMutablePointer(to: &mine) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(client, $0, &mineLen) }
            }
            clientPort = withUnsafeBytes(of: &mine) { UInt16(bigEndian: $0.load(fromByteOffset: 2, as: UInt16.self)) }
            fds = [listener, client, accepted]
        }
        deinit { fds.forEach { close($0) } }
    }

    /// The real kernel table on this Mac, with a connection made right here on
    /// loopback. Reading the table needs no privilege and posts nothing.
    func testTheLiveKernelTableShowsAnIPv4LoopbackConnection() throws {
        let lo = try XCTUnwrap(Loopback(family: AF_INET))
        let peers = try XCTUnwrap(TCPTable.read(localPort: lo.port), "the kernel table could not be read or parsed")
        XCTAssertEqual(peers, [PeerAddress(bytes: [127, 0, 0, 1])!])
    }

    /// Regression for the IPv6 foreign address read at 52..68 instead of 48..64:
    /// that read ::1 as 0:0:0:100::, so no real IPv6 Screen Sharing peer ever matched.
    func testTheLiveKernelTableShowsAnIPv6LoopbackConnectionWithItsRealAddress() throws {
        guard let lo = Loopback(family: AF_INET6) else { throw XCTSkip("no IPv6 loopback on this machine") }
        let peers = try XCTUnwrap(TCPTable.read(localPort: lo.port), "the kernel table could not be read or parsed")
        XCTAssertEqual(peers, [PeerAddress(bytes: [UInt8](repeating: 0, count: 15) + [1])!])
    }

    func testTheLiveTableNamesTheOwningProcessAndPorts() throws {
        let lo = try XCTUnwrap(Loopback(family: AF_INET))
        let conns = try XCTUnwrap(TCPTable.readConnections(localPort: lo.port))
        XCTAssertEqual(conns.count, 1)
        XCTAssertEqual(conns.first?.pid, getpid())
        XCTAssertEqual(conns.first?.remotePort, lo.clientPort)
    }

    // MARK: layout derivation and self-check

    func testOffsetsComeFromTheKernelStructsAndMatchTheDumpedLayout() {
        let l = TCPLayout.sdk
        XCTAssertEqual([l.fport, l.lport, l.vflag, l.faddr6, l.faddr4], [16, 18, 44, 48, 60])
        XCTAssertEqual(l.faddr4 - l.faddr6, 12, "IPv4 sits in the last 4 bytes of the 16-byte slot (in_addr_4in6)")
        XCTAssertEqual([l.lastPid, l.ePid, l.state], [68, 72, 36])
    }

    func testTheSelfCheckAcceptsTheRealLayout() throws {
        let trust = try XCTUnwrap(TCPTable.selfCheck(layout: .sdk))
        guard case .trusted = trust else { return XCTFail("\(trust)") }
    }

    /// The old IPv6 offset (52) must be noticed by the self-check and the table
    /// then denied as a whole, not silently mis-read.
    func testTheSelfCheckDeniesAWrongIPv6Offset() throws {
        try XCTSkipIf(Loopback(family: AF_INET6) == nil, "no IPv6 loopback on this machine")
        var wrong = TCPLayout.sdk
        wrong.faddr6 = 52
        XCTAssertEqual(TCPTable.selfCheck(layout: wrong), .untrusted)
    }

    func testTheSelfCheckDeniesAWrongIPv4OrPidOffset() {
        var v4 = TCPLayout.sdk
        v4.faddr4 = 56
        XCTAssertEqual(TCPTable.selfCheck(layout: v4), .untrusted)
        var pid = TCPLayout.sdk
        pid.lastPid = 64  // the uid slot
        XCTAssertEqual(TCPTable.selfCheck(layout: pid), .untrusted)
        var state = TCPLayout.sdk
        state.state = 40
        XCTAssertEqual(TCPTable.selfCheck(layout: state), .untrusted)
    }

    /// The source accepts a socket on its effective pid too, so the self-check
    /// has to prove that slot: pointing it at the uid slot (a number that is
    /// neither this pid nor 0) must be refused.
    func testTheSelfCheckDeniesAWrongEffectivePidOffset() {
        var epid = TCPLayout.sdk
        epid.ePid = epid.lastPid - 4  // the uid slot
        XCTAssertEqual(TCPTable.selfCheck(layout: epid), .untrusted)
    }

    // MARK: who owns the socket on the Screen Sharing port

    func conn(_ last: UInt8, pid: Int32, epid: Int32 = 0) -> TCPConnection {
        TCPConnection(
            peer: PeerAddress(bytes: [10, 0, 0, last])!, localPort: 5900, remotePort: 50000 + UInt16(last), pid: pid,
            effectivePid: epid)
    }

    /// Anyone on this Mac may listen on 5900 while Screen Sharing is off, and any
    /// LAN host may connect to it: only a socket owned by Apple's server counts.
    func testOnlyConnectionsOwnedByTheScreenSharingServerCountAsPeers() {
        var table: [TCPConnection]? = [
            conn(1, pid: 100), conn(2, pid: 200), conn(3, pid: 0), conn(4, pid: -5), conn(5, pid: 200, epid: 100),
        ]
        let source = SystemScreenSharingSource(connections: { _ in table }, isServer: { $0 == 100 })
        XCTAssertEqual(source.peers(), [PeerAddress(bytes: [10, 0, 0, 1])!, PeerAddress(bytes: [10, 0, 0, 5])!])
        table = [conn(2, pid: 200)]
        XCTAssertEqual(source.peers(), [], "a stranger's listener on the port yields no peers")
        table = nil
        XCTAssertNil(source.peers(), "an unreadable table stays unknown")
    }

    func testTheOwnerVerdictIsCachedBrieflyThenChecked_Again() {
        var now = 0.0
        var asked: [Int32] = []
        let source = SystemScreenSharingSource(
            connections: { _ in [self.conn(1, pid: 100)] }, isServer: { asked.append($0); return true }, clock: { now })
        for _ in 0..<5 { _ = source.peers() }
        XCTAssertEqual(asked, [100])
        now += SystemScreenSharingSource.verdictLifetime + 0.1
        _ = source.peers()
        XCTAssertEqual(asked, [100, 100])
    }

    func testTheCodeSigningCheckByPid() {
        // launchd is Apple-signed with this identifier; it is not Screen Sharing.
        XCTAssertTrue(ScreenSharingServer.isRunning(pid: 1, requirement: "anchor apple and identifier \"com.apple.xpc.launchd\""))
        XCTAssertFalse(ScreenSharingServer.isRunning(pid: 1))
        XCTAssertFalse(ScreenSharingServer.isRunning(pid: getpid()), "this unsigned test process is not Apple's server")
        for bad: Int32 in [0, -1, 2_000_000_000] { XCTAssertFalse(ScreenSharingServer.isRunning(pid: bad)) }
        XCTAssertFalse(ScreenSharingServer.isRunning(pid: 1, requirement: "this is not a requirement"))
    }

    /// The per-user agent may own the established socket: it is on the allowlist,
    /// and the real Apple binaries satisfy the requirement while another
    /// Apple-signed binary or our own unsigned one does not.
    func testTheAppleScreenSharingBinariesSatisfyTheRequirement() throws {
        let base = "/System/Library/CoreServices/RemoteManagement/"
        let agent = base + "ScreensharingAgent.bundle"
        let daemon = base + "screensharingd.bundle"
        try XCTSkipUnless(FileManager.default.fileExists(atPath: agent), "no Screen Sharing on this macOS")
        XCTAssertTrue(ScreenSharingServer.requirement.contains("com.apple.screensharing.agent"))
        XCTAssertTrue(ScreenSharingServer.matches(path: agent), "the per-user agent is allowed")
        XCTAssertTrue(ScreenSharingServer.matches(path: daemon))
        XCTAssertFalse(ScreenSharingServer.matches(path: "/usr/bin/true"), "Apple-signed but not Screen Sharing")
        XCTAssertFalse(ScreenSharingServer.matches(path: CommandLine.arguments[0]), "unsigned or ad-hoc code")
        XCTAssertFalse(ScreenSharingServer.matches(path: "/nonexistent"))
    }

    /// A program of ours listening on a port and connecting to itself is the
    /// stand-in for an attacker's listener on 5900.
    func testALiveLoopbackListenerOfAnotherProgramIsNotScreenSharing() throws {
        let lo = try XCTUnwrap(Loopback(family: AF_INET))
        let source = SystemScreenSharingSource(port: lo.port)
        XCTAssertEqual(source.peers(), [])
        let anyOwner = SystemScreenSharingSource(port: lo.port, isServer: { _ in true })
        XCTAssertEqual(anyOwner.peers(), [PeerAddress(bytes: [127, 0, 0, 1])!], "only the owner check filtered it out")
    }

    // MARK: console and lock

    func testConsoleAndLockFromTheSessionDictionary() {
        var dict: [String: Any]? = ["kCGSSessionOnConsoleKey": 1, "kCGSSessionUserIDKey": 501]
        var now = 10.0
        let source = SystemSessionSource(clock: { now }, dictionary: { dict })
        XCTAssertEqual(source.consoleUID(), 501)
        XCTAssertEqual(source.screenLocked(), false)

        dict?["CGSSessionScreenIsLocked"] = 1
        XCTAssertEqual(source.screenLocked(), true)

        dict = ["kCGSSessionOnConsoleKey": 0, "kCGSSessionUserIDKey": 501]  // another user is on the console
        XCTAssertNil(source.consoleUID())

        dict = nil  // no dictionary at all: unknown, which the gate treats as locked
        XCTAssertNil(source.consoleUID())
        XCTAssertNil(source.screenLocked())

        dict = ["kCGSSessionOnConsoleKey": 1, "kCGSSessionUserIDKey": 501]
        XCTAssertEqual(source.screenLocked(), false)
        // The lock notification wins for two seconds even if the dictionary lags.
        source.noteLockNotification()
        XCTAssertEqual(source.screenLocked(), true)
        now += SystemSessionSource.lockHintSeconds - 0.1
        XCTAssertEqual(source.screenLocked(), true)
        now += 0.2
        XCTAssertEqual(source.screenLocked(), false)
        source.noteLockNotification()
        source.noteUnlockNotification()
        XCTAssertEqual(source.screenLocked(), false)
    }

    /// The lock hint is written by the notification thread and read on the
    /// runtime queue. Under Thread Sanitizer (swift test --sanitize=thread) an
    /// unlocked hint is reported as a data race here.
    func testLockHintIsSafeAcrossThreads() {
        let source = SystemSessionSource(
            clock: { ProcessInfo.processInfo.systemUptime },
            dictionary: { ["kCGSSessionOnConsoleKey": 1, "kCGSSessionUserIDKey": 501] })
        let group = DispatchGroup()
        for t in 0..<4 {
            DispatchQueue.global().async(group: group) {
                for _ in 0..<2000 {
                    if t % 2 == 0 { source.noteLockNotification(); source.noteUnlockNotification() } else {
                        _ = source.screenLocked()
                    }
                }
            }
        }
        group.wait()
        source.noteUnlockNotification()
        XCTAssertEqual(source.screenLocked(), false)
    }

    // MARK: environment monitor

    final class Session: SessionStateSource {
        var console: UInt32? = 501
        var locked: Bool? = false
        func consoleUID() -> UInt32? { console }
        func screenLocked() -> Bool? { locked }
    }
    final class Sharing: ScreenSharingPeerSource {
        var set: Set<PeerAddress>? = []
        func peers() -> Set<PeerAddress>? { set }
    }
    final class Access: AccessSource {
        var yes = true
        func granted() -> Bool { yes }
    }

    func testMonitorStampsEverySampleAndFailsClosedWhenSourcesAreUnknown() {
        let session = Session(), sharing = Sharing(), access = Access()
        var now = 5.0
        let monitor = EnvironmentMonitor(
            session: session, sharing: sharing, access: access, ownUID: 501, clock: { now })
        let peer = PeerAddress(bytes: [1, 2, 3, 4])!
        // Never sampled: no timestamp, the gate says locked.
        var inputs = monitor.gateInputs(udpPeer: peer, toggleOn: true)
        XCTAssertNil(inputs.snapshotAt)
        inputs.peerPaired = true
        XCTAssertEqual(Gate.evaluate(inputs, now: now), .deny(.locked))

        sharing.set = [peer]
        monitor.refresh()
        inputs = monitor.gateInputs(udpPeer: peer, toggleOn: true)
        inputs.peerPaired = true
        XCTAssertEqual(inputs.snapshotAt, 5.0)
        XCTAssertEqual(Gate.evaluate(inputs, now: now), .accept)

        now += 0.6  // an old sample is refused
        XCTAssertEqual(Gate.evaluate(inputs, now: now), .deny(.locked))

        for (name, change): (String, () -> Void) in [
            ("console", { session.console = nil }), ("lock", { session.locked = nil }),
            ("sharing", { sharing.set = nil }), ("access", { access.yes = false }),
        ] {
            let m = EnvironmentMonitor(session: session, sharing: sharing, access: access, ownUID: 501, clock: { now })
            sharing.set = [peer]; session.console = 501; session.locked = false; access.yes = true
            change()
            m.refresh()
            var i = m.gateInputs(udpPeer: peer, toggleOn: true)
            i.peerPaired = true
            XCTAssertNotEqual(Gate.evaluate(i, now: now), .accept, name)
        }
    }

    // MARK: identity files

    func testIdentityKeyIsPrivateStableAndNeverAccepted_WhenTruncated() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = IdentityStore(directory: dir)
        let key = try store.loadOrCreateKey()
        let attrs = { (u: URL) in (try? FileManager.default.attributesOfItem(atPath: u.path))?[.posixPermissions] as? Int }
        XCTAssertEqual(attrs(dir), 0o700)
        XCTAssertEqual(attrs(dir.appendingPathComponent("identity.key")), 0o600)
        XCTAssertEqual(try store.loadOrCreateKey().rawRepresentation, key.rawRepresentation)
        XCTAssertEqual(try store.loadPaired(), [])
        // A damaged key file is an error, never a silently new identity.
        try Data(count: 5).write(to: dir.appendingPathComponent("identity.key"))
        XCTAssertThrowsError(try store.loadOrCreateKey())
        // Paired entries with a bad key are ignored.
        try store.savePaired([.init(name: "ok", key: String(repeating: "ab", count: 32)), .init(name: "bad", key: "zz")])
        XCTAssertEqual(try store.loadPaired().map(\.name), ["ok"])
    }
}

extension Data {
    mutating func putLE32(_ v: UInt32, at i: Int) {
        for k in 0..<4 { self[i + k] = UInt8((v >> (8 * UInt32(k))) & 0xff) }
    }
}
