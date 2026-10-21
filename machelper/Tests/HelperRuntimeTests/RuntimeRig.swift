import CryptoKit
import XCTest

@testable import HelperCore
@testable import HelperRuntime

typealias PrivKey = Curve25519.KeyAgreement.PrivateKey

/// Records what would have been posted. Nothing here touches the window server.
final class RecordingPoster: EventPoster {
    var log: [String] = []
    func postMove(dx: Int, dy: Int, held: ButtonMask) { log.append("move \(dx) \(dy) held=\(held.rawValue)") }
    func postButton(_ button: MouseButton, down: Bool, clickState: Int) {
        log.append(down ? "down \(button.rawValue) click=\(clickState)" : "up \(button.rawValue)")
    }
    func postScroll(dx: Int, dy: Int) { log.append("scroll \(dx) \(dy)") }

    var ups: Int { log.filter { $0.hasPrefix("up") }.count }
    var downs: Int { log.filter { $0.hasPrefix("down") }.count }
    var moves: Int { log.filter { $0.hasPrefix("move") }.count }
}

final class FakeSessionSource: SessionStateSource {
    var console: UInt32? = 501
    var locked: Bool? = false
    func consoleUID() -> UInt32? { console }
    func screenLocked() -> Bool? { locked }
}

final class FakeSharingSource: ScreenSharingPeerSource {
    var current: Set<PeerAddress>? = [RuntimeRig.addr]
    func peers() -> Set<PeerAddress>? { current }
}

final class FakeAccessSource: AccessSource {
    var isGranted = true
    func granted() -> Bool { isGranted }
}

/// A HelperRuntime with every outside world replaced by a fake, and a Linux
/// client written from the protocol (public building blocks only).
final class RuntimeRig {
    static let addr = PeerAddress(bytes: [192, 0, 2, 1])!
    static let otherAddr = PeerAddress(bytes: [192, 0, 2, 9])!

    let macKey = PrivKey()
    let linuxKey = PrivKey()
    let poster = RecordingPoster()
    let session = FakeSessionSource()
    let sharing = FakeSharingSource()
    let access = FakeAccessSource()
    var mono = 1000.0
    var wall = Date(timeIntervalSince1970: 1_800_000_000)
    var paired: [Data]
    var runtime: HelperRuntime!
    var counter = 0
    var states: [RuntimeState] = []

    init() {
        paired = [linuxKey.publicKey.rawRepresentation]
        runtime = HelperRuntime(
            staticKey: macKey, pairedKeys: { [unowned self] in self.paired }, poster: poster,
            session: session, sharing: sharing, access: access, ownUID: 501,
            clock: ChannelClock(monotonic: { [unowned self] in self.mono }, wall: { [unowned self] in self.wall }),
            timestamps: MemoryTimestampStore())
        runtime.onState = { [unowned self] in self.states.append($0) }
    }

    /// Advances time in 50 ms steps, sampling the environment and running the watchdogs like the timer does.
    func run(_ seconds: Double, sampling: Bool = true) {
        var left = seconds
        while left > 1e-9 {
            let step = min(0.05, left)
            mono += step; wall = wall.addingTimeInterval(step); left -= step
            if sampling { runtime.refreshEnvironment() }
            runtime.tick()
        }
    }

    struct Client {
        var session: TransportSession
    }

    /// Handshake from the paired Linux key, then one keepalive so the session goes live.
    func connect(key: PrivKey? = nil, from source: PeerAddress = RuntimeRig.addr) throws -> Client {
        wall = wall.addingTimeInterval(1)  // every initiation newer than the last
        let hs = try HandshakeState(
            pattern: .ik, initiator: true, staticKey: key ?? linuxKey,
            remoteStatic: macKey.publicKey.rawRepresentation)
        let m = try hs.writeMessage(payload: TAI64N.encode(wall))
        let dg = Mac1.append(
            to: Data([DatagramKind.ikInitiation.rawValue]) + m, macPublic: macKey.publicKey.rawRepresentation)
        let reply = try XCTUnwrap(runtime.handle(datagram: dg, from: source), "handshake not answered")
        _ = try hs.readMessage(reply.dropFirst())
        let client = Client(session: TransportSession(keys: try hs.split(), now: mono))
        _ = send(client, InputPacket(type: .keepalive), from: source)
        return client
    }

    @discardableResult
    func send(_ c: Client, _ p: InputPacket, from source: PeerAddress = RuntimeRig.addr) -> Data? {
        counter += 1
        let dg = try! c.session.seal(p.encode(), now: mono)
        return runtime.handle(datagram: dg, from: source)
    }

    /// Sends the same packet again after clearing the poster log; returns what was posted.
    func posted(after block: () -> Void) -> [String] {
        poster.log.removeAll()
        block()
        return poster.log
    }

    func status(_ reply: Data?, _ c: Client) -> HelperState? {
        guard let reply, let pt = try? c.session.open(reply, maxPlaintext: 2, now: mono),
            let s = try? StatusPacket.decode(pt)
        else { return nil }
        return s.state
    }
}
