import Darwin
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// A poster that can be read from any thread and remembers when each button-up happened.
final class SyncPoster: EventPoster, @unchecked Sendable {
    private let lock = NSLock()
    private var entries: [(at: Double, text: String)] = []
    func add(_ t: String) { lock.lock(); entries.append((ProcessInfo.processInfo.systemUptime, t)); lock.unlock() }
    func postMove(dx: Int, dy: Int, held: ButtonMask) { add("move") }
    func postButton(_ button: MouseButton, down: Bool, clickState: Int) { add(down ? "down" : "up") }
    func postScroll(dx: Int, dy: Int) { add("scroll") }
    var count: Int { lock.lock(); defer { lock.unlock() }; return entries.count }
    var texts: [String] { lock.lock(); defer { lock.unlock() }; return entries.map(\.text) }
    func firstUp(after t: Double) -> Double? {
        lock.lock(); defer { lock.unlock() }
        return entries.first { $0.text == "up" && $0.at >= t }?.at
    }
}

/// The kill switch and the watchdogs under attack: a flood on the UDP socket
/// (the socket is read on the same queue as the timer), and a runtime queue
/// that is busy or stuck. Real clocks, a real loopback socket, a fake poster.
final class KillSwitchFloodTests: XCTestCase {
    static let source = PeerAddress(bytes: [127, 0, 0, 1])!
    let macKey = PrivKey(), linuxKey = PrivKey()
    let poster = SyncPoster()
    var runtime: HelperRuntime!
    var server: UDPServer!
    var port: UInt16 = 0
    var client: TransportSession!
    var floodStop = false
    let floodLock = NSLock()
    var floodThreads: [Thread] = []

    override func setUp() async throws {
        let session = FakeSessionSource(), sharing = FakeSharingSource(), access = FakeAccessSource()
        sharing.current = [Self.source]
        runtime = HelperRuntime(
            staticKey: macKey, pairedKeys: { [linuxKey] in [linuxKey.publicKey.rawRepresentation] }, poster: poster,
            session: session, sharing: sharing, access: access, ownUID: 501,
            clock: ChannelClock(monotonic: { ProcessInfo.processInfo.systemUptime }, wall: { Date() }),
            timestamps: MemoryTimestampStore())
        server = UDPServer(runtime: runtime, port: 0, loopbackOnly: true, interval: 0.05)
        port = try server.start()
        runtime.queue.sync { runtime.setAllowRawMouse(true) }
        Thread.sleep(forTimeInterval: 0.15)  // a few environment samples
        client = try handshake()
    }

    override func tearDown() async throws {
        stopFlood()
        server.stop()
    }

    final class Box<T>: @unchecked Sendable { var value: T? }

    /// Runs `body` on the runtime queue and waits at most `timeout`; nil when the
    /// queue did not get to it (starved or stuck). Never blocks the test forever.
    func onQueue<T>(timeout: Double = 3, _ body: @escaping () -> T) -> T? {
        let box = Box<T>(), done = DispatchSemaphore(value: 0)
        runtime.queue.async { box.value = body(); done.signal() }
        return done.wait(timeout: .now() + timeout) == .success ? box.value : nil
    }

    func now() -> Double { ProcessInfo.processInfo.systemUptime }

    func handshake() throws -> TransportSession {
        let hs = try HandshakeState(
            pattern: .ik, initiator: true, staticKey: linuxKey, remoteStatic: macKey.publicKey.rawRepresentation)
        let m = try hs.writeMessage(payload: TAI64N.encode(Date()))
        let dg = Mac1.append(
            to: Data([DatagramKind.ikInitiation.rawValue]) + m, macPublic: macKey.publicKey.rawRepresentation)
        let reply = try XCTUnwrap(runtime.queue.sync { runtime.handle(datagram: dg, from: Self.source) })
        _ = try hs.readMessage(reply.dropFirst())
        let t = TransportSession(keys: try hs.split(), now: now())
        send(t, InputPacket(type: .keepalive))
        return t
    }

