import Foundation

/// An IP address as raw bytes (4 or 16). IPv4-mapped IPv6 addresses are
/// normalised to 4 bytes so a dual-stack socket and `netstat` agree.
public struct PeerAddress: Hashable, Sendable {
    public let bytes: [UInt8]

    public init?(bytes: [UInt8]) {
        switch bytes.count {
        case 4:
            self.bytes = bytes
        case 16:
            let mapped: [UInt8] = [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff]
            self.bytes = Array(bytes[0..<12]) == mapped ? Array(bytes[12..<16]) : bytes
        default:
            return nil
        }
    }
}

/// Everything the acceptance decision depends on, passed in so the logic stays
/// a pure function. Optionals are "could not determine" and fail closed.
public struct GateInputs: Sendable {
    /// Menu switch "Allow raw mouse". Local only.
    public var toggleOn: Bool
    /// The peer's static key is in the paired list.
    public var peerPaired: Bool
    /// UDP source address of the authenticated peer.
    public var udpPeer: PeerAddress?
    /// Remote addresses of established TCP connections to local port 5900.
    public var screenSharingPeers: Set<PeerAddress>
    /// uid owning the console session, nil if there is none or it is unknown.
    public var consoleUID: UInt32?
    /// uid this helper runs as.
    public var ownUID: UInt32
    /// nil means unknown, which is treated as locked.
    public var screenLocked: Bool?
    public var accessibilityGranted: Bool
    public var rateLimited: Bool

    public init(
        toggleOn: Bool, peerPaired: Bool, udpPeer: PeerAddress?,
        screenSharingPeers: Set<PeerAddress>, consoleUID: UInt32?, ownUID: UInt32,
        screenLocked: Bool?, accessibilityGranted: Bool, rateLimited: Bool
    ) {
        self.toggleOn = toggleOn; self.peerPaired = peerPaired; self.udpPeer = udpPeer
        self.screenSharingPeers = screenSharingPeers; self.consoleUID = consoleUID
        self.ownUID = ownUID; self.screenLocked = screenLocked
        self.accessibilityGranted = accessibilityGranted; self.rateLimited = rateLimited
    }
}

public enum GateDecision: Equatable, Sendable {
    case accept
    /// Authenticated peer, but input is refused; the state is reported back.
    case deny(HelperState)
    /// Unpaired peer: no reply of any kind.
    case dropSilently
}

public enum Gate {
    /// The six accept conditions of design 3.3. First failing condition wins, in
    /// this order, so the reported reason is stable and the cheapest to fix first.
    public static func evaluate(_ i: GateInputs) -> GateDecision {
        guard i.peerPaired else { return .dropSilently }
        guard i.toggleOn else { return .deny(.disabled) }
        guard let console = i.consoleUID, console == i.ownUID else { return .deny(.notConsole) }
        guard i.screenLocked == false else { return .deny(.locked) }
        guard let peer = i.udpPeer, i.screenSharingPeers.contains(peer) else { return .deny(.noSession) }
        guard i.accessibilityGranted else { return .deny(.needsGrant) }
        guard !i.rateLimited else { return .deny(.rateLimited) }
        return .accept
    }
}
