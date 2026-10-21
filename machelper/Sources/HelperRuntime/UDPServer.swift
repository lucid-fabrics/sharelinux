import Foundation
import HelperCore
import os

public enum UDPServerError: Error { case socket(Int32), bind(Int32) }

/// The helper's UDP endpoint (default port 5961): one dual-stack socket read on
/// the runtime's queue, plus the timer that drives the runtime's environment
/// sampling and watchdogs.
///
/// Unauthenticated traffic gets no reply (the channel decides that); oversized
/// datagrams are dropped before they reach it. Nothing here logs payloads.
public final class UDPServer {
    public static let defaultPort: UInt16 = 5961
    private let runtime: HelperRuntime
    private let requestedPort: UInt16
    private let loopbackOnly: Bool
    private let interval: Double
    /// The timer refreshes the environment only while this returns true (the
    /// harness uses it to simulate a stalled poller; the app always allows).
    private let samplingEnabled: () -> Bool
    private var fd: Int32 = -1
    private var readSource: DispatchSourceRead?
    private var timer: DispatchSourceTimer?
    private let log = Logger(subsystem: "io.github.sharelinux.mousehelper", category: "udp")

    public init(
        runtime: HelperRuntime, port: UInt16 = UDPServer.defaultPort, loopbackOnly: Bool = false,
        interval: Double = 0.1, samplingEnabled: @escaping () -> Bool = { true }
    ) {
        self.runtime = runtime; self.requestedPort = port; self.loopbackOnly = loopbackOnly
        self.interval = interval; self.samplingEnabled = samplingEnabled
    }

    /// Binds and starts. Returns the bound port (useful with port 0). The normal
    /// socket is dual-stack IPv6; `loopbackOnly` (tests) binds IPv4 127.0.0.1 only.
    @discardableResult
    public func start() throws -> UInt16 {
        let family = loopbackOnly ? AF_INET : AF_INET6
        let s = socket(family, SOCK_DGRAM, 0)
        guard s >= 0 else { throw UDPServerError.socket(errno) }
        if !loopbackOnly {
            var off: Int32 = 0
            setsockopt(s, IPPROTO_IPV6, IPV6_V6ONLY, &off, socklen_t(MemoryLayout<Int32>.size))
        }
        _ = fcntl(s, F_SETFL, fcntl(s, F_GETFL) | O_NONBLOCK)
        var storage = sockaddr_storage()
        var length = socklen_t(0)
        if loopbackOnly {
            var a4 = sockaddr_in()
            a4.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
            a4.sin_family = sa_family_t(AF_INET)
            a4.sin_port = requestedPort.bigEndian
            a4.sin_addr.s_addr = UInt32(0x7f00_0001).bigEndian
            length = socklen_t(MemoryLayout<sockaddr_in>.size)
            withUnsafeBytes(of: &a4) { _ = memcpy(&storage, $0.baseAddress!, $0.count) }
        } else {
            var a6 = sockaddr_in6()
            a6.sin6_len = UInt8(MemoryLayout<sockaddr_in6>.size)
            a6.sin6_family = sa_family_t(AF_INET6)
            a6.sin6_port = requestedPort.bigEndian
            a6.sin6_addr = in6addr_any
            length = socklen_t(MemoryLayout<sockaddr_in6>.size)
            withUnsafeBytes(of: &a6) { _ = memcpy(&storage, $0.baseAddress!, $0.count) }
        }
        let bound = withUnsafePointer(to: &storage) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(s, $0, length) }
        }
        guard bound == 0 else { let e = errno; close(s); throw UDPServerError.bind(e) }
        var got = sockaddr_storage()
        var gotLength = socklen_t(MemoryLayout<sockaddr_storage>.size)
        _ = withUnsafeMutablePointer(to: &got) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { getsockname(s, $0, &gotLength) }
        }
        let boundPort: UInt16 = withUnsafeBytes(of: &got) { raw in
            // sin_port and sin6_port are both at offset 2.
            UInt16(bigEndian: raw.load(fromByteOffset: 2, as: UInt16.self))
        }
        fd = s

        let src = DispatchSource.makeReadSource(fileDescriptor: s, queue: runtime.queue)
        src.setEventHandler { [weak self] in self?.drain() }
        src.setCancelHandler { close(s) }
        readSource = src
        src.resume()

        let t = DispatchSource.makeTimerSource(queue: runtime.queue)
        t.schedule(deadline: .now() + interval, repeating: interval, leeway: .milliseconds(5))
        t.setEventHandler { [weak self] in
            guard let self else { return }
            if self.samplingEnabled() { self.runtime.refreshEnvironment() }
            self.runtime.tick()
        }
        timer = t
        t.resume()
        log.info("listening on UDP \(boundPort)")
        return boundPort
    }

    public func stop() {
        timer?.cancel(); timer = nil
        readSource?.cancel(); readSource = nil
        fd = -1
    }

    /// Reads everything waiting on the socket.
    private func drain() {
        var buf = [UInt8](repeating: 0, count: 2048)
        while true {
            var from = sockaddr_storage()
            var fromLen = socklen_t(MemoryLayout<sockaddr_storage>.size)
            let n = withUnsafeMutablePointer(to: &from) { p in
                p.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                    recvfrom(fd, &buf, buf.count, 0, $0, &fromLen)
                }
            }
            if n < 0 { return }  // EAGAIN: drained (any other error also ends this round)
            guard n > 0, n <= WireLimits.maxDatagram else { continue }
            let family = Int32(from.ss_family)
            let bytes: [UInt8]? = withUnsafeBytes(of: &from) { raw in
                switch family {
                case AF_INET: return Array(raw[4..<8])  // sin_addr
                case AF_INET6: return clearLinkLocalScope(Array(raw[8..<24]))  // sin6_addr
                default: return nil
                }
            }
            guard let bytes, let source = PeerAddress(bytes: bytes) else { continue }
            if let reply = runtime.handle(datagram: Data(buf[0..<n]), from: source) {
                reply.withUnsafeBytes { r in
                    withUnsafePointer(to: &from) { p in
                        p.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                            _ = sendto(fd, r.baseAddress, r.count, 0, $0, fromLen)
                        }
                    }
                }
            }
        }
    }
}
