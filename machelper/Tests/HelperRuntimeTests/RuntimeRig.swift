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
    /// Set when the rig was made with `pairing: true`: the pairing flow with an
    /// in-memory key store (nobody paired at first) and a failure ledger.
    var coordinator: PairingCoordinator?
    var keyStore: MemoryKeyStore?
    var ledgerStore = MemoryPairingFailureStore()
    var statuses: [PairingStatus] = []
    var keyWatch: KeyWatch?
    var keysChangedAlerts = 0

    init(pairing: Bool = false, watchKeys: Bool = false, defaults: UserDefaults? = nil,
         prepairedName: String? = nil) {
        paired = pairing ? [] : [linuxKey.publicKey.rawRepresentation]
        if pairing {
            let store = MemoryKeyStore(
                identity: macKey,
                paired: prepairedName.map { [PairedDevice(name: $0, key: linuxKey.publicKey.rawRepresentation)] } ?? [])
            keyStore = store
            let ledger = PairingFailureLedger(store: ledgerStore, wall: { [unowned self] in self.wall.timeIntervalSince1970 })
            coordinator = try! PairingCoordinator(
                staticKey: macKey, store: store, ledger: ledger, monotonic: { [unowned self] in self.mono })
            coordinator?.onChange = { [unowned self] in self.statuses.append($0) }
        }
        if watchKeys {
            let store = keyStore ?? MemoryKeyStore(identity: macKey)
            keyStore = store
            keyWatch = try! KeyWatch(store: store, ownKey: macKey.publicKey.rawRepresentation, defaults: defaults)
        }
        let coordinator = self.coordinator
        runtime = HelperRuntime(
            staticKey: macKey,
            pairedKeys: { [unowned self] in coordinator?.pairedKeys ?? self.paired }, poster: poster,
            session: session, sharing: sharing, access: access, ownUID: 501,
            clock: ChannelClock(monotonic: { [unowned self] in self.mono }, wall: { [unowned self] in self.wall }),
            timestamps: MemoryTimestampStore(),
            deviceName: { coordinator?.name(for: $0) ?? HelperRuntime.fingerprintName($0) }, pairing: coordinator,
            keyWatch: keyWatch)
        runtime.onKeysChanged = { [unowned self] in self.keysChangedAlerts += 1 }
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

    /// Like `connect` but nil when the Mac does not answer the handshake (an
    /// unpaired key), without recording a failure.
    func connectIfAnswered(key: PrivKey? = nil, from source: PeerAddress = RuntimeRig.addr) throws -> Client? {
        wall = wall.addingTimeInterval(1)
        let hs = try HandshakeState(
            pattern: .ik, initiator: true, staticKey: key ?? linuxKey,
            remoteStatic: macKey.publicKey.rawRepresentation)
        let m = try hs.writeMessage(payload: TAI64N.encode(wall))
        let dg = Mac1.append(
            to: Data([DatagramKind.ikInitiation.rawValue]) + m, macPublic: macKey.publicKey.rawRepresentation)
        guard let reply = runtime.handle(datagram: dg, from: source) else { return nil }
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
