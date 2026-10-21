import Darwin
import XCTest

@testable import HelperRuntime

/// The support directory and the identity key: a planted symlink, another
/// owner or loose permissions must never end up holding the private key.
final class IdentityStoreHardeningTests: XCTestCase {
    private func tempDir() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("oss-\(UUID().uuidString)")
    }
    private func mode(_ u: URL) -> Int? {
        (try? FileManager.default.attributesOfItem(atPath: u.path))?[.posixPermissions] as? Int
    }

    func testAnExistingLooseDirectoryAndKeyAreTightened() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o755])
        let key = try IdentityStore(directory: dir).loadOrCreateKey()
        XCTAssertEqual(mode(dir), 0o700, "created 0755 by someone, now closed")
        let keyFile = dir.appendingPathComponent("identity.key")
        chmod(keyFile.path, 0o644)
        XCTAssertEqual(try IdentityStore(directory: dir).loadOrCreateKey().rawRepresentation, key.rawRepresentation)
        XCTAssertEqual(mode(keyFile), 0o600)
    }

    func testASymlinkedSupportDirectoryIsRefusedAndNothingIsWrittenThroughIt() throws {
        let target = tempDir(), link = tempDir()
        defer { try? FileManager.default.removeItem(at: target); try? FileManager.default.removeItem(at: link) }
        try FileManager.default.createDirectory(
            at: target, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o755])
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: target)
        let store = IdentityStore(directory: link)
        XCTAssertThrowsError(try store.loadOrCreateKey()) { XCTAssert($0 is IdentityStore.StoreError) }
        XCTAssertThrowsError(try store.loadPaired())
        XCTAssertThrowsError(try store.savePaired([]))
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: target.path), [], "no key written through the link")
        XCTAssertEqual(mode(target), 0o755, "the link's target is not chmod'ed either")
    }

    func testADirectoryOwnedByAnotherUserIsRefused() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        let stranger = IdentityStore(directory: dir, ownerUID: geteuid() &+ 1)
        XCTAssertThrowsError(try stranger.loadOrCreateKey()) {
            XCTAssertEqual($0 as? IdentityStore.StoreError, .unsafe("owned by another user"))
        }
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), [])
    }

    func testAKeyFileThatIsASymlinkIsRefused() throws {
        let dir = tempDir(), elsewhere = tempDir()
        defer { try? FileManager.default.removeItem(at: dir); try? FileManager.default.removeItem(at: elsewhere) }
        _ = try IdentityStore(directory: dir).loadOrCreateKey()
        let keyFile = dir.appendingPathComponent("identity.key")
        try FileManager.default.moveItem(at: keyFile, to: elsewhere)
        try FileManager.default.createSymbolicLink(at: keyFile, withDestinationURL: elsewhere)
        XCTAssertThrowsError(try IdentityStore(directory: dir).loadOrCreateKey())
    }
}
