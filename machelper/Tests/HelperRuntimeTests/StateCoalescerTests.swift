import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// The runtime queue can flip the visible state on every packet. The main
/// thread (hotkey, "Stop now", menu) must not be buried under those updates.
final class StateCoalescerTests: XCTestCase {
    final class Counter: @unchecked Sendable {
        let lock = NSLock()
        var calls = 0, last: RuntimeState?
        func hit(_ s: RuntimeState) { lock.lock(); calls += 1; last = s; lock.unlock() }
        var snapshot: (Int, RuntimeState?) { lock.lock(); defer { lock.unlock() }; return (calls, last) }
    }

    func now() -> Double { ProcessInfo.processInfo.systemUptime }

    func testAFloodOfStateFlipsBecomesBoundedMainQueueWorkAndTheKillStillLands() throws {
        let rig = RuntimeRig()
        let fakeMain = DispatchQueue(label: "fake.main")
        let seen = Counter()
        let interval = 0.1
        let c = rig.runtime.observeState(on: fakeMain, minInterval: interval) { s in
            seen.hit(s)
            // What apply() costs on the real main thread: image, menu text, badge.
            let end = ProcessInfo.processInfo.systemUptime + 0.0002
            while ProcessInfo.processInfo.systemUptime < end {}
        }
        // Counted under a lock: the kill below publishes from the runtime queue.
        let rawCount = Counter()
        rig.runtime.onState = { [prior = rig.runtime.onState] s in rawCount.hit(s); prior?(s) }
        rig.runtime.setAllowRawMouse(true)
        let client = try rig.connect()
        rig.run(0.2)
        let started = now()

        // The peer alternates "allowed" and "denied" on every packet.
        for i in 0..<4000 {
            rig.session.locked = true
            rig.runtime.refreshEnvironment()
            rig.mono += 0.001; rig.wall = rig.wall.addingTimeInterval(0.001)
            rig.send(client, InputPacket(type: .input, dx: 16, dy: 0, seqLocal: UInt32(i)))
            rig.session.locked = false
            rig.runtime.refreshEnvironment()
            rig.mono += 0.001; rig.wall = rig.wall.addingTimeInterval(0.001)
            rig.send(client, InputPacket(type: .input, dx: 16, dy: 0, seqLocal: UInt32(i)))
        }
        let floodSeconds = now() - started
        let raw = rawCount.snapshot.0
        XCTAssertGreaterThan(raw, 2000, "the flood really flipped the state (\(raw) updates)")

        // The hotkey handler is a block on the main queue: it must run at once.
        let queued = now()
        let ran = DispatchSemaphore(value: 0)
        var latency = 0.0
        fakeMain.async {
            latency = ProcessInfo.processInfo.systemUptime - queued
            rig.runtime.emergencyStop(.kill)
            ran.signal()
        }
        XCTAssertEqual(ran.wait(timeout: .now() + 2), .success)
        XCTAssertLessThan(latency, 0.1, "the kill handler waited \(latency) s behind UI updates")
        XCTAssertFalse(rig.runtime.allowRawMouse)

        // Bounded work: at most one delivery per interval (plus the first), never one per flip.
        let (calls, _) = seen.snapshot
        let bound = Int(floodSeconds / interval) + 3
        XCTAssertLessThanOrEqual(calls, bound, "\(calls) deliveries for \(raw) updates in \(floodSeconds) s")
        XCTAssertLessThan(calls, raw / 10)
        XCTAssertEqual(c.deliveries, calls)
    }

    func testTheNewestStateIsAlwaysDeliveredLast() {
        let q = DispatchQueue(label: "fake.main2")
        let seen = Counter()
        let c = StateCoalescer<RuntimeState>(target: q, minInterval: 0.05) { seen.hit($0) }
        for i in 0..<100 {
            c.push(RuntimeState(allowRawMouse: i % 2 == 0, device: "d\(i)", injecting: false, lastDeny: nil))
        }
        q.sync {}  // nothing but the pending delivery is queued behind this
        Thread.sleep(forTimeInterval: 0.2)
        q.sync {}
        let (calls, last) = seen.snapshot
        XCTAssertLessThanOrEqual(calls, 2)
        XCTAssertEqual(last?.device, "d99", "the trailing update is never lost")
        c.push(RuntimeState(allowRawMouse: true, device: "later", injecting: true, lastDeny: nil))
        Thread.sleep(forTimeInterval: 0.2)
        q.sync {}
        XCTAssertEqual(seen.snapshot.1?.device, "later", "pushing after a quiet spell still delivers")
    }
}
