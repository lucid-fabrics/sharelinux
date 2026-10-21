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
//   !grant on|off, !advance <seconds>, !tick   -> "OK" (tick prints the event)
// Pairing (XX) datagrams are routed to a PairingResponder: its event reads
// "pair-sas <6 digits> <name with _ for spaces> <remote static hex>" after message 3.

var args = Array(CommandLine.arguments.dropFirst())
var paired: [Data] = []
var keyHex: String?
while !args.isEmpty {
    let a = args.removeFirst()
    switch a {
    case "--paired": if let v = args.first, let d = Data(hex: v) { paired.append(d); args.removeFirst() }
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
var toggle = true, locked = false, console = true, sharing = true, grant = true
var mono = 1000.0

let channel = HelperChannel(
    staticKey: staticKey, pairedKeys: { paired },
    environment: { peer, _ in
        GateInputs(
            toggleOn: toggle, peerPaired: false, udpPeer: peer,
            screenSharingPeers: sharing ? [udpPeer] : [], consoleUID: console ? 501 : nil, ownUID: 501,
            screenLocked: locked, accessibilityGranted: grant, rateLimited: false)
    },
    clock: ChannelClock(monotonic: { mono }, wall: { Date() }))

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

var pairing: PairingResponder?

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
    if let first = dg.first, first == DatagramKind.xxMessage1.rawValue || first == DatagramKind.xxMessage3.rawValue {
        if first == DatagramKind.xxMessage1.rawValue { pairing = try? PairingResponder(staticKey: staticKey) }
        guard let p = pairing else { out("EVENT pair-error nostate"); out("NONE"); continue }
        do {
            let reply = try p.receive(dg)
            if p.isComplete, let sas = p.sas, let name = p.deviceName, let rs = p.remoteStatic {
                out("EVENT pair-sas \(sas) \(name.replacingOccurrences(of: " ", with: "_")) \(rs.hex)")
            } else {
                out("EVENT pair-ok")
            }
            out(reply.map { "REPLY \($0.hex)" } ?? "NONE")
        } catch {
            pairing = nil
            out("EVENT pair-error \(error)")
            out("NONE")
        }
        continue
    }
    let r = channel.receive(dg, from: udpPeer)
    out("EVENT " + r.events.map(describe).joined(separator: ";"))
    out(r.reply.map { "REPLY \($0.hex)" } ?? "NONE")
}
