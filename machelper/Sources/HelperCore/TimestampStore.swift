import Foundation

/// Newest accepted handshake timestamp per paired device. It survives a helper
/// restart, so a captured initiation cannot be replayed inside the 300 s clock
/// window just because the in-memory table was lost.
public enum TimestampStoreError: Error, Equatable {
    /// There is no stored table at all: the first run (or the file was removed).
    /// The channel then starts a floor at "now" instead of trusting nothing.
    case missing
}

public protocol TimestampStore: AnyObject {
    /// Throws `TimestampStoreError.missing` when nothing was ever stored.
    func load() throws -> [Data: Data]
    /// Must be durable before it returns; a throw makes the channel refuse the handshake.
    func save(peer: Data, timestamp: Data) throws
    /// Called when `load` failed: set the unreadable data aside and start empty.
    /// Throwing keeps the channel failing closed.
    func moveCorruptAside() throws
}

extension TimestampStore {
    /// Default: no recovery possible, so the channel keeps failing closed.
    /// Stores that can recover override it.
    public func moveCorruptAside() throws { throw CocoaError(.fileWriteUnknown) }
}

public final class MemoryTimestampStore: TimestampStore {
    private var table: [Data: Data] = [:]
    public init() {}
    public func load() throws -> [Data: Data] { table }
    public func save(peer: Data, timestamp: Data) throws { table[peer] = timestamp }
    public func moveCorruptAside() throws { table = [:] }
}

/// JSON file {peerKeyHex: timestampHex}, replaced atomically, created with
/// mode 0600 (see `AtomicFile`). A missing file throws `.missing`, which the
/// channel treats as a first run: it stores a "now" floor at once, so a
/// datagram captured before the file existed (or before it was deleted) is
/// not replayable. An unreadable or malformed one throws, and
/// the channel then moves it aside (`moveCorruptAside`) and restarts with a
/// fresh floor, or refuses handshakes if even that fails.
public final class FileTimestampStore: TimestampStore {
    private let url: URL
    private var table: [Data: Data]?

    public init(url: URL) { self.url = url }

    public func load() throws -> [Data: Data] {
        if let table { return table }
        guard FileManager.default.fileExists(atPath: url.path) else { throw TimestampStoreError.missing }
        let raw = try JSONDecoder().decode([String: String].self, from: Data(contentsOf: url))
        var out: [Data: Data] = [:]
        for (k, v) in raw {
            guard let kd = Data(hex: k), let vd = Data(hex: v), kd.count == 32, vd.count == TAI64N.size else {
                throw CocoaError(.fileReadCorruptFile)
            }
            out[kd] = vd
        }
        table = out
        return out
    }

    /// Renames the file to `<name>.corrupt-<unix seconds>` (mode untouched) and
    /// forgets any cached table. The next `save` writes a fresh file.
    public func moveCorruptAside() throws {
        let aside = url.deletingLastPathComponent().appendingPathComponent(
            url.lastPathComponent + ".corrupt-\(Int(Date().timeIntervalSince1970))")
        try? FileManager.default.removeItem(at: aside)
        if FileManager.default.fileExists(atPath: url.path) {
            try FileManager.default.moveItem(at: url, to: aside)
        }
        table = [:]
    }

    public func save(peer: Data, timestamp: Data) throws {
        var t: [Data: Data]
        do { t = try load() } catch TimestampStoreError.missing { t = [:] }
        t[peer] = timestamp
        let raw = Dictionary(uniqueKeysWithValues: t.map { ($0.key.hex, $0.value.hex) })
        let data = try JSONEncoder().encode(raw)
        try AtomicFile.write(data, to: url)
        table = t
    }
}
