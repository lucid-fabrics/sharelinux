import Foundation
import HelperCore

/// Where the monitor reads the world from. Each returns nil when it cannot
/// tell, which the gate treats as "deny".
public protocol SessionStateSource: AnyObject {
    /// uid of the user whose session is on the console (nil: not on console or unknown).
    func consoleUID() -> UInt32?
    /// nil: unknown, treated as locked.
    func screenLocked() -> Bool?
}

public protocol ScreenSharingPeerSource: AnyObject {
    /// Remote addresses of established TCP connections to local port 5900. nil: could not read.
    func peers() -> Set<PeerAddress>?
}

public protocol AccessSource: AnyObject {
    /// Accessibility / PostEvent grant present (a check, never a prompt).
    func granted() -> Bool
}

/// One sampling of the environment.
public struct EnvironmentSnapshot: Equatable, Sendable {
    public var at: Double
    public var consoleUID: UInt32?
    public var screenLocked: Bool?
    public var peers: Set<PeerAddress>?
    public var accessGranted: Bool

    /// True when input could be flowing under this snapshot as far as the
    /// environment goes (console, unlocked, grant). Screen Sharing peers are
    /// checked per session.
    func allows(ownUID: UInt32) -> Bool {
        consoleUID == ownUID && screenLocked == false && accessGranted
    }
}

/// Samples the environment sources and hands the latest sample, with its
/// monotonic timestamp, to the gate. The gate refuses a sample older than
/// `ChannelLimits.environmentMaxAge`, so a stalled `refresh` shows up as a deny
/// rather than as stale permission.
///
/// Not thread safe: the runtime calls it from its own serial queue.
public final class EnvironmentMonitor {
    private let session: SessionStateSource
    private let sharing: ScreenSharingPeerSource
    private let access: AccessSource
    private let clock: () -> Double
    public let ownUID: UInt32
    public private(set) var snapshot: EnvironmentSnapshot?

    public init(
        session: SessionStateSource, sharing: ScreenSharingPeerSource, access: AccessSource,
        ownUID: UInt32, clock: @escaping () -> Double
    ) {
        self.session = session; self.sharing = sharing; self.access = access
        self.ownUID = ownUID; self.clock = clock
    }

    /// Takes a new sample. Returns the previous and the new one.
    @discardableResult
    public func refresh() -> (old: EnvironmentSnapshot?, new: EnvironmentSnapshot) {
        let old = snapshot
        let new = EnvironmentSnapshot(
            at: clock(), consoleUID: session.consoleUID(), screenLocked: session.screenLocked(),
            peers: sharing.peers(), accessGranted: access.granted())
        snapshot = new
        return (old, new)
    }

    /// Gate inputs for a peer. `toggleOn` is the local switch, read by the caller
    /// right now (not part of the sample). `peerPaired` and `rateLimited` are
    /// filled in by the channel.
    public func gateInputs(udpPeer: PeerAddress?, toggleOn: Bool) -> GateInputs {
        let s = snapshot
        return GateInputs(
            toggleOn: toggleOn, peerPaired: false, udpPeer: udpPeer,
            screenSharingPeers: s?.peers ?? [], consoleUID: s?.consoleUID, ownUID: ownUID,
            screenLocked: s?.screenLocked, accessibilityGranted: s?.accessGranted ?? false,
            rateLimited: false, snapshotAt: s?.at)
    }
}
