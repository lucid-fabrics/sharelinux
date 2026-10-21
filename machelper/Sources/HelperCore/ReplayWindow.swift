import Foundation

/// Sliding replay window over 64-bit counters (RFC 6479 idea): the highest
/// accepted counter plus a 2048-bit ring of "seen" flags below it.
/// Usage: `check` before decrypting (cheap reject), `commit` only after the
/// packet authenticated.
public struct ReplayWindow: Sendable {
    public static let size: UInt64 = 2048

    private var words = [UInt64](repeating: 0, count: Int(ReplayWindow.size / 64))
    private var highest: UInt64 = 0
    private var any = false

    public init() {}

    private func bit(_ c: UInt64) -> (Int, UInt64) {
        let i = c % ReplayWindow.size
        return (Int(i / 64), 1 << (i % 64))
    }

    public func check(_ counter: UInt64) -> Bool {
        guard any else { return true }
        if counter > highest { return true }
        if highest - counter >= ReplayWindow.size { return false }
        let (w, m) = bit(counter)
        return words[w] & m == 0
    }

    /// Returns false if the counter is not acceptable (same rule as `check`).
    @discardableResult
    public mutating func commit(_ counter: UInt64) -> Bool {
        guard check(counter) else { return false }
        if !any {
            any = true
            highest = counter
        } else if counter > highest {
            let gap = counter - highest
            if gap >= ReplayWindow.size {
                for i in words.indices { words[i] = 0 }
            } else {
                var c = highest &+ 1
                while c <= counter {
                    let (w, m) = bit(c)
                    words[w] &= ~m
                    c &+= 1
                }
            }
            highest = counter
        }
        let (w, m) = bit(counter)
        words[w] |= m
        return true
    }
}
