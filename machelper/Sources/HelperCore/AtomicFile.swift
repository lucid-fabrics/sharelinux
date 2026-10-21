import Foundation

/// Writes a file that is never readable by anyone but the owner, not even for
/// an instant: the temp file is created with mode 0600 in the same directory
/// (`open` with the mode, `O_EXCL`, so no umask-then-chmod gap and no reuse of
/// a planted file), written, flushed to disk, then renamed over the target.
public enum AtomicFile {
    /// - Parameter observeTemp: test hook, called with the temp file's path
    ///   before any data is written.
    public static func write(_ data: Data, to url: URL, observeTemp: ((String) -> Void)? = nil) throws {
        let dir = url.deletingLastPathComponent().path
        let tmp = dir + "/." + url.lastPathComponent + ".tmp-" + UUID().uuidString
        let fd = open(tmp, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0o600)
        guard fd >= 0 else { throw CocoaError(.fileWriteUnknown) }
        var committed = false
        defer {
            close(fd)
            if !committed { unlink(tmp) }
        }
        observeTemp?(tmp)
        try data.withUnsafeBytes { (buf: UnsafeRawBufferPointer) in
            var off = 0
            while off < buf.count {
                let n = Darwin.write(fd, buf.baseAddress! + off, buf.count - off)
                if n < 0 { if errno == EINTR { continue }; throw CocoaError(.fileWriteUnknown) }
                off += n
            }
        }
        guard fsync(fd) == 0, rename(tmp, url.path) == 0 else { throw CocoaError(.fileWriteUnknown) }
        committed = true
    }
}
