import Darwin
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// Environment sources with injected data: no Accessibility, no window server.
final class SourcesTests: XCTestCase {
    // MARK: Screen Sharing peers from the kernel table

    /// Builds the part of an `xinpcb_n` + `xtcpcb_n` pair the parser reads.
    func connection(
        fport: UInt16, lport: UInt16, vflag: UInt8, foreign: [UInt8], state: Int32, extraRecords: Bool = true
    ) -> Data {
        var inpcb = Data(count: 104)
        inpcb.putLE32(104, at: 0); inpcb.putLE32(0x10, at: 4)
        inpcb[16] = UInt8(fport >> 8); inpcb[17] = UInt8(fport & 0xff)
        inpcb[18] = UInt8(lport >> 8); inpcb[19] = UInt8(lport & 0xff)
        inpcb[44] = vflag
        if foreign.count == 4 {
            inpcb.replaceSubrange(60..<64, with: foreign)
        } else {
            inpcb.replaceSubrange(52..<68, with: foreign)
        }
        var out = inpcb
        if extraRecords {
            var sock = Data(count: 104)  // a socket record in between, which the parser must skip
            sock.putLE32(104, at: 0); sock.putLE32(0x1, at: 4)
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

    /// The real kernel table on this Mac, with a connection made right here on
    /// loopback. Reading the table needs no privilege and posts nothing.
    func testTheLiveKernelTableShowsALoopbackConnection() throws {
        let listener = socket(AF_INET, SOCK_STREAM, 0)
        defer { close(listener) }
        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_addr.s_addr = UInt32(0x7f00_0001).bigEndian
        let bound = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(listener, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        XCTAssertEqual(bound, 0)
        XCTAssertEqual(listen(listener, 1), 0)
        var got = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        _ = withUnsafeMutablePointer(to: &got) { $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(listener, $0, &len) } }
        let port = UInt16(bigEndian: got.sin_port)

        let client = socket(AF_INET, SOCK_STREAM, 0)
        defer { close(client) }
        let connected = withUnsafePointer(to: &got) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(client, $0, socklen_t(MemoryLayout<sockaddr_in>.size)) }
        }
        XCTAssertEqual(connected, 0)
        let accepted = accept(listener, nil, nil)
        defer { close(accepted) }

        let peers = try XCTUnwrap(TCPTable.read(localPort: port), "the kernel table could not be read or parsed")
        XCTAssertEqual(peers, [PeerAddress(bytes: [127, 0, 0, 1])!])
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
