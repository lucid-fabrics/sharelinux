import XCTest

@testable import HelperCore

final class DeviceIdentityTests: XCTestCase {
    func testLookAlikeNamesShareASkeleton() {
        let pairs: [(String, String)] = [
            ("Desk PC", "Desk \u{0420}\u{0421}"),  // Cyrillic Er and Es
            ("Desk PC", "desk pc"),
            ("Desk PC", "Desk-PC"),
            ("Desk PC", "DeskPC"),
            ("laptop", "Iaptop"),  // capital i
            ("laptop", "1aptop"),
            ("modern", "rnodern"),
            ("Cafe", "Caf\u{00E9}"),  // accent
            ("Desk PC", "\u{FF24}esk PC"),  // fullwidth D
            ("Office", "0ffice"),
            ("Gaming", "Gamin\u{0261}"),  // Latin script g
        ]
        for (a, b) in pairs {
            XCTAssertTrue(DeviceIdentity.looksAlike(a, b), "\(a) / \(b)")
            XCTAssertTrue(DeviceIdentity.looksAlike(b, a), "\(b) / \(a)")
        }
    }

    func testDifferentNamesStayDifferent() {
        let pairs: [(String, String)] = [
            ("Desk PC", "Desk PD"), ("Laptop 1", "Laptop 2"), ("Laptop", "Laptops"), ("Living room", "Bedroom"),
            ("PC 1", "PC 11"), ("Anna", "Hanna"),
        ]
        for (a, b) in pairs { XCTAssertFalse(DeviceIdentity.looksAlike(a, b), "\(a) / \(b)") }
    }

    func testFingerprintIsStableShortAndKeySpecific() {
        let a = DeviceIdentity.fingerprint(Data(repeating: 1, count: 32))
        let b = DeviceIdentity.fingerprint(Data(repeating: 2, count: 32))
        XCTAssertEqual(a, DeviceIdentity.fingerprint(Data(repeating: 1, count: 32)))
        XCTAssertNotEqual(a, b)
        XCTAssertEqual(a.count, 19, "four groups of four hex digits")
        XCTAssertNotNil(a.range(of: #"^[0-9a-f]{4}( [0-9a-f]{4}){3}$"#, options: .regularExpression))
        // Pinned so the Go side (cmd/sharelinux fingerprint) can be checked against the same value.
        XCTAssertEqual(DeviceIdentity.fingerprint(Data(repeating: 1, count: 32)), "72cd 6e84 22c4 07fb")
    }
}