    @discardableResult
    func send(_ t: TransportSession, _ p: InputPacket) -> Data? {
        let dg = try! t.seal(p.encode(), now: now())
        return runtime.queue.sync { runtime.handle(datagram: dg, from: Self.source) }
    }

    /// Presses the left button through the real path and checks it is down.
    func holdLeft() {
        send(client, InputPacket(type: .input, buttons: [.left], dx: 16, dy: 0))
        XCTAssertEqual(runtime.guardedPoster.buttonsDown, [.left])
        XCTAssertTrue(poster.texts.contains("down"))
    }

    // MARK: flood

    /// Junk shaped like transport packets, so each one costs an AEAD attempt
    /// against the live session before it is dropped.
    func startFlood(threads: Int = 6) {
        floodStop = false
        for _ in 0..<threads {
            let t = Thread { [self] in
                let s = socket(AF_INET, SOCK_DGRAM, 0)
                defer { close(s) }
                var a = sockaddr_in()
                a.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
                a.sin_family = sa_family_t(AF_INET)
                a.sin_port = port.bigEndian
                a.sin_addr.s_addr = UInt32(0x7f00_0001).bigEndian
                var junk = [UInt8](repeating: 0xAB, count: 120)
                var sent = 0
                while true {
                    if sent % 256 == 0 {
                        floodLock.lock(); let stop = floodStop; floodLock.unlock()
                        if stop { return }
                    }
                    junk[0] = DatagramKind.transport.rawValue
                    junk[1] = UInt8(truncatingIfNeeded: sent)
                    _ = withUnsafePointer(to: &a) {
                        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                            sendto(s, junk, junk.count, 0, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
                        }
                    }
                    sent += 1
                }
            }
            t.start()
            floodThreads.append(t)
        }
    }

    func stopFlood() {
        floodLock.lock(); floodStop = true; floodLock.unlock()
        while floodThreads.contains(where: { !$0.isFinished }) { Thread.sleep(forTimeInterval: 0.01) }
        floodThreads = []
    }

    func waitFor(_ seconds: Double, _ condition: () -> Bool) -> Bool {
        let end = now() + seconds
        while now() < end {
            if condition() { return true }
            Thread.sleep(forTimeInterval: 0.001)
        }
        return condition()
    }

    // MARK: tests

    /// The kill switch must not queue behind a flood: the button is up within
    /// 100 ms of the call, posting stops, and nothing is posted afterwards even
    /// though the flood goes on and the queue is still draining it.
    func testKillSwitchReleasesWithin100msUnderAFloodAndStopsPosting() {
        holdLeft()
        startFlood()
        Thread.sleep(forTimeInterval: 0.25)  // the queue is saturated by now
        let t0 = now()
        runtime.emergencyStop(.kill)
        XCTAssertFalse(runtime.allowRawMouse, "the switch is off as soon as the call returns")
        let up = waitFor(0.1) { self.poster.firstUp(after: t0) != nil }
        XCTAssertTrue(up, "button-up within 100 ms of the kill switch under a flood")
        if let at = poster.firstUp(after: t0) { XCTAssertLessThan(at - t0, 0.1) }
        XCTAssertEqual(runtime.guardedPoster.buttonsDown, [])
        let frozen = poster.count
        // Authenticated traffic keeps coming too: none of it may post.
        for _ in 0..<20 {
            let dg = try! client.seal(InputPacket(type: .input, buttons: [.left], dx: 400, dy: 400, sx: 5, sy: 5).encode(), now: now())
            runtime.queue.async { _ = self.runtime.handle(datagram: dg, from: Self.source) }
        }
        Thread.sleep(forTimeInterval: 0.3)
        XCTAssertEqual(poster.count, frozen, "nothing posted after the kill switch")
        stopFlood()
        // The queue caught up: the session is over and the block is lifted again.
        XCTAssertTrue(waitFor(2) { !self.runtime.guardedPoster.isStopped })
        XCTAssertNil(onQueue { self.runtime.state.device } ?? "starved")
        XCTAssertFalse(runtime.allowRawMouse)
    }

