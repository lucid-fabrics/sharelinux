import Foundation
import os

/// Hands state updates from the runtime queue to another queue (the main
/// thread, where the menu and the kill switch handlers live) with bounded
/// work: at most one delivery is ever pending, and deliveries are at least
/// `minInterval` apart. Pushes in between replace the pending value, so only
/// the newest one is delivered (the trailing update is never lost).
///
/// Why: a paired peer that alternates denied and allowed packets flips the
/// visible state on every packet, up to a thousand times a second. Posting
/// each flip to the main thread would queue the hotkey and "Stop now" handlers
/// behind a pile of menu redraws.
public final class StateCoalescer<Value>: @unchecked Sendable {
    private let target: DispatchQueue
    private let minInterval: Double
    private let clock: () -> Double
    private let deliver: (Value) -> Void
    private let lock = NSLock()
    private var latest: Value?
    private var scheduled = false
    private var lastDelivery = -Double.infinity

    /// Deliveries made so far (tests).
    private var delivered = 0
    public var deliveries: Int { lock.lock(); defer { lock.unlock() }; return delivered }

    public init(
        target: DispatchQueue, minInterval: Double = 0.1,
        clock: @escaping () -> Double = { ProcessInfo.processInfo.systemUptime },
        deliver: @escaping (Value) -> Void
    ) {
        self.target = target; self.minInterval = minInterval; self.clock = clock; self.deliver = deliver
    }

    /// Callable from any thread.
    public func push(_ value: Value) {
        lock.lock()
        latest = value
        if scheduled { lock.unlock(); return }
        scheduled = true
        let now = clock()
        let wait = lastDelivery <= now ? max(0, lastDelivery + minInterval - now) : minInterval
        lock.unlock()
        target.asyncAfter(deadline: .now() + wait) { [self] in
            lock.lock()
            let value = latest
            latest = nil
            scheduled = false
            lastDelivery = clock()
            delivered += 1
            lock.unlock()
            if let value { deliver(value) }
        }
    }
}
