import Foundation
import HelperCore

/// The only thing between the controller and the window server, and the part
/// of the kill switch that does not depend on any queue.
///
///  * `beginStop()` can be called from any thread (hotkey, menu, sleep and lock
///    handlers). It raises the stop flag and, on the calling thread, posts
///    button-up for every button this poster put down. From then on every
///    move, press and scroll is dropped until `endStop()`; a button-up is
///    still let through (and only for a button that is actually down), so the
///    controller's own release can never be blocked.
///  * The flag is a counter: each `beginStop()` is paired with an `endStop()`
///    that the runtime calls on its queue after it has ended the session and
///    released the controller. If the queue never gets there, posting stays off.
///  * Every post happens under one lock together with the flag check, so a
///    press cannot slip out between the check and the post, and the emergency
///    release cannot interleave with a post in flight.
///
/// It also counts what it let through, which makes "nothing was posted after
/// the stop" testable from outside.
public final class GuardedPoster: EventPoster, @unchecked Sendable {
    private let inner: EventPoster
    private let lock = NSLock()
    private var stops = 0
    private var down: ButtonMask = []
    private var clickState: [MouseButton: Int] = [:]
    private var posts = 0

    public init(_ inner: EventPoster) { self.inner = inner }

    public var isStopped: Bool { lock.lock(); defer { lock.unlock() }; return stops > 0 }
    /// Posts that reached the inner poster so far.
    public var postedCount: Int { lock.lock(); defer { lock.unlock() }; return posts }
    public var buttonsDown: ButtonMask { lock.lock(); defer { lock.unlock() }; return down }

    /// Stops posting and releases every button this poster put down, now, on this thread.
    public func beginStop() {
        lock.lock(); defer { lock.unlock() }
        stops += 1
        for b in MouseButton.allCases where down.contains(b.mask) {
            inner.postButton(b, down: false, clickState: clickState[b] ?? 1)
            posts += 1
        }
        down = []
        clickState = [:]
    }

    /// One `beginStop()` has been fully handled.
    public func endStop() {
        lock.lock(); defer { lock.unlock() }
        if stops > 0 { stops -= 1 }
    }

    public func postMove(dx: Int, dy: Int, held: ButtonMask) {
        lock.lock(); defer { lock.unlock() }
        guard stops == 0 else { return }
        inner.postMove(dx: dx, dy: dy, held: held)
        posts += 1
    }

    public func postButton(_ button: MouseButton, down isDown: Bool, clickState click: Int) {
        lock.lock(); defer { lock.unlock() }
        if isDown {
            guard stops == 0 else { return }
            down.insert(button.mask)
            clickState[button] = click
        } else {
            // Only a button we put down can be released; a second release (the
            // controller catching up after an emergency one) is a no-op.
            guard down.contains(button.mask) else { return }
            down.remove(button.mask)
        }
        inner.postButton(button, down: isDown, clickState: click)
        posts += 1
    }

    public func postScroll(dx: Int, dy: Int) {
        lock.lock(); defer { lock.unlock() }
        guard stops == 0 else { return }
        inner.postScroll(dx: dx, dy: dy)
        posts += 1
    }
}
