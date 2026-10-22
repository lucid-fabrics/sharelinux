import AppKit
import CryptoKit
import HelperCore
import HelperRuntime
import PairingUI

// Renders the pairing window in each of its states to PNG files, offscreen.
// Every state is what a real PairingCoordinator reports: the coordinator is
// driven with a Linux side written here from the protocol (a real handshake,
// a real code) and the window is given only the coordinator's own status. No
// view model is built by hand. Nothing is shown on screen, no Keychain or
// key store is touched (memory only), no permission is asked for. Not shipped.
//
// usage: pairingpreview <output directory>

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: pairingpreview <output directory>\n".utf8))
    exit(2)
}
let dir = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)

let app = NSApplication.shared
app.setActivationPolicy(.prohibited)

typealias PrivKey = Curve25519.KeyAgreement.PrivateKey
func privKey(_ seed: UInt8) -> PrivKey {
    try! PrivKey(rawRepresentation: Data((0..<32).map { seed &+ UInt8($0) &* 7 }))
}
func pub(_ seed: UInt8) -> Data { privKey(seed).publicKey.rawRepresentation }

/// The Linux side of pairing, from the protocol description.
final class LinuxSide {
    let hs: HandshakeState
    let nonce = randomPairingNonce()
    let name: String
    private var session: TransportSession?
    private var transcript = Data()
    init(key: PrivKey, name: String) throws {
        hs = try HandshakeState(pattern: .xx, initiator: true, staticKey: key)
        self.name = name
    }
    func message1() throws -> Data { Data([DatagramKind.xxMessage1.rawValue]) + (try hs.writeMessage()) }
    func afterMessage2(_ m2: Data) throws -> (m3: Data, commit: Data) {
        _ = try hs.readMessage(m2.dropFirst())
        let m3 = Data([DatagramKind.xxMessage3.rawValue]) + (try hs.writeMessage(payload: Data(name.utf8)))
        transcript = hs.handshakeHash
        let s = TransportSession(keys: try hs.split(), now: 0, kind: .pairingTransport)
        session = s
        return (m3, try s.seal(Data([PairingTag.commit]) + PairingCode.commitment(of: nonce), now: 0))
    }
    func afterResponderNonce(_ dg: Data) throws -> Data {
        let pt = try session!.open(dg, maxPlaintext: PairingTag.messageSize, now: 0)
        _ = PairingCode.code(handshakeHash: transcript, initiatorNonce: nonce, responderNonce: Data(pt.dropFirst()))
        return try session!.seal(Data([PairingTag.reveal]) + nonce, now: 0)
    }
}

/// One Mac: a coordinator over an in-memory store, a clock the preview moves, and the window.
final class Clock { var mono = 1000.0; var sessionActive = false }

final class Scene {
    let clock = Clock()
    var mono: Double { get { clock.mono } set { clock.mono = newValue } }
    var sessionActive: Bool { get { clock.sessionActive } set { clock.sessionActive = newValue } }
    let coordinator: PairingCoordinator
    let controller = PairingWindowController()
    let addr = PeerAddress(bytes: [192, 0, 2, 7])!
    init(paired: [PairedDevice] = []) throws {
        let mac = privKey(100)
        coordinator = try PairingCoordinator(
            staticKey: mac, store: MemoryKeyStore(identity: mac, paired: paired),
            ledger: PairingFailureLedger(store: MemoryPairingFailureStore()),
            monotonic: { [clock] in clock.mono })
        coordinator.isSessionActive = { [clock] in clock.sessionActive }
        controller.now = { [clock] in clock.mono }
    }
    func advance(_ s: Double) { mono += s; coordinator.tick() }
    /// A Linux device goes through the pairing handshake until both sides hold a code.
    func pair(seed: UInt8, name: String, from src: PeerAddress? = nil) throws {
        let l = try LinuxSide(key: privKey(seed), name: name)
        let src = src ?? addr
        guard let m2 = coordinator.receive(try l.message1(), from: src) else { throw PreviewError.refused(name) }
        let (m3, commit) = try l.afterMessage2(m2)
        _ = coordinator.receive(m3, from: src)
        guard let rm = coordinator.receive(commit, from: src) else { throw PreviewError.refused(name) }
        _ = coordinator.receive(try l.afterResponderNonce(rm), from: src)
    }
    /// The person reads the code for a moment and confirms what is on screen.
    @discardableResult func confirmOnScreen() -> ConfirmRefusal? {
        advance(1.5)
        let s = coordinator.status
        guard case .confirm(let code, _) = s.phase, let key = s.pendingKey else { return .noAttempt }
        return coordinator.confirm(code: code, peerKey: key)
    }
    func render(dark: Bool = false) -> Data? {
        controller.update(coordinator.status)
        return controller.renderPNG(dark: dark)
    }
}
enum PreviewError: Error { case refused(String) }

