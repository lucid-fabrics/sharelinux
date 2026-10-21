import XCTest
@testable import HelperCore

final class ReplayWindowTests: XCTestCase {
    func testFirstCounterZeroIsAccepted() {
        var window = ReplayWindow()

        XCTAssertTrue(window.check(0))
        XCTAssertTrue(window.commit(0))
        XCTAssertFalse(window.check(0))
    }

    func testDuplicateCounterIsRejected() {
        var window = ReplayWindow()

        XCTAssertTrue(window.commit(42))
        XCTAssertFalse(window.check(42))
        XCTAssertFalse(window.commit(42))
    }

    func testOutOfOrderCounterWithinWindowIsAcceptedOnlyOnce() {
        var window = ReplayWindow()

        XCTAssertTrue(window.commit(2_047))
        XCTAssertTrue(window.check(0))
        XCTAssertTrue(window.commit(0))
        XCTAssertFalse(window.check(0))
        XCTAssertFalse(window.commit(0))
    }

    func testCounterOlderThanWindowIsRejected() {
        var window = ReplayWindow()

        XCTAssertTrue(window.commit(3_000))
        XCTAssertFalse(window.check(952))
        XCTAssertFalse(window.commit(952))
    }

    func testLargeJumpClearsWindowAndRejectsOldCounters() {
        var window = ReplayWindow()

        XCTAssertTrue(window.commit(0))
        XCTAssertTrue(window.commit(2_048))
        XCTAssertFalse(window.check(0))
        XCTAssertFalse(window.commit(0))
    }

    func testCheckDoesNotMutateWindow() {
        var window = ReplayWindow()

        XCTAssertTrue(window.check(123))
        XCTAssertTrue(window.check(123))
        XCTAssertTrue(window.commit(123))
        XCTAssertFalse(window.check(123))
    }

    func testCommitOfRejectedCounterReturnsFalseWithoutMutatingWindow() {
        var window = ReplayWindow()

        XCTAssertTrue(window.commit(10_000))
        XCTAssertFalse(window.commit(0))
        XCTAssertTrue(window.commit(9_999))
    }

    func testMaximumCounterFollowedBySmallerCounterDoesNotOverflow() {
        var window = ReplayWindow()

        XCTAssertTrue(window.commit(UInt64.max - 1))
        XCTAssertTrue(window.commit(UInt64.max - 2))
        XCTAssertFalse(window.check(UInt64.max - 2))
        XCTAssertFalse(window.commit(UInt64.max - 2))
    }

    func testRateLimiterAllowsFiveRequestsThenDeniesSixth() {
        var limiter = RateLimiter(perSecond: 5)

        for index in 0..<5 {
            XCTAssertTrue(limiter.allow(now: 0), "Request \(index) should be allowed")
        }
        XCTAssertFalse(limiter.allow(now: 0))
    }

    func testRateLimiterRefillsAfterOneSecond() {
        var limiter = RateLimiter(perSecond: 5)

        for _ in 0..<5 {
            XCTAssertTrue(limiter.allow(now: 10))
        }
        XCTAssertFalse(limiter.allow(now: 10))
        XCTAssertTrue(limiter.allow(now: 11))
    }

    func testRateLimiterBackwardsClockDoesNotMintTokens() {
        var limiter = RateLimiter(perSecond: 5)

        for _ in 0..<5 {
            XCTAssertTrue(limiter.allow(now: 10))
        }

        XCTAssertFalse(limiter.allow(now: 9))
        XCTAssertFalse(limiter.allow(now: 10))
        XCTAssertTrue(limiter.allow(now: 11))
    }
}
