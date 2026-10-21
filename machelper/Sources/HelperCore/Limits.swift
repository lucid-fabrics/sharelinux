import Foundation

public enum ChannelLimits {
    public static let inputPacketsPerSecond = 1000.0
    public static let handshakesPerSecond = 5.0
    /// Per UDP source, applied before the global budget so one noisy address
    /// cannot drain it. Burst of 2 covers a client retry.
    public static let handshakesPerSourcePerSecond = 1.0
    public static let handshakesPerSourceBurst = 2.0
    /// Distinct sources tracked at once; when full, the oldest never-authenticated source makes room.
    public static let maxTrackedSources = 256
    /// Sources that authenticated a session stay protected from eviction this
    /// long, at most this many at once (well below `maxTrackedSources`).
    public static let sourcePinLifetime = 600.0
    public static let maxPinnedSources = 32
    /// Total |dx|+|dy| per second, in points * 16 (20 000 pt/s).
    public static let motionUnitsPerSecond = 20_000.0 * 16
    /// Environment snapshots older than this are not trusted (design 3.3).
    public static let environmentMaxAge = 0.5
    /// Pairing window: one local click opens it for this long.
    public static let pairingWindow = 120.0
    /// Failed pairing attempts (protocol error, bad commit, or "codes differ")
    /// before the window closes.
    public static let pairingMaxFailures = 3
    /// Minimum time between two pairing attempts starting (message 1), so the
    /// window cannot be spun through attempts faster than a person could try.
    public static let pairingMinStartSpacing = 1.0
    /// Attempt starts (message 1) one source may make per pairing window. A LAN
    /// host using its own address could otherwise restart for free every
    /// `pairingMinStartSpacing` and keep an honest device out for the whole
    /// window. Past the cap that source is refused until the window ends.
    public static let pairingMaxStartsPerSource = 3
    /// Failed pairing attempts across windows: this many within
    /// `pairingFailureHorizon` and pairing is locked until the oldest of them
    /// is older than the horizon (the owner sees the cooldown).
    public static let pairingFailureCap = 9
    public static let pairingFailureHorizon = 24.0 * 3600
    /// An attempt that has not finished within this long is dropped. Charged
    /// attempts (r_M sent) count as a failure when it fires.
    public static let pairingAttemptTimeout = 20.0
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

    public init(perSecond: Double, burst: Double? = nil) {
        rate = perSecond
        capacity = burst ?? perSecond
        tokens = capacity
    }

    /// True when the bucket has refilled completely, so forgetting it loses nothing.
    public func isFull(now: Double) -> Bool {
        guard let l = last, now >= l else { return last == nil }
        return tokens + (now - l) * rate >= capacity
    }

    public mutating func allow(now: Double, cost: Double = 1) -> Bool {
        // A clock that goes backwards must not mint tokens, so `last` never moves back.
        if let l = last, now > l {
            tokens = min(capacity, tokens + (now - l) * rate)
            last = now
        } else if last == nil {
            last = now
        }
        guard tokens >= cost else { return false }
        tokens -= cost
        return true
    }
}