    /// The silence watchdog and the sampling timer live on the queue the flood
    /// tries to starve: a held button must still come up after one second of
    /// silence, flood or not.
    func testSilenceWatchdogStillFiresUnderAFlood() {
        holdLeft()
        let t0 = now()
        startFlood()
        let up = waitFor(ChannelLimits.silenceTimeout + 1.0) { self.poster.firstUp(after: t0) != nil }
        XCTAssertTrue(up, "button released by the watchdog while the socket is flooded")
        if let at = poster.firstUp(after: t0) {
            XCTAssertLessThan(at - t0, ChannelLimits.silenceTimeout + 0.5)
        }
    }

    /// A lost environment (here: the screen locking) is noticed by the sampling
    /// timer even while the socket is flooded.
    func testEnvironmentSamplingStillRunsUnderAFlood() {
        let before = onQueue { self.runtime.monitor.snapshot?.at ?? 0 } ?? 0
        startFlood()
        Thread.sleep(forTimeInterval: 0.5)
        let after = onQueue { self.runtime.monitor.snapshot?.at ?? 0 }
        XCTAssertNotNil(after, "the queue answered under the flood")
        XCTAssertGreaterThan((after ?? 0) - before, 0.3, "the snapshot kept refreshing")
    }

    /// Behaviour, not constants: with a real socket under a sustained flood no
    /// read pass takes more than the cap, passes are many (the flood needs
    /// them), the sampling timer keeps firing in between, and work queued on
    /// the runtime queue is not kept waiting for the whole flood.
    func testAFloodIsReadInBoundedPassesWhileTimersAndQueuedWorkRun() {
        server.stop()
        // A fresh server with a fast timer and a pass recorder.
        final class Log: @unchecked Sendable {
            let lock = NSLock()
            var passes: [Int] = []
            var timerFires = 0
            func pass(_ n: Int) { lock.lock(); passes.append(n); lock.unlock() }
            func fire() { lock.lock(); timerFires += 1; lock.unlock() }
            var snapshot: ([Int], Int) { lock.lock(); defer { lock.unlock() }; return (passes, timerFires) }
        }
        let log = Log()
        server = UDPServer(
            runtime: runtime, port: 0, loopbackOnly: true, interval: 0.02,
            samplingEnabled: { log.fire(); return true })
        server.onPass = { log.pass($0) }
        port = try! server.start()
        startFlood()
        var worst = 0.0
        let t0 = now()
        while now() - t0 < 0.5 {
            let queued = now()
            let ran = DispatchSemaphore(value: 0)
            var latency = 0.0
            runtime.queue.async { latency = self.now() - queued; ran.signal() }
            XCTAssertEqual(ran.wait(timeout: .now() + 2), .success, "queued work ran during the flood")
            worst = max(worst, latency)
            Thread.sleep(forTimeInterval: 0.02)
        }
        stopFlood()
        let (passes, fires) = log.snapshot
        let heavy = passes.filter { $0 > 0 }
        XCTAssertGreaterThan(heavy.count, 20, "the flood needed many passes")
        XCTAssertLessThanOrEqual(passes.max() ?? 0, UDPServer.maxDatagramsPerPass, "no pass reads more than the cap")
        XCTAssertGreaterThanOrEqual(fires, 10, "the sampling timer kept firing during the flood (\(fires))")
        XCTAssertLessThan(worst, 0.1, "work queued behind the flood waited \(worst) s")
    }

    // MARK: a stuck or busy runtime queue

