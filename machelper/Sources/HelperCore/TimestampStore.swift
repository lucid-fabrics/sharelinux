import Foundation

/// Newest accepted handshake timestamp per paired device. It survives a helper
/// restart, so a captured initiation cannot be replayed inside the 300 s clock
/// window just because the in-memory table was lost.
public protocol TimestampStore: AnyObject {
    func load() throws -> [Data: Data]
    /// Must be durable before it returns; a throw makes the channel refuse the handshake.
    func save(peer: Data, timestamp: Data) throws
}

public final class MemoryTimestampStore: TimestampStore {
    private var table: [Data: Data] = [:]
    public init() {}
    public func load() throws -> [Data: Data] { table }
    public func save(peer: Data, timestamp: Data) throws { table[peer] = timestamp }
}

/// JSON file {peerKeyHex: timestampHex}, replaced atomically, mode 0600. A
/// missing file is an empty table; an unreadable or malformed one throws, and
/// the channel then refuses handshakes rather than guess.
public final class FileTimestampStore: TimestampStore {
    private let url: URL
    private var table: [Data: Data]?

    public init(url: URL) { self.url = url }

    public func load() throws -> [Data: Data] {
        if let table { return table }
        guard FileManager.default.fileExists(atPath: url.path) else { table = [:]; return [:] }
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

    public func save(peer: Data, timestamp: Data) throws {
        var t = try load()
        t[peer] = timestamp
        let raw = Dictionary(uniqueKeysWithValues: t.map { ($0.key.hex, $0.value.hex) })
        let data = try JSONEncoder().encode(raw)
        try data.write(to: url, options: .atomic)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
        table = t
    }
}
