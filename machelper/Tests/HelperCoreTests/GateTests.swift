import XCTest
@testable import HelperCore

final class GateTests: XCTestCase {
    private func allOKInputs() -> GateInputs {
        let peer = PeerAddress(bytes: [192, 0, 2, 1])!
        return GateInputs(
            toggleOn: true,
            peerPaired: true,
            udpPeer: peer,
            screenSharingPeers: Set([peer]),
            consoleUID: 501,
            ownUID: 501,
            screenLocked: false,
            accessibilityGranted: true,
            rateLimited: false
        )
    }

    func testAllOKAccepts() {
        XCTAssertEqual(Gate.evaluate(allOKInputs()), .accept)
    }

    func testUnpairedPeerIsDroppedSilentlyEvenWhenEverythingElseIsBad() {
        var inputs = allOKInputs()
        inputs.peerPaired = false
        inputs.toggleOn = false
        inputs.udpPeer = nil
        inputs.screenSharingPeers = []
        inputs.consoleUID = nil
        inputs.screenLocked = nil
        inputs.accessibilityGranted = false
        inputs.rateLimited = true

        XCTAssertEqual(Gate.evaluate(inputs), .dropSilently)
    }

    func testToggleOffIsDeniedAsDisabled() {
        var inputs = allOKInputs()
        inputs.toggleOn = false

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.disabled))
    }

    func testMissingConsoleUIDIsDeniedAsNotConsole() {
        var inputs = allOKInputs()
        inputs.consoleUID = nil

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.notConsole))
    }

    func testDifferentConsoleUIDIsDeniedAsNotConsole() {
        var inputs = allOKInputs()
        inputs.consoleUID = 502

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.notConsole))
    }

    func testLockedScreenIsDeniedAsLocked() {
        var inputs = allOKInputs()
        inputs.screenLocked = true

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.locked))
    }

    func testUnknownScreenLockStateIsDeniedAsLocked() {
        var inputs = allOKInputs()
        inputs.screenLocked = nil

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.locked))
    }

    func testMissingUDPPeerIsDeniedAsNoSession() {
        var inputs = allOKInputs()
        inputs.udpPeer = nil

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.noSession))
    }

    func testUDPPeerWithoutScreenSharingSessionIsDeniedAsNoSession() {
        var inputs = allOKInputs()
        inputs.udpPeer = PeerAddress(bytes: [198, 51, 100, 2])

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.noSession))
    }

    func testIPv4MappedIPv6AddressEqualsIPv4Address() {
        let ipv4 = PeerAddress(bytes: [192, 0, 2, 1])
        let mappedIPv6 = PeerAddress(bytes: [
            0, 0, 0, 0, 0, 0, 0, 0,
            0, 0, 0xff, 0xff,
            192, 0, 2, 1
        ])

        XCTAssertNotNil(ipv4)
        XCTAssertNotNil(mappedIPv6)
        XCTAssertEqual(mappedIPv6, ipv4)
    }

    func testMissingAccessibilityGrantIsDeniedAsNeedsGrant() {
        var inputs = allOKInputs()
        inputs.accessibilityGranted = false

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.needsGrant))
    }

    func testRateLimitedIsDeniedAsRateLimited() {
        var inputs = allOKInputs()
        inputs.rateLimited = true

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.rateLimited))
    }

    func testDisabledTakesPrecedenceOverLocked() {
        var inputs = allOKInputs()
        inputs.toggleOn = false
        inputs.screenLocked = true

        XCTAssertEqual(Gate.evaluate(inputs), .deny(.disabled))
    }

    func testPeerAddressWithInvalidLengthIsNil() {
        XCTAssertNil(PeerAddress(bytes: [192, 0, 2, 1, 0]))
    }
}
