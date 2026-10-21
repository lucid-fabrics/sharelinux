import CryptoKit
import Foundation
import HelperCore
import HelperRuntime

// The app's runtime on a real UDP socket (loopback) with a fake poster and fake
// environment sources, driven by the Go tests (internal/machelper). It posts
// nothing to the window server and asks for no permission. Not shipped.
//
// Arguments: --paired <hex public key> (repeatable), --key <hex private key>
// stdout:
//   PUB <hex>          static public key
//   LISTEN <port>      UDP port on 127.0.0.1 / ::1
//   POST move <dx> <dy> held=<mask>
//   POST down <button> click=<n>   |   POST up <button>   |   POST scroll <dx> <dy>
//   STATE allow=.. device=.. injecting=.. deny=..   after every state change
// stdin, one command per line, each answered with "OK" (after running on the runtime queue):
//   !toggle on|off     the local "Allow raw mouse" switch
//   !locked on|off, !console on|off, !sharing on|off, !grant on|off
//   !freeze on|off     stop the environment sampling (a stalled poller)
//   !kill, !sleep, !lock (lock notification), !unpair
//   !sync              OK once everything queued before it ran

final class FakePoster: EventPoster {
    func postMove(dx: Int, dy: Int, held: ButtonMask) { out("POST move \(dx) \(dy) held=\(held.rawValue)") }
    func postButton(_ button: MouseButton, down: Bool, clickState: Int) {
        out(down ? "POST down \(button.rawValue) click=\(clickState)" : "POST up \(button.rawValue)")
    }
    func postScroll(dx: Int, dy: Int) { out("POST scroll \(dx) \(dy)") }
}

final class Switches {
    var locked = false, console = true, sharing = true, grant = true, frozen = false
    var sharingPeers: Set<PeerAddress> = [PeerAddress(bytes: [127, 0, 0, 1])!]
}

final class FakeSession: SessionStateSource {
    let s: Switches
    init(_ s: Switches) { self.s = s }
    func consoleUID() -> UInt32? { s.console ? 501 : nil }
    func screenLocked() -> Bool? { s.locked }
}
final class FakeSharing: ScreenSharingPeerSource {
    let s: Switches
    init(_ s: Switches) { self.s = s }
    func peers() -> Set<PeerAddress>? { s.sharing ? s.sharingPeers : [] }
}
final class FakeAccess: AccessSource {
    let s: Switches
    init(_ s: Switches) { self.s = s }
    func granted() -> Bool { s.grant }
}

let outLock = NSLock()
func out(_ line: String) {
    outLock.lock()
    defer { outLock.unlock() }
    print(line)
    fflush(stdout)
}

var args = Array(CommandLine.arguments.dropFirst())
var keyHex: String?
var paired: [Data] = []
while !args.isEmpty {
    let a = args.removeFirst()
    switch a {
    case "--key": if let v = args.first { keyHex = v; args.removeFirst() }
    case "--paired": if let v = args.first, let d = Data(hex: v) { paired.append(d); args.removeFirst() }
    default:
        FileHandle.standardError.write(Data("unknown argument \(a)\n".utf8))
        exit(2)
    }
}
let staticKey: Curve25519.KeyAgreement.PrivateKey =
    keyHex.flatMap { Data(hex: $0) }.flatMap { try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: $0) }
    ?? Curve25519.KeyAgreement.PrivateKey()

let sw = Switches()
var pairedList = paired
let runtime = HelperRuntime(
    staticKey: staticKey, pairedKeys: { pairedList }, poster: FakePoster(),
    session: FakeSession(sw), sharing: FakeSharing(sw), access: FakeAccess(sw), ownUID: 501,
    clock: ChannelClock(monotonic: { ProcessInfo.processInfo.systemUptime }, wall: { Date() }),
    timestamps: MemoryTimestampStore())
runtime.onState = { s in
    out(
        "STATE allow=\(s.allowRawMouse) device=\(s.device ?? "-") injecting=\(s.injecting) deny=\(s.lastDeny.map { String($0.rawValue) } ?? "-")"
    )
}
let server = UDPServer(runtime: runtime, port: 0, loopbackOnly: true, interval: 0.05, samplingEnabled: { !sw.frozen })
let port = try server.start()
out("PUB \(runtime.staticPublicKey.hex)")
out("LISTEN \(port)")

while let line = readLine(strippingNewline: true) {
    let parts = line.split(separator: " ").map(String.init)
    guard parts.first?.hasPrefix("!") == true else {
        out("ERR not a command")
        continue
    }
    let on = parts.count > 1 && parts[1] == "on"
    runtime.queue.sync {
        switch parts[0] {
        case "!toggle": runtime.setAllowRawMouse(on)
        case "!locked": sw.locked = on; runtime.refreshEnvironment()
        case "!console": sw.console = on; runtime.refreshEnvironment()
        case "!sharing": sw.sharing = on; runtime.refreshEnvironment()
        case "!grant": sw.grant = on; runtime.refreshEnvironment()
        case "!freeze": sw.frozen = on
        case "!kill": runtime.killSwitch()
        case "!sleep": runtime.willSleep()
        case "!lock":  // the lock notification: the source answers "locked" from now on, and buttons come up at once
            sw.locked = true
            runtime.environmentInterruption(.locked)
        case "!unpair":
            pairedList = []
            runtime.pairedKeysChanged()
        case "!sync": break
        default: out("ERR unknown command")
        }
    }
    out("OK")
}
server.stop()
