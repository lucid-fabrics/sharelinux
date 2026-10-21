import CryptoKit
import Security
import XCTest

@testable import HelperCore
@testable import HelperRuntime

/// A throwaway keychain file in a temp directory: the owner's login keychain is never touched.
final class TestKeychain {
    let dir: URL
    let keychain: SecKeychain
    init() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("oss-test-kc-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        self.dir = dir
        let pw = "throwaway-" + UUID().uuidString
        var kc: SecKeychain?
        let path = dir.appendingPathComponent("test.keychain-db").path
        let status = pw.withCString { SecKeychainCreate(path, UInt32(pw.utf8.count), $0, false, nil, &kc) }
        guard status == errSecSuccess, let kc else { throw KeyStoreError.failed("cannot create a test keychain (\(status))") }
        keychain = kc
    }
    func destroy() {
        SecKeychainDelete(keychain)
        try? FileManager.default.removeItem(at: dir)
    }
}

final class KeychainKeyStoreTests: XCTestCase {
    var kc: TestKeychain!
    var store: KeychainKeyStore!

    override func setUpWithError() throws {
        kc = try TestKeychain()
        store = KeychainKeyStore(keychain: kc.keychain)
    }
    override func tearDown() { kc.destroy() }

    func testTheIdentityKeyIsCreatedOnceAndComesBackIdentical() throws {
        let a = try store.loadOrCreateIdentity()
        let b = try KeychainKeyStore(keychain: kc.keychain).loadOrCreateIdentity()
        XCTAssertEqual(a.rawRepresentation, b.rawRepresentation)
        let files = try FileManager.default.contentsOfDirectory(atPath: kc.dir.path).filter { !$0.hasPrefix(".") }
        XCTAssertEqual(files, ["test.keychain-db"], "no key file anywhere but the keychain")
        XCTAssertNotEqual(a.rawRepresentation, Data(count: 32), "the returned key is not the wiped copy")
    }

    func testPairedDevicesAreAddedListedReplacedAndRemoved() throws {
        XCTAssertEqual(try store.loadPaired(), [])
        let k1 = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let k2 = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k1))
        try store.addPaired(PairedDevice(name: "Laptop", key: k2))
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Desk PC", "Laptop"])
        try store.addPaired(PairedDevice(name: "Desk PC 2", key: k1))
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Desk PC 2", "Laptop"], "same key replaces, no duplicate")
        try store.removePaired(key: k1)
        XCTAssertEqual(try store.loadPaired(), [PairedDevice(name: "Laptop", key: k2)])
        try store.removePaired(key: k1)  // removing twice is fine
        XCTAssertThrowsError(try store.addPaired(PairedDevice(name: "x", key: Data(count: 5))))
    }

    /// Items must not sync to iCloud and must name only this application.
    func testItemsAreNotSynchronisedAndTrustOnlyThisApplication() throws {
        _ = try store.loadOrCreateIdentity()
        try store.addPaired(PairedDevice(name: "Desk PC", key: Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation))
        for service in [KeychainKeyStore.identityService, KeychainKeyStore.pairedService] {
            let q: [String: Any] = [
                kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
                kSecMatchSearchList as String: [kc.keychain], kSecAttrSynchronizable as String: kSecAttrSynchronizableAny,
                kSecReturnAttributes as String: true, kSecReturnRef as String: true, kSecMatchLimit as String: kSecMatchLimitOne,
            ]
            var out: CFTypeRef?
            XCTAssertEqual(SecItemCopyMatching(q as CFDictionary, &out), errSecSuccess, service)
            let item = try XCTUnwrap(out as? [String: Any])
            // A file-based login keychain item never syncs; if the attribute is reported at all it must be false.
            let sync = (item[kSecAttrSynchronizable as String] as? NSNumber)?.boolValue
            XCTAssertTrue(sync == nil || sync == false, "\(service) must not sync")

            var access: SecAccess?
            let ref = try XCTUnwrap(item[kSecValueRef as String]) as! SecKeychainItem
            XCTAssertEqual(SecKeychainItemCopyAccess(ref, &access), errSecSuccess)
            let acls = try XCTUnwrap(SecAccessCopyMatchingACLList(try XCTUnwrap(access), kSecACLAuthorizationDecrypt) as? [SecACL])
            XCTAssertFalse(acls.isEmpty)
            for acl in acls {
                var apps: CFArray?
                var desc: CFString?
                var selector = SecKeychainPromptSelector()
                XCTAssertEqual(SecACLCopyContents(acl, &apps, &desc, &selector), errSecSuccess)
                let list = try XCTUnwrap(apps as? [SecTrustedApplication], "an ACL naming no applications means every application")
                XCTAssertEqual(list.count, 1, "only this application may read \(service)")
            }
        }
    }

    func testAnItemWhoseAccountAndPayloadDisagreeIsIgnored() throws {
        let real = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Real", key: real))
        // Planted items: account does not match the key inside, a bad name, a short key.
        let other = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        func plant(account: String, json: String) {
            let q: [String: Any] = [
                kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: KeychainKeyStore.pairedService,
                kSecAttrAccount as String: account, kSecValueData as String: Data(json.utf8), kSecUseKeychain as String: kc.keychain,
            ]
            XCTAssertEqual(SecItemAdd(q as CFDictionary, nil), errSecSuccess)
        }
        plant(account: "00" + other.hex.dropFirst(2), json: #"{"name":"Mismatch","key":"\#(other.hex)"}"#)
        plant(account: other.hex, json: #"{"name":"Bad\u{0}Name","key":"\#(other.hex)"}"#)
        plant(account: "abcd", json: #"{"name":"Short","key":"abcd"}"#)
        plant(account: "zz", json: "not json")
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Real"])
    }

    func testTheOldFilesMoveIntoTheKeychainAndAreDeleted() throws {
        let dir = kc.dir.appendingPathComponent("support")
        let legacy = IdentityStore(directory: dir)
        let oldKey = try legacy.loadOrCreateKey()
        let peer = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try legacy.savePaired([.init(name: "Old PC", key: peer.hex)])
        try store.importLegacyFiles(from: legacy)
        XCTAssertEqual(try store.loadOrCreateIdentity().rawRepresentation, oldKey.rawRepresentation, "same identity, so existing pairings keep working")
        XCTAssertEqual(try store.loadPaired(), [PairedDevice(name: "Old PC", key: peer)])
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), [], "no secret left on disk")
        try store.importLegacyFiles(from: legacy)  // nothing to do the second time
    }
}
