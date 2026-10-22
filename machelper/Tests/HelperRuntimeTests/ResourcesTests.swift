import XCTest

/// The bundle inputs: the Info.plist, the LaunchAgent template and bundle.sh
/// keep the promises of the design (per-user, Aqua only, menu bar only,
/// ad-hoc signing only).
final class ResourcesTests: XCTestCase {
    var root: URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
    }

    func plist(_ name: String) throws -> [String: Any] {
        let data = try Data(contentsOf: root.appendingPathComponent("Resources/\(name)"))
        return try XCTUnwrap(
            PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
    }

    func testInfoPlist() throws {
        let info = try plist("Info.plist")
        XCTAssertEqual(info["CFBundleIdentifier"] as? String, "io.github.sharelinux.mousehelper")
        XCTAssertEqual(info["CFBundleExecutable"] as? String, "MouseHelper")
        XCTAssertEqual(info["LSUIElement"] as? Bool, true, "menu bar only, no Dock icon")
        XCTAssertNil(info["NSAppleEventsUsageDescription"])
    }

    func testLaunchAgentIsPerUserAquaOnlyAndStaysQuit() throws {
        let agent = try plist("io.github.sharelinux.mousehelper.plist")
        XCTAssertEqual(agent["Label"] as? String, "io.github.sharelinux.mousehelper")
        XCTAssertEqual(agent["LimitLoadToSessionType"] as? String, "Aqua")
        XCTAssertEqual(agent["KeepAlive"] as? Bool, false, "Quit and the kill switch must stay quit")
        XCTAssertEqual(agent["RunAtLoad"] as? Bool, true)
        XCTAssertNil(agent["UserName"], "never a root or system daemon")
        let args = try XCTUnwrap(agent["ProgramArguments"] as? [String])
        XCTAssertEqual(args.count, 1)
        XCTAssertEqual(args[0], "__APP_EXECUTABLE__", "bundle.sh fills in the absolute path")
    }

    func testBundleScriptSignsAdHocOnlyAndInstallsNothing() throws {
        let script = try String(contentsOf: root.appendingPathComponent("scripts/bundle.sh"), encoding: .utf8)
        XCTAssertTrue(script.contains("codesign --force --sign - "), "ad-hoc identity")
        // No way to name another identity.
        XCTAssertFalse(script.contains("SIGN_IDENTITY"))
        XCTAssertFalse(script.contains("--sign \"$"))
        XCTAssertFalse(script.contains("--sign $"))
        // It checks its own result.
        XCTAssertTrue(script.contains("Signature=adhoc"))
        XCTAssertTrue(script.contains("TeamIdentifier=not set"))
        // Nothing is installed or launched.
        for forbidden in ["launchctl", "open ", "cp -R \"$app\" ~", "/Library/LaunchAgents/io", "tccutil", "xattr"] {
            let code = script.split(separator: "\n").filter { !$0.hasPrefix("#") && !$0.hasPrefix("echo") }
            XCTAssertFalse(code.contains { $0.contains(forbidden) }, forbidden)
        }
    }

    /// W3 audit: a local self-signed identity (key readable by /usr/bin/codesign, certificate trusted)
    /// let any same-user process sign a binary that meets the designated requirement.
    func testThereIsNoLocalSigningIdentityPathAnywhere() throws {
        let fm = FileManager.default
        for gone in ["make-local-signing-identity.sh", "check-signing-identity.sh", "test-signing-guards.sh"] {
            XCTAssertFalse(fm.fileExists(atPath: root.appendingPathComponent("scripts/\(gone)").path), gone)
        }
        let script = try String(contentsOf: root.appendingPathComponent("scripts/bundle.sh"), encoding: .utf8)
        for forbidden in ["OSS_LOCAL_SIGNING_IDENTITY", "certificate leaf", "check-signing-identity", "security find-"] {
            XCTAssertFalse(script.contains(forbidden), forbidden)
        }
        let scripts = try fm.contentsOfDirectory(atPath: root.appendingPathComponent("scripts").path)
        for name in scripts {
            let text = try String(contentsOf: root.appendingPathComponent("scripts/\(name)"), encoding: .utf8)
            XCTAssertFalse(text.contains("create-keychain") || text.contains("add-trusted-cert") || text.contains("set-key-partition-list"), name)
            XCTAssertFalse(text.contains("-T /usr/bin/codesign"), name)
        }
    }

    // MARK: LaunchAgent filling

    func fill(_ exe: String) throws -> (status: Int32, out: Data, err: String) {
        let p = Process()
        p.executableURL = root.appendingPathComponent("scripts/fill-launchagent.sh")
        p.arguments = [root.appendingPathComponent("Resources/io.github.sharelinux.mousehelper.plist").path, exe]
        let out = Pipe(), err = Pipe()
        p.standardOutput = out; p.standardError = err
        try p.run()
        let data = out.fileHandleForReading.readDataToEndOfFile()
        let e = String(data: err.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        p.waitUntilExit()
        return (p.terminationStatus, data, e)
    }

    /// The old sed substitution treated "&" and "|" in the path as syntax and
    /// also rewrote the placeholder mentioned in the plist's comment.
    func testTheExecutablePathIsFilledInVerbatimWhateverItContains() throws {
        let template = try String(contentsOf: root.appendingPathComponent("Resources/io.github.sharelinux.mousehelper.plist"), encoding: .utf8)
        XCTAssertEqual(template.components(separatedBy: "__APP_EXECUTABLE__").count - 1, 1, "the placeholder appears once, not in the comment")
        for exe in [
            "/Users/Plain Name/Applications/OSS Mouse Helper.app/Contents/MacOS/MouseHelper",
            "/Users/a&b|c/App/MouseHelper", "/Users/back\\slash\\1/x", "/Users/<angle>/x", "/Users/$HOME`id`/x", "/Users/it's \"q\"/x",
            "/Users/dash--dash/x", "/Users/a&amp;b/x",
        ] {
            let r = try fill(exe)
            XCTAssertEqual(r.status, 0, "\(exe): \(r.err)")
            let obj = try PropertyListSerialization.propertyList(from: r.out, format: nil) as? [String: Any]
            XCTAssertEqual((obj?["ProgramArguments"] as? [String]) ?? [], [exe], exe)
            // Everything else is untouched, comment included.
            let text = String(data: r.out, encoding: .utf8) ?? ""
            let header = template.components(separatedBy: "<plist")[0]
            XCTAssertTrue(text.hasPrefix(header), "the comment is unchanged")
            XCTAssertEqual(text.split(separator: "\n").count, template.split(separator: "\n").count)
        }
    }

    func testARelativeOrMultilinePathIsRefused() throws {
        XCTAssertNotEqual(try fill("relative/path").status, 0)
        XCTAssertNotEqual(try fill("/Users/a\nb/x").status, 0)
    }

    func runFill(template: URL, exe: String, output: URL?) throws -> (status: Int32, out: Data) {
        let p = Process()
        p.executableURL = root.appendingPathComponent("scripts/fill-launchagent.sh")
        p.arguments = [template.path, exe] + (output.map { [$0.path] } ?? [])
        let out = Pipe(), err = Pipe()
        p.standardOutput = out; p.standardError = err
        try p.run()
        let data = out.fileHandleForReading.readDataToEndOfFile()
        _ = err.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus, data)
    }

    /// With an output file the plist is built beside it and moved into place
    /// only on success: a failure leaves the old file as it was and no stray
    /// temporary file; a success leaves just the file.
    func testTheOutputFileIsReplacedOnlyWhenTheFillSucceeded() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("fill-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        let good = root.appendingPathComponent("Resources/io.github.sharelinux.mousehelper.plist")
        // The placeholder exists once but not as a ProgramArguments string: awk fails after reading it.
        let bad = dir.appendingPathComponent("bad.plist")
        try "<plist><!-- __APP_EXECUTABLE__ --></plist>\n".write(to: bad, atomically: true, encoding: .utf8)
        let out = dir.appendingPathComponent("out.plist")
        try "OLD".write(to: out, atomically: true, encoding: .utf8)

        let failed = try runFill(template: bad, exe: "/x/y", output: out)
        XCTAssertNotEqual(failed.status, 0)
        XCTAssertEqual(try String(contentsOf: out, encoding: .utf8), "OLD", "a failed fill leaves the old file alone")
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path).sorted(), ["bad.plist", "out.plist"], "no temporary file left")
        let failedPrint = try runFill(template: bad, exe: "/x/y", output: nil)
        XCTAssertNotEqual(failedPrint.status, 0)
        XCTAssertEqual(failedPrint.out.count, 0, "nothing is printed on failure")

        let ok = try runFill(template: good, exe: "/x/y", output: out)
        XCTAssertEqual(ok.status, 0)
        let obj = try PropertyListSerialization.propertyList(from: Data(contentsOf: out), format: nil) as? [String: Any]
        XCTAssertEqual(obj?["ProgramArguments"] as? [String], ["/x/y"])
        XCTAssertEqual(try FileManager.default.contentsOfDirectory(atPath: dir.path).sorted(), ["bad.plist", "out.plist"])
    }
}
