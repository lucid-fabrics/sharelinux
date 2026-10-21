import Foundation

public enum ChannelLimits {
    public static let inputPacketsPerSecond = 1000.0
    public static let handshakesPerSecond = 5.0
    /// A session with no authenticated packet for this long ends (design 3.4).
    public static let silenceTimeout = 1.0
    /// Sessions older than this, or past `maxMessages`, must be re-keyed with a new handshake.
    public static let maxSessionAge = 120.0
    public static let maxMessages: UInt64 = 1 << 20
    /// Accepted distance between a handshake timestamp and our wall clock.
    public static let handshakeClockSkew = 300.0
    public static let statusMinInterval = 0.25
    public static let maxDeviceNameBytes = 64
}

/// Token bucket with an injected clock (monotonic seconds). Burst equals one second of rate.
public struct RateLimiter: Sendable {
    private let rate: Double
    private let capacity: Double
    private var tokens: Double
    private var last: Double?

    public init(perSecond: Double) {
        rate = perSecond
        capacity = perSecond
        tokens = perSecond
    }

    public mutating func allow(now: Double) -> Bool {
        // A clock that goes backwards must not mint tokens, so `last` never moves back.
        if let l = last, now > l {
            tokens = min(capacity, tokens + (now - l) * rate)
            last = now
        } else if last == nil {
            last = now
        }
        guard tokens >= 1 else { return false }
        tokens -= 1
        return true
    }
}