    func testHotkeyReleasesAtOnceWhileTheRuntimeQueueIsBlocked() {
        holdLeft()
        let gate = DispatchSemaphore(value: 0)
        runtime.queue.async { gate.wait() }  // stuck
        defer { gate.signal() }
        let t0 = now()
        runtime.emergencyStop(.kill)
        XCTAssertLessThan(now() - t0, 0.05, "the call itself does not wait for the queue")
        XCTAssertNotNil(poster.firstUp(after: t0), "button-up was posted by the calling thread")
        XCTAssertEqual(runtime.guardedPoster.buttonsDown, [])
        XCTAssertTrue(runtime.guardedPoster.isStopped, "posting stays off until the queue has ended the session")
    }

    /// Packets that were already queued behind a busy queue when the stop came
    /// must not post: the switch is still ON (sleep keeps it), the session is
    /// live and the gate accepts them, so only the poster's own stop flag
    /// holds them back. Deleting the guard on moves, presses or scrolls fails here.
    func testPacketsQueuedBeforeAStopDoNotPostWhileTheStopIsPending() {
        let gate = DispatchSemaphore(value: 0)
        runtime.queue.async { gate.wait() }
        let before = poster.count
        for p in [
            InputPacket(type: .input, dx: 64, dy: 32),
            InputPacket(type: .input, buttons: [.left], dx: 16, dy: 0),
            InputPacket(type: .input, buttons: [.left], sx: 40, sy: 40),
        ] {
            let dg = try! client.seal(p.encode(), now: now())
            runtime.queue.async { _ = self.runtime.handle(datagram: dg, from: Self.source) }
        }
        runtime.emergencyStop(.sleep)  // the flag is up before the queued packets run
        XCTAssertTrue(runtime.allowRawMouse, "sleep keeps the switch: the gate would accept these packets")
        gate.signal()
        XCTAssertTrue(waitFor(2) { !self.runtime.guardedPoster.isStopped })
        XCTAssertEqual(poster.count, before, "no move, press or scroll got through the pending stop")
        XCTAssertEqual(poster.texts.suffix(from: min(before, poster.texts.count)).filter { $0 != "up" }, [])
    }

    func testSleepAndQuitNeverBlockOnTheQueueAndStillRelease() {
        holdLeft()
        let gate = DispatchSemaphore(value: 0)
        runtime.queue.async { gate.wait() }
        let t0 = now()
        let finished = runtime.emergencyStop(.sleep, wait: 0.3)
        let elapsed = now() - t0
        XCTAssertFalse(finished)
        XCTAssertGreaterThanOrEqual(elapsed, 0.25)
        XCTAssertLessThan(elapsed, 1.0, "bounded by the timeout, not by the stuck queue")
        XCTAssertNotNil(poster.firstUp(after: t0), "the release happened although the queue never answered")
        gate.signal()
        XCTAssertTrue(waitFor(2) { !self.runtime.guardedPoster.isStopped }, "the queue finishes the stop later")
        XCTAssertNil(onQueue { self.runtime.state.device } ?? "starved")
    }

    func assertEmergencyStops(_ kind: EmergencyKind) {
        if kind != .kill { runtime.queue.sync { runtime.setAllowRawMouse(true) } }
        holdLeft()
        let t0 = now()
        XCTAssertTrue(runtime.emergencyStop(kind, wait: 2), "\(kind)")
        XCTAssertNotNil(poster.firstUp(after: t0), "\(kind)")
        XCTAssertEqual(runtime.guardedPoster.buttonsDown, [], "\(kind)")
        XCTAssertEqual(onQueue { self.runtime.controller.held }, [], "\(kind)")
        XCTAssertFalse(runtime.guardedPoster.isStopped, "\(kind): block lifted once the queue handled it")
    }

    func testKillStopsAndReleases() { assertEmergencyStops(.kill) }
    func testSleepStopsAndReleases() { assertEmergencyStops(.sleep) }
    func testLockStopsAndReleases() { assertEmergencyStops(.lock) }
    func testSessionLostStopsAndReleases() { assertEmergencyStops(.sessionLost) }
}
