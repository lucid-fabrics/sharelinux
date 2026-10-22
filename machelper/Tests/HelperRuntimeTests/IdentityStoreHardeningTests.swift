import Darwin
import XCTest

@testable import HelperRuntime

/// The support directory: a planted symlink, another owner or loose permissions
/// must never be trusted, and nothing in it is ever read as a key or a device.
final class IdentityStoreHardeningTests: XCTestCase {
    private func tempDir() -> URL {
        FileManager.default.temporaryDirectory.appendingPathComponent("oss-\(UUID().uuidString)")
    }
    private func mode(_ u: URL) -> Int? {
        (try? FileManager.default.attributesOfItem(atPath: u.path))?[.posixPermissions] as? Int
    }

    func testAnExistingLooseDirectoryIsTightenedAndACreatedOneIsPrivate() throws {
        let dir = tempDir(), fresh = tempDir()
        defer { try? FileManager.default.removeItem(at: dir); try? FileManager.default.removeItem(at: fresh) }
        try FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o755])
        try IdentityStore(directory: dir).prepareDirectory()
        XCTAssertEqual(mode(dir), 0o700, "created 0755 by someone, now closed")
        try IdentityStore(directory: fresh).prepareDirectory()
        XCTAssertEqual(mode(fresh), 0o700)
    }

    func testASymlinkedSupportDirectoryIsRefusedAndNothingIsTouchedThroughIt() throws {
        let target = tempDir(), link = tempDir()
        defer { try? FileManager.default.removeItem(at: target); try? FileManager.default.removeItem(at: link) }
        try FileManager.default.createDirectory(
            at: target, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o755])
        try FileManager.default.createSymbolicLink(at: link, withDestinationURL: target)
        try Data("x".utf8).write(to: target.appendingPathComponent("paired.json"))
        let store = IdentityStore(directory: link)
        XCTAssertThrowsError(try store.prepareDirectory()) { XCTAssert($0 is IdentityStore.StoreError) }
        XCTAssertThrowsError(try store.removeStaleLegacyFiles())
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: target.path), ["paired.json"], "nothing removed through the link")
        XCTAssertEqual(mode(target), 0o755, "the link's target is not chmod'ed either")
    }

    func testADirectoryOwnedByAnotherUserIsRefused() throws {
        let dir = tempDir()
        defer { try? FileManager.default.removeItem(at: dir) }
        try FileManager.default.createDirectory(
            at: dir, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        let stranger = IdentityStore(directory: dir, ownerUID: geteuid() &+ 1)
        XCTAssertThrowsError(try stranger.removeStaleLegacyFiles()) {
            XCTAssertEqual($0 as? IdentityStore.StoreError, .unsafe("owned by another user"))
        }
    }

    func testAStaleKeyFileThatIsASymlinkIsUnlinkedNotFollowed() throws {
        let dir = tempDir(), elsewhere = tempDir()
        defer { try? FileManager.default.removeItem(at: dir); try? FileManager.default.removeItem(at: elsewhere) }
        try IdentityStore(directory: dir).prepareDirectory()
        try Data("keep".utf8).write(to: elsewhere)
        try FileManager.default.createSymbolicLink(at: dir.appendingPathComponent("identity.key"), withDestinationURL: elsewhere)
        try IdentityStore(directory: dir).removeStaleLegacyFiles()
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), [])
        XCTAssertEqual(try String(contentsOf: elsewhere, encoding: .utf8), "keep", "the link target is untouched")
    }

    /// The gap between "the directory is vetted" and "the stale files are unlinked": the directory
    /// is swapped for a symlink to someone else's folder right there. The unlink must go through the
    /// vetted descriptor, so the other folder's file with the same name survives.
    func testSwappingTheDirectoryForASymlinkAfterTheCheckDoesNotRedirectTheUnlink() throws {
        let dir = tempDir(), moved = tempDir(), victim = tempDir()
        defer { for u in [dir, moved, victim] { try? FileManager.default.removeItem(at: u) } }
        try FileManager.default.createDirectory(at: victim, withIntermediateDirectories: false)
        try Data("precious".utf8).write(to: victim.appendingPathComponent("identity.key"))
        try Data("precious".utf8).write(to: victim.appendingPathComponent("paired.json"))
        let store = IdentityStore(directory: dir)
        try store.prepareDirectory()
        try Data("stale".utf8).write(to: dir.appendingPathComponent("identity.key"))
        store.afterVettingForTests = {
            // What a same-user attacker does in the window: move the real directory away and put a
            // symlink to a folder holding files with the same names in its place.
            try? FileManager.default.moveItem(at: dir, to: moved)
            try? FileManager.default.createSymbolicLink(at: dir, withDestinationURL: victim)
        }
        try store.removeStaleLegacyFiles()
        XCTAssertEqual(
            Set(try FileManager.default.contentsOfDirectory(atPath: victim.path)), ["identity.key", "paired.json"],
            "nothing was deleted through the swapped-in link")
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: moved.path), [], "the vetted directory was cleaned")
    }
}
