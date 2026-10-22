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
        try store.removePaired(key: Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation)
        let gone = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Gone", key: gone))
        try store.removePaired(key: gone)  // writes the revocation set
        for service in [KeychainKeyStore.identityService, KeychainKeyStore.pairedService, KeychainKeyStore.revokedService] {
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

    /// What a foreign program of the same user can do: add generic-password items
    /// under our service name through the plain Keychain API (not through this app).
    private func plant(account: String, json: String, service: String = KeychainKeyStore.pairedService) {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
            kSecAttrAccount as String: account, kSecValueData as String: Data(json.utf8), kSecUseKeychain as String: kc.keychain,
        ]
        XCTAssertEqual(SecItemAdd(q as CFDictionary, nil), errSecSuccess)
    }

    private let nonceHex = String(repeating: "07", count: 16)

    private func hmac(_ macKey: SymmetricKey, key: Data, name: String, created: Int64) -> String {
        Data(HMAC<SHA256>.authenticationCode(
            for: KeychainKeyStore.message(key: key, name: name, created: created, nonce: Data(repeating: 7, count: 16), gen: 0),
            using: macKey)).hex
    }

    private func rawItem(service: String, account: String) -> Data? {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
            kSecAttrAccount as String: account, kSecMatchSearchList as String: [kc.keychain],
            kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        return SecItemCopyMatching(q as CFDictionary, &out) == errSecSuccess ? out as? Data : nil
    }

    private func dropItem(service: String, account: String) {
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: service,
            kSecAttrAccount as String: account, kSecMatchSearchList as String: [kc.keychain],
        ]
        XCTAssertEqual(SecItemDelete(q as CFDictionary), errSecSuccess)
    }

    func testAnItemWhoseAccountAndPayloadDisagreeIsIgnored() throws {
        let real = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Real", key: real))
        let other = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        plant(account: "00" + other.hex.dropFirst(2), json: #"{"name":"Mismatch","key":"\#(other.hex)"}"#)
        plant(account: other.hex, json: #"{"name":"Bad\u{0}Name","key":"\#(other.hex)"}"#)
        plant(account: "abcd", json: #"{"name":"Short","key":"abcd"}"#)
        plant(account: "zz", json: "not json")
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Real"])
        XCTAssertEqual(store.ignoredRecords, 4)
    }

    /// Finding 2: any program of the same user could add a trusted device. A record this app did not write is never trusted.
    func testAForeignItemInTheOldFormatIsNeverTrustedAndIsReported() throws {
        _ = try store.loadOrCreateIdentity()
        let real = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: real))
        XCTAssertEqual(store.ignoredRecords, 0)
        let attacker = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        // Well-formed in every respect except that this app did not write it.
        plant(account: attacker.hex, json: #"{"name":"Trusted Laptop","key":"\#(attacker.hex)"}"#)
        XCTAssertEqual(try store.loadPaired().map(\.key), [real], "the foreign key is not in the trusted list")
        XCTAssertEqual(store.ignoredRecords, 1, "and the app knows it ignored something")
        XCTAssertEqual(store.ignoredAccounts, [attacker.hex])
    }

    func testAForeignRecordWithAMacMadeFromTheWrongKeyOrAForgedFieldIsIgnored() throws {
        _ = try store.loadOrCreateIdentity()
        let attacker = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let attackerMacKey = SymmetricKey(data: Data(repeating: 9, count: 32))  // not derived from the identity
        let created: Int64 = 1_800_000_000
        let wrong = hmac(attackerMacKey, key: attacker, name: "Evil", created: created)
        plant(account: attacker.hex, json: #"{"v":3,"name":"Evil","key":"\#(attacker.hex)","created":\#(created),"nonce":"\#(nonceHex)","gen":0,"mac":"\#(wrong)"}"#)
        let other = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        plant(account: other.hex, json: #"{"v":3,"name":"Evil2","key":"\#(other.hex)","created":\#(created),"nonce":"\#(nonceHex)","gen":0,"mac":"\#(String(repeating: "00", count: 32))"}"#)
        XCTAssertEqual(try store.loadPaired(), [])
        XCTAssertEqual(store.ignoredRecords, 2)
    }

    /// Reading every paired item (the attributes are not secret) lets a foreign program copy or edit them, not forge them.
    func testTamperingWithAGenuineRecordMakesItUntrusted() throws {
        _ = try store.loadOrCreateIdentity()
        let k1 = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let k2 = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k1))
        // Read the genuine payload the way any same-user program could be allowed to.
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: KeychainKeyStore.pairedService,
            kSecAttrAccount as String: k1.hex, kSecMatchSearchList as String: [kc.keychain],
            kSecReturnData as String: true, kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var out: CFTypeRef?
        XCTAssertEqual(SecItemCopyMatching(q as CFDictionary, &out), errSecSuccess)
        var json = try XCTUnwrap(String(data: try XCTUnwrap(out as? Data), encoding: .utf8))
        // 1. Rename the device, keeping its MAC.
        let renamed = json.replacingOccurrences(of: "Desk PC", with: "Safe PC")
        XCTAssertNotEqual(renamed, json)
        XCTAssertEqual(SecItemDelete(q.filter { $0.key != kSecReturnData as String && $0.key != kSecMatchLimit as String } as CFDictionary), errSecSuccess)
        plant(account: k1.hex, json: renamed)
        XCTAssertEqual(try store.loadPaired(), [], "an altered name voids the MAC")
        // 2. Move the genuine MAC onto another key.
        json = json.replacingOccurrences(of: k1.hex, with: k2.hex)
        plant(account: k2.hex, json: json)
        XCTAssertEqual(try store.loadPaired(), [], "a MAC does not move to another key")
        XCTAssertEqual(store.ignoredRecords, 2)
    }

    func testReplacingTheIdentityKeyUntrustsTheOldRecordsAndANewKeychainTrustsNothing() throws {
        let key = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        // No identity yet: a planted record cannot be authentic, and reading does not create an identity.
        plant(account: key.hex, json: #"{"v":3,"name":"X","key":"\#(key.hex)","created":1,"nonce":"\#(nonceHex)","gen":0,"mac":"00"}"#)
        XCTAssertEqual(try store.loadPaired(), [])
        XCTAssertEqual(store.ignoredRecords, 1)
        // A second install (another identity) does not accept the first one's genuine records.
        let kc2 = try TestKeychain()
        defer { kc2.destroy() }
        let other = KeychainKeyStore(keychain: kc2.keychain)
        _ = try other.loadOrCreateIdentity()
        try other.addPaired(PairedDevice(name: "Elsewhere", key: key))
        XCTAssertEqual(try other.loadPaired().count, 1)
        XCTAssertEqual(try store.loadPaired(), [], "this keychain's identity is a different one")
    }

    func testGenuineRecordsSurviveARestartAndReportNothing() throws {
        let k = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))
        let again = KeychainKeyStore(keychain: kc.keychain)  // a new process: nothing cached
        XCTAssertEqual(try again.loadPaired(), [PairedDevice(name: "Desk PC", key: k)])
        XCTAssertEqual(again.ignoredRecords, 0)
    }

    // MARK: revocation (follow-up audit, finding 7)

    /// A removed record's exact bytes, put back, must not be trusted again.
    func testTheExactBytesOfARemovedRecordAreRejectedWhenPutBack() throws {
        let k = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let other = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))
        try store.addPaired(PairedDevice(name: "Laptop", key: other))
        let copy = try XCTUnwrap(rawItem(service: KeychainKeyStore.pairedService, account: k.hex), "any same-user program may be able to copy this")
        try store.removePaired(key: k)
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Laptop"])
        plant(account: k.hex, json: String(decoding: copy, as: UTF8.self))
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Laptop"], "the removed record is not trusted again")
        XCTAssertEqual(store.ignoredAccounts, [k.hex])
        XCTAssertEqual(try KeychainKeyStore(keychain: kc.keychain).loadPaired().map(\.name), ["Laptop"], "also after a restart")
    }

    func testAPairedAgainDeviceIsTrustedAfterRemovalButTheOldCopyIsNot() throws {
        let k = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))
        let old = try XCTUnwrap(rawItem(service: KeychainKeyStore.pairedService, account: k.hex))
        try store.removePaired(key: k)
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))  // the owner paired it again, same second
        XCTAssertEqual(try store.loadPaired().map(\.key), [k])
        dropItem(service: KeychainKeyStore.pairedService, account: k.hex)
        plant(account: k.hex, json: String(decoding: old, as: UTF8.self))
        XCTAssertEqual(try store.loadPaired(), [], "the first record stays revoked")
    }

    func testDeletingOrRollingBackTheRevocationSetDoesNotRestoreNewerRecords() throws {
        let a = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let b = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let c = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "A", key: a))
        try store.removePaired(key: a)
        try store.addPaired(PairedDevice(name: "B", key: b))  // written after one removal: gen 1
        XCTAssertEqual(try store.loadPaired().map(\.name), ["B"])
        let set = try XCTUnwrap(rawItem(service: KeychainKeyStore.revokedService, account: "set"))
        dropItem(service: KeychainKeyStore.revokedService, account: "set")
        XCTAssertEqual(try store.loadPaired(), [], "the set is gone, so a record newer than it is not trusted")
        // A foreign replacement fails its MAC: nothing is trusted either.
        plant(account: "set", json: #"{"v":1,"count":0,"macs":[],"mac":"00"}"#, service: KeychainKeyStore.revokedService)
        XCTAssertEqual(try store.loadPaired(), [])
        XCTAssertThrowsError(try store.addPaired(PairedDevice(name: "C", key: c)), "no new trust while the set is damaged")
        // The genuine set back in place restores B.
        dropItem(service: KeychainKeyStore.revokedService, account: "set")
        plant(account: "set", json: String(decoding: set, as: UTF8.self), service: KeychainKeyStore.revokedService)
        XCTAssertEqual(try store.loadPaired().map(\.name), ["B"])
    }

    /// W3 follow-up audit, finding 1: the launch sequence imported a user-writable paired.json and gave
    /// every entry a valid record MAC. Neither old file may create trust or an identity now.
    func testDroppedLegacyFilesNeverCreateTrustOrIdentityAndAreRemovedUnread() throws {
        let dir = kc.dir.appendingPathComponent("support")
        let support = IdentityStore(directory: dir)
        try support.prepareDirectory()
        let attacker = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let planted = Curve25519.KeyAgreement.PrivateKey()
        try Data(#"[{"name":"Evil PC","key":"\#(attacker.hex)"}]"#.utf8).write(to: dir.appendingPathComponent("paired.json"))
        try planted.rawRepresentation.write(to: dir.appendingPathComponent("identity.key"))

        let identity = try support.openIdentity(keys: store)
        XCTAssertNotEqual(identity.rawRepresentation, planted.rawRepresentation, "a dropped key file is not the identity")
        XCTAssertEqual(try store.loadPaired(), [], "a dropped paired file is never trusted")
        XCTAssertEqual(store.ignoredRecords, 0)
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path), [], "both files were removed")
        // And a coordinator over that store trusts nobody.
        let c = try PairingCoordinator(staticKey: identity, store: store, ledger: PairingFailureLedger(store: MemoryPairingFailureStore()))
        XCTAssertEqual(c.pairedKeys, [])
    }

    // MARK: W3 closing: re-pair revokes, snapshot tripwire, reset

    /// Re-pairing a device replaces its record. The replaced bytes must not stay as good as the new
    /// record: put back, they are refused.
    func testRePairingRevokesTheReplacedRecord() throws {
        let k = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))
        let old = try XCTUnwrap(rawItem(service: KeychainKeyStore.pairedService, account: k.hex))
        try store.addPaired(PairedDevice(name: "Desk PC renamed", key: k))  // re-pair, no removal in between
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Desk PC renamed"])
        dropItem(service: KeychainKeyStore.pairedService, account: k.hex)
        plant(account: k.hex, json: String(decoding: old, as: UTF8.self))
        XCTAssertEqual(try store.loadPaired(), [], "the replaced record was revoked when it was replaced")
        XCTAssertEqual(store.ignoredAccounts, [k.hex])
        // And the fresh pair after that still works.
        try store.addPaired(PairedDevice(name: "Desk PC again", key: k))
        XCTAssertEqual(try store.loadPaired().map(\.name), ["Desk PC again"])
    }

    func testTheSnapshotIsStableAndSeesAnIdentitySwapADeletionAndForeignOrEditedRecords() throws {
        _ = try store.loadOrCreateIdentity()
        let k = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))
        let base = try store.snapshot()
        XCTAssertNotNil(base.identity)
        XCTAssertEqual(try KeychainKeyStore(keychain: kc.keychain).snapshot(), base, "stable across reads and restarts")
        // An own write moves the trust digest but not the identity stamp.
        let k2 = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Laptop", key: k2))
        let afterOwn = try store.snapshot()
        XCTAssertEqual(afterOwn.identity, base.identity)
        XCTAssertNotEqual(afterOwn.trust, base.trust)
        // A foreign record planted next to ours.
        plant(account: String(repeating: "ab", count: 32), json: "{}")
        let planted = try store.snapshot()
        XCTAssertNotEqual(planted.trust, afterOwn.trust)
        // A record deleted and put back byte for byte is a different item.
        let copy = try XCTUnwrap(rawItem(service: KeychainKeyStore.pairedService, account: k.hex))
        dropItem(service: KeychainKeyStore.pairedService, account: k.hex)
        plant(account: k.hex, json: String(decoding: copy, as: UTF8.self))
        let reput = try store.snapshot()
        XCTAssertNotEqual(reput.trust, planted.trust)
        // The identity item replaced by another program: same service and account, other key.
        dropItem(service: KeychainKeyStore.identityService, account: "static-key")
        XCTAssertNil(try store.snapshot().identity, "a missing identity item is reported as missing")
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: KeychainKeyStore.identityService,
            kSecAttrAccount as String: "static-key", kSecValueData as String: Data(repeating: 9, count: 32),
            kSecUseKeychain as String: kc.keychain,
        ]
        XCTAssertEqual(SecItemAdd(q as CFDictionary, nil), errSecSuccess)
        let replaced = try XCTUnwrap(try store.snapshot().identity)
        XCTAssertNotEqual(replaced, base.identity, "a replaced identity item is a different item")
    }

    func testAnEditedRecordInPlaceChangesTheSnapshot() throws {
        let k = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "Desk PC", key: k))
        let before = try store.snapshot()
        Thread.sleep(forTimeInterval: 1.1)  // the Keychain's date attributes are one second grained
        let q: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword, kSecAttrService as String: KeychainKeyStore.pairedService,
            kSecAttrAccount as String: k.hex, kSecMatchSearchList as String: [kc.keychain],
        ]
        XCTAssertEqual(SecItemUpdate(q as CFDictionary, [kSecValueData as String: Data("edited".utf8)] as CFDictionary), errSecSuccess)
        XCTAssertNotEqual(try store.snapshot().trust, before.trust)
    }

    /// MEDIUM: a damaged revocation set used to need Keychain Access. `resetAll` recovers.
    func testResetAllRecoversFromADamagedRevocationSetWithoutKeychainAccess() throws {
        let a = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let b = Curve25519.KeyAgreement.PrivateKey().publicKey.rawRepresentation
        let oldIdentity = try store.loadOrCreateIdentity().publicKey.rawRepresentation
        try store.addPaired(PairedDevice(name: "A", key: a))
        plant(account: "set", json: "garbage", service: KeychainKeyStore.revokedService)
        plant(account: b.hex, json: "{}")  // a foreign record is swept too
        XCTAssertEqual(try store.loadPaired(), [])
        XCTAssertTrue(store.revocationDamaged)
        XCTAssertThrowsError(try store.addPaired(PairedDevice(name: "B", key: b))) {
            XCTAssertEqual($0 as? KeyStoreError, .corrupt("the list of removed devices is damaged"))
        }
        try store.resetAll()
        XCTAssertFalse(store.revocationDamaged)
        for service in [KeychainKeyStore.identityService, KeychainKeyStore.pairedService, KeychainKeyStore.revokedService] {
            XCTAssertNil(rawItem(service: service, account: service == KeychainKeyStore.identityService ? "static-key" : service == KeychainKeyStore.revokedService ? "set" : a.hex), service)
        }
        XCTAssertNil(rawItem(service: KeychainKeyStore.pairedService, account: b.hex))
        XCTAssertNil(try store.snapshot().identity)
        // Fresh start: a new identity, nothing paired, pairing works again.
        let fresh = try store.loadOrCreateIdentity().publicKey.rawRepresentation
        XCTAssertNotEqual(fresh, oldIdentity)
        XCTAssertEqual(try store.loadPaired(), [])
        XCTAssertFalse(store.revocationDamaged)
        try store.addPaired(PairedDevice(name: "B", key: b))
        XCTAssertEqual(try store.loadPaired().map(\.name), ["B"])
    }

    func testAnEmptyStoreCanBeResetAndAnAlreadyDamagedSetWithNoRecordsIsFlagged() throws {
        try store.resetAll()  // nothing there: fine
        _ = try store.loadOrCreateIdentity()
        plant(account: "set", json: "garbage", service: KeychainKeyStore.revokedService)
        XCTAssertEqual(try store.loadPaired(), [])
        XCTAssertTrue(store.revocationDamaged, "damaged even with no record to ignore")
    }
}
