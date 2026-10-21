import CryptoKit
import Foundation
import HelperCore

// Line-driven test peer for the Go interop test (internal/machelper). It is the
// Mac side of the channel with every environment input under test control.
// Not shipped, not installed.
//
// stdout, first line:  PUB <hex static public key>
// stdin, one per line:
//   <hex datagram>      -> "EVENT <events>" then "REPLY <hex>" or "NONE"
//   !toggle on|off, !locked on|off, !console on|off, !sharing on|off,
//   !grant on|off, !stale on|off, !advance <seconds>, !tick   -> "OK" (tick prints the event)
//   !pair open|reject|confirm   -> "OK" (confirm prints "confirmed <name> <hex>" or "nothing")
// Pairing needs the window opened with "!pair open" (the local click). Pairing
// datagrams (kinds 0x10, 0x12, 0x13) go to a PairingWindow; the event reads
// "pair-ok" per step and "pair-sas <6 digits> <name with _ for spaces> <remote static hex>"
// after the reveal, or "pair-error <error>".
// Options: --paired <hex>, --key <hex>, --timestamps <file> (persisted handshake timestamps).

var args = Array(CommandLine.arguments.dropFirst())
var paired: [Data] = []
var keyHex: String?
var timestampPath: String?
while !args.isEmpty {
    let a = args.removeFirst()
    switch a {
    case "--paired": if let v = args.first, let d = Data(hex: v) { paired.append(d); args.removeFirst() }
    case "--timestamps": if let v = args.first { timestampPath = v; args.removeFirst() }
    case "--key": if let v = args.first { keyHex = v; args.removeFirst() }
    default: FileHandle.standardError.write(Data("unknown argument \(a)\n".utf8)); exit(2)
    }
}

let staticKey: Curve25519.KeyAgreement.PrivateKey = {
    if let keyHex, let d = Data(hex: keyHex), let k = try? Curve25519.KeyAgreement.PrivateKey(rawRepresentation: d) {
        return k
    }
    return Curve25519.KeyAgreement.PrivateKey()
}()

let udpPeer = PeerAddress(bytes: [192, 0, 2, 1])!
var toggle = true, locked = false, console = true, sharing = true, grant = true, stale = false
var mono = 1000.0

let channel = HelperChannel(
    staticKey: staticKey, pairedKeys: { paired },
    environment: { peer, _ in
        GateInputs(
            toggleOn: toggle, peerPaired: false, udpPeer: peer,
            screenSharingPeers: sharing ? [udpPeer] : [], consoleUID: console ? 501 : nil, ownUID: 501,
            screenLocked: locked, accessibilityGranted: grant, rateLimited: false,
            snapshotAt: stale ? mono - 1 : mono)
    },
    clock: ChannelClock(monotonic: { mono }, wall: { Date() }),
    timestamps: timestampPath.map { FileTimestampStore(url: URL(fileURLWithPath: $0)) } ?? MemoryTimestampStore())

func describe(_ e: ChannelEvent) -> String {
    switch e {
    case .input(let p): return "input \(p.encode().hex)"
    case .sessionStarted(let k): return "started \(k.hex)"
    case .keepalive: return "keepalive"
    case .denied(let s): return "denied \(s.rawValue)"
    case .ended(let r): return "ended \(r)"
    case .dropped(let r): return "dropped \(r)"
    case .handshakeAnswered: return "answered"
    }
}

func out(_ s: String) { print(s); fflush(stdout) }

out("PUB \(staticKey.publicKey.rawRepresentation.hex)")

var window: PairingWindow?

while let line = readLine(strippingNewline: true) {
    if line.hasPrefix("!") {
        let parts = line.dropFirst().split(separator: " ").map(String.init)
        let on = parts.count > 1 && parts[1] == "on"
        switch parts.first ?? "" {
        case "toggle": toggle = on
        case "locked": locked = on
        case "console": console = on
        case "sharing": sharing = on
        case "grant": grant = on
        case "stale": stale = on
        case "pair":
            switch parts.count > 1 ? parts[1] : "" {
            case "open": window = PairingWindow(staticKey: staticKey, clock: { mono })
            case "reject": window?.reject()
            case "confirm":
                if let c = window?.confirm() {
                    out("confirmed \(c.name.replacingOccurrences(of: " ", with: "_")) \(c.key.hex)")
                } else { out("nothing") }
                continue
            default: out("ERR unknown pair command"); continue
            }
        case "advance": mono += Double(parts.count > 1 ? parts[1] : "0") ?? 0
        case "tick":
            out(channel.tick().map(describe) ?? "none")
            continue
        default: out("ERR unknown command"); continue
        }
        out("OK")
        continue
    }
    guard let dg = Data(hex: line) else { out("EVENT bad-hex"); out("NONE"); continue }
    if let first = dg.first, [DatagramKind.xxMessage1, .xxMessage3, .pairingTransport].map(\.rawValue).contains(first) {
        guard let w = window else { out("EVENT pair-error closed"); out("NONE"); continue }
        do {
            let reply = try w.receive(dg)
            if let p = w.pending, let sas = p.sas, let name = p.deviceName, let rs = p.remoteStatic {
                out("EVENT pair-sas \(sas) \(name.replacingOccurrences(of: " ", with: "_")) \(rs.hex)")
            } else {
                out("EVENT pair-ok")
            }
            out(reply.map { "REPLY \($0.hex)" } ?? "NONE")
        } catch {
            out("EVENT pair-error \(error)")
            out("NONE")
        }
        continue
    }
    let r = channel.receive(dg, from: udpPeer)
    out("EVENT " + r.events.map(describe).joined(separator: ";"))
    out(r.reply.map { "REPLY \($0.hex)" } ?? "NONE")
}
