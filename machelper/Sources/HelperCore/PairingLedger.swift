import Foundation

/// Persistence for `PairingFailureLedger`: the unix times of recent failures.
public protocol PairingFailureStore: AnyObject {
    func load() throws -> [Double]
    func save(_ failures: [Double]) throws
}

public final class MemoryPairingFailureStore: PairingFailureStore {
    private var failures: [Double] = []
    public init() {}
    public func load() throws -> [Double] { failures }
    public func save(_ failures: [Double]) throws { self.failures = failures }
}

/// JSON array of unix seconds, created 0600 and replaced atomically. A missing
/// file is an empty list; an unreadable one throws (the ledger then fails
/// closed).
public final class FilePairingFailureStore: PairingFailureStore {
    private let url: URL
    public init(url: URL) { self.url = url }

    public func load() throws -> [Double] {
        guard FileManager.default.fileExists(atPath: url.path) else { return [] }
        let list = try JSONDecoder().decode([Double].self, from: Data(contentsOf: url))
        guard list.count <= 10_000, list.allSatisfy({ $0.isFinite && $0 >= 0 }) else {
            throw CocoaError(.fileReadCorruptFile)
        }
        return list
    }

    public func save(_ failures: [Double]) throws {
        try AtomicFile.write(try JSONEncoder().encode(failures), to: url)
    }
}

/// Failed pairing codes across windows. One window allows three failures, but
/// a window is one click away, so an attacker (or a patient guesser) could
/// still collect tickets at 10^-6 each by waiting for the owner to open
/// windows. After `ChannelLimits.pairingFailureCap` (9) failures within
/// `pairingFailureHorizon` (24 h) pairing is locked until the 9th most recent
/// failure is older than the horizon. The cooldown is persisted, so a restart
/// does not clear it, and the app shows `lockoutRemaining` to the owner.
///
/// The clock is the wall clock (unix seconds): the ledger must survive
/// restarts. A clock set back only makes the lock longer. An unreadable store
/// counts as a full ledger (fail closed); `reset()` is the owner's deliberate
/// local override and is not reachable from the network.
public final class PairingFailureLedger {
    private let store: PairingFailureStore
    private let wall: () -> Double
    private var failures: [Double]

    public init(store: PairingFailureStore, wall: @escaping () -> Double = { Date().timeIntervalSince1970 }) {
        self.store = store; self.wall = wall
        if let loaded = try? store.load() { failures = loaded } else {
            let now = wall()
            failures = Array(repeating: now, count: ChannelLimits.pairingFailureCap)
            try? store.save(failures)
        }
    }

    /// Seconds until pairing may be tried again, nil when it may be tried now.
    public var lockoutRemaining: Double? {
        let now = wall()
        let recent = failures.filter { $0 > now - ChannelLimits.pairingFailureHorizon }.sorted(by: >)
        guard recent.count >= ChannelLimits.pairingFailureCap else { return nil }
        let until = recent[ChannelLimits.pairingFailureCap - 1] + ChannelLimits.pairingFailureHorizon
        return until > now ? until - now : nil
    }

    public var isLockedOut: Bool { lockoutRemaining != nil }

    public func recordFailure() {
        let now = wall()
        failures = failures.filter { $0 > now - ChannelLimits.pairingFailureHorizon } + [now]
        // If it cannot be stored the failure still counts for this run.
        try? store.save(failures)
    }

    /// The owner clears the cooldown on purpose, from the local UI.
    public func reset() {
        failures = []
        try? store.save(failures)
    }
}
