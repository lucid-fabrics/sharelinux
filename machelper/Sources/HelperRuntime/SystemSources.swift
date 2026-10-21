import CoreGraphics
import Foundation
import HelperCore
import Security
import os

/// Console and lock state from `CGSessionCopyCurrentDictionary()`:
/// `kCGSSessionOnConsoleKey` and `kCGSSessionUserIDKey` (documented), and
/// `CGSSessionScreenIsLocked` (undocumented but long-standing; it is present
/// and true while the screen is locked, absent otherwise). The distributed
/// notifications `com.apple.screenIsLocked` / `...Unlocked`, delivered by the
/// app through `noteLockNotification` / `noteUnlockNotification`, make a lock
/// count at once and for `lockHintSeconds` even if the dictionary lags.
public final class SystemSessionSource: SessionStateSource {
    static let lockHintSeconds = 2.0
    private let dictionary: () -> [String: Any]?
    private let clock: () -> Double
    /// Written by the notification thread, read on the runtime queue: always under the lock.
    private let hint = OSAllocatedUnfairLock(initialState: -Double.infinity)

    public init(
        clock: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime },
        dictionary: @escaping () -> [String: Any]? = {
            CGSessionCopyCurrentDictionary() as? [String: Any]
        }
    ) {
        self.clock = clock; self.dictionary = dictionary
    }

    public func noteLockNotification() {
        let until = clock() + SystemSessionSource.lockHintSeconds
        hint.withLock { $0 = until }
    }
    public func noteUnlockNotification() { hint.withLock { $0 = -.infinity } }

    public func consoleUID() -> UInt32? {
        guard let d = dictionary(), Self.truthy(d["kCGSSessionOnConsoleKey"]),
            let uid = (d["kCGSSessionUserIDKey"] as? NSNumber)?.uint32Value
        else { return nil }
        return uid
    }

    public func screenLocked() -> Bool? {
        guard let d = dictionary(), d["kCGSSessionOnConsoleKey"] != nil else { return nil }  // unknown
        return Self.truthy(d["CGSSessionScreenIsLocked"]) || clock() < hint.withLock { $0 }
    }

    static func truthy(_ v: Any?) -> Bool {
        if let b = v as? Bool { return b }
        return ((v as? NSNumber)?.intValue ?? 0) != 0
    }
}

/// Whether a process is Apple's Screen Sharing server: an Apple-signed binary
/// whose identifier is one of the Screen Sharing / Remote Management servers.
///
/// `com.apple.screensharing.agent` (the per-user ScreensharingAgent) is on the
/// list on purpose: launchd hands the accepted connection to
/// `com.apple.screensharing.daemon`, but the per-user agent serves the session
/// in the logged-in user's context and may own the established socket on
/// recent macOS. Both are Apple-anchored identifiers, so the check is no
/// weaker: only Apple-signed code (SIP protected) can carry them. Which of the
/// three really owns the socket on the owner's macOS is a W5 drill item; the
/// paired key stays the real lock either way.
/// The kernel's code-signing check by pid needs no privilege, also for root
/// processes. A pid that no longer exists, an unsigned binary or an Apple
/// binary with another identifier all answer false.
public enum ScreenSharingServer {
    public static let requirement =
        "anchor apple and (identifier \"com.apple.screensharing.daemon\" or identifier \"com.apple.screensharing.agent\" or identifier \"com.apple.RemoteDesktopAgent\" or identifier \"com.apple.AppleVNCServer\")"

    /// Whether the code at `path` satisfies the requirement (static check, no
    /// process needed). Used to prove the allowlist against the real binaries.
    static func matches(path: String, requirement: String = ScreenSharingServer.requirement) -> Bool {
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(URL(fileURLWithPath: path) as CFURL, [], &code) == errSecSuccess, let code
        else { return false }
        var req: SecRequirement?
        guard SecRequirementCreateWithString(requirement as CFString, [], &req) == errSecSuccess, let req else {
            return false
        }
        return SecStaticCodeCheckValidity(code, [], req) == errSecSuccess
    }

    public static func isRunning(pid: Int32, requirement: String = ScreenSharingServer.requirement) -> Bool {
        guard pid > 0 else { return false }
        var code: SecCode?
        guard SecCodeCopyGuestWithAttributes(nil, [kSecGuestAttributePid: pid] as CFDictionary, [], &code) == errSecSuccess,
            let code
        else { return false }
        var req: SecRequirement?
        guard SecRequirementCreateWithString(requirement as CFString, [], &req) == errSecSuccess, let req else {
            return false
        }
        return SecCodeCheckValidity(code, [], req) == errSecSuccess
    }
}

/// Screen Sharing (TCP 5900) peers from the kernel table: established
/// connections to the port whose socket belongs to Apple's Screen Sharing
/// server (not just any process that listens on 5900, which an unprivileged
/// program can do while Screen Sharing is off). What this proves and what it
/// cannot is in the design's threat model (section 3.6): it does not prove the
/// remote user logged in.
public final class SystemScreenSharingSource: ScreenSharingPeerSource {
    private let port: UInt16
    private let connections: (UInt16) -> [TCPConnection]?
    private let isServer: (Int32) -> Bool
    private let clock: () -> Double
    private var verdicts: [Int32: (ok: Bool, at: Double)] = [:]
    /// A pid is re-checked after this long (pids are not reused that fast).
    static let verdictLifetime = 2.0

    public init(
        port: UInt16 = 5900,
        connections: @escaping (UInt16) -> [TCPConnection]? = { TCPTable.readConnections(localPort: $0) },
        isServer: @escaping (Int32) -> Bool = { ScreenSharingServer.isRunning(pid: $0) },
        clock: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime }
    ) {
        self.port = port; self.connections = connections; self.isServer = isServer; self.clock = clock
    }

    public func peers() -> Set<PeerAddress>? {
        guard let all = connections(port) else { return nil }
        let now = clock()
        verdicts = verdicts.filter { now >= $0.value.at && now - $0.value.at < Self.verdictLifetime }
        func ok(_ pid: Int32) -> Bool {
            guard pid > 0 else { return false }
            if let v = verdicts[pid] { return v.ok }
            let r = isServer(pid)
            verdicts[pid] = (r, now)
            return r
        }
        return Set(all.filter { ok($0.pid) || ok($0.effectivePid) }.map(\.peer))
    }
}

/// The PostEvent (Accessibility) grant, checked without ever prompting.
/// Asking is a separate, explicit menu action in the app.
public final class SystemAccessSource: AccessSource {
    public init() {}
    public func granted() -> Bool { CGPreflightPostEventAccess() }
}