let desk = PairedDevice(name: "Desk PC", key: pub(1))
let laptop = PairedDevice(name: "Living room laptop", key: pub(2))

var failed = false
func emit(_ name: String, dark: Bool = false, _ scene: Scene) throws {
    guard let png = scene.render(dark: dark) else {
        FileHandle.standardError.write(Data("could not render \(name)\n".utf8))
        failed = true
        return
    }
    try png.write(to: dir.appendingPathComponent(name + ".png"))
    print(name, "\(scene.coordinator.status.phase)")
}

// 1: closed, nothing paired yet
try emit("mac-1-closed", try Scene())
// 2: the window is open, waiting
do { let s = try Scene(paired: [desk]); s.coordinator.open(); try emit("mac-2-waiting", s) }
// 3: a new device (a different name from every paired one) asks to pair
for dark in [false, true] {
    let s = try Scene(paired: [desk, laptop]); s.coordinator.open()
    try s.pair(seed: 3, name: "Studio PC")
    try emit(dark ? "mac-3-confirm-dark" : "mac-3-confirm", dark: dark, s)
}
// 4: a second address tries while the first waits
do {
    let s = try Scene(); s.coordinator.open()
    try s.pair(seed: 3, name: "Studio PC")
    _ = try? s.pair(seed: 4, name: "Other PC", from: PeerAddress(bytes: [192, 0, 2, 9])!)
    try emit("mac-4-another-device", s)
}
// 5: confirmed and stored
do {
    let s = try Scene(); s.coordinator.open()
    try s.pair(seed: 1, name: "Desk PC")
    if let why = s.confirmOnScreen() { throw PreviewError.refused("confirm: \(why)") }
    try emit("mac-5-paired", s)
}
// 6: the person rejected the code
do {
    let s = try Scene(); s.coordinator.open()
    try s.pair(seed: 3, name: "Studio PC")
    s.advance(1.5)
    s.coordinator.reject()
    try emit("mac-6-rejected", s)
}
// 7: nine rejected codes lock pairing
do {
    let s = try Scene(paired: [desk])
    for _ in 0..<3 {
        s.coordinator.open()
        for _ in 0..<3 {
            s.advance(2)
            try s.pair(seed: 3, name: "Studio PC")
            s.coordinator.reject()
        }
    }
    try emit("mac-7-locked", s)
}
// 8: a device whose name looks like a paired one (a Cyrillic look-alike), different key
do {
    let s = try Scene(paired: [desk]); s.coordinator.open()
    try s.pair(seed: 5, name: "Desk \u{0420}\u{0421}")
    try emit("mac-8-lookalike", s)
}
// 8b: the very same name under another key
do {
    let s = try Scene(paired: [desk]); s.coordinator.open()
    try s.pair(seed: 5, name: "Desk PC")
    try emit("mac-8b-same-name", s)
}
// 9: opening was refused because a session is live
do {
    let s = try Scene(paired: [desk, laptop]); s.sessionActive = true
    s.coordinator.open()
    try emit("mac-9-session-active", s)
}
// 10: Confirm clicked while another window covers this one: the window refuses and says why
do {
    let s = try Scene(paired: [desk]); s.coordinator.open()
    try s.pair(seed: 3, name: "Studio PC")
    s.advance(1.5)
    s.controller.update(s.coordinator.status)
    s.advance(1.5)  // the person has looked at the code for a moment
    s.controller.isFullyVisible = { _ in false }
    s.controller.confirmMouseDown()
    s.controller.confirmClicked()  // refused, with the reason in the notice
    guard let png = s.controller.renderPNG() else { throw PreviewError.refused("render mac-10") }
    try png.write(to: dir.appendingPathComponent("mac-10-covered.png"))
    print("mac-10-covered")
}
exit(failed ? 1 : 0)
