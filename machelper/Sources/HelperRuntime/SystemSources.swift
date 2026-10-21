import CoreGraphics
import Foundation
import HelperCore

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
    private var hintUntil = -Double.infinity

    public init(
        clock: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime },
        dictionary: @escaping () -> [String: Any]? = {
            CGSessionCopyCurrentDictionary() as? [String: Any]
        }
    ) {
        self.clock = clock; self.dictionary = dictionary
    }

    public func noteLockNotification() { hintUntil = clock() + SystemSessionSource.lockHintSeconds }
    public func noteUnlockNotification() { hintUntil = -.infinity }

    public func consoleUID() -> UInt32? {
        guard let d = dictionary(), Self.truthy(d["kCGSSessionOnConsoleKey"]),
            let uid = (d["kCGSSessionUserIDKey"] as? NSNumber)?.uint32Value
        else { return nil }
        return uid
    }

    public func screenLocked() -> Bool? {
        guard let d = dictionary(), d["kCGSSessionOnConsoleKey"] != nil else { return nil }  // unknown
        return Self.truthy(d["CGSSessionScreenIsLocked"]) || clock() < hintUntil
    }

    static func truthy(_ v: Any?) -> Bool {
        if let b = v as? Bool { return b }
        return ((v as? NSNumber)?.intValue ?? 0) != 0
    }
}

/// Screen Sharing (TCP 5900) peers from the kernel table.
public final class SystemScreenSharingSource: ScreenSharingPeerSource {
    private let port: UInt16
    public init(port: UInt16 = 5900) { self.port = port }
    public func peers() -> Set<PeerAddress>? { TCPTable.read(localPort: port) }
}

/// The PostEvent (Accessibility) grant, checked without ever prompting.
/// Asking is a separate, explicit menu action in the app.
public final class SystemAccessSource: AccessSource {
    public init() {}
    public func granted() -> Bool { CGPreflightPostEventAccess() }
}
