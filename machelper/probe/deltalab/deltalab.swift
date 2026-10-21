import AppKit
import CoreGraphics
import GameController

// DeltaLab: Probe B. Shows NSEvent deltas, event location and GCMouse deltas,
// in absolute mode (cursor associated) and in game mode (cursor disconnected).
// Safety: game mode ends after 20 s, on Esc, on deactivation and on quit;
// the app quits by itself after 3 minutes.

let logPath = NSString(string: "~/oss-probe/deltalab.log").expandingTildeInPath
let gameTimeout = 20.0
let lifetime = 180.0

struct Totals {
    var events = 0, nonzero = 0, matchLoc = 0
    var sx = 0.0, sy = 0.0, ax = 0.0, ay = 0.0
    var lastLoc: CGPoint? = nil
    var minX = Double.infinity, maxX = -Double.infinity
}

final class Lab: NSObject, NSApplicationDelegate, NSWindowDelegate {
    var window: NSWindow!
    var label: NSTextField!
    var game = false
    var cursorHidden = false
    var gameTimer: Timer?
    var abs = Totals(), gm = Totals()
    var gcX = 0.0, gcY = 0.0, gcAbsX = 0.0, gcAbsY = 0.0, gcSeen = false
    var gcMouse = "none"
    var t0 = Date()
    var fh: FileHandle?
    var buf = ""
    var maxGap = 0.0
    var lastEventT = Date()

    func log(_ s: String) {
        buf += s + "\n"
        if buf.count > 8000 { flush() }
    }
    func flush() {
        guard let fh = fh, !buf.isEmpty else { return }
        fh.write(buf.data(using: .utf8)!)
        buf = ""
    }

    func applicationDidFinishLaunching(_ n: Notification) {
        let fm = FileManager.default
        try? fm.createDirectory(atPath: NSString(string: "~/oss-probe").expandingTildeInPath, withIntermediateDirectories: true)
        if !fm.fileExists(atPath: logPath) { fm.createFile(atPath: logPath, contents: nil) }
        fh = FileHandle(forWritingAtPath: logPath)
        fh?.seekToEndOfFile()
        log("# run start \(Date())")

        window = NSWindow(contentRect: NSRect(x: 200, y: 300, width: 640, height: 360),
                          styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = "DeltaLab"
        window.acceptsMouseMovedEvents = true
        window.delegate = self
        label = NSTextField(labelWithString: "")
        label.font = NSFont.monospacedSystemFont(ofSize: 13, weight: .regular)
        label.frame = NSRect(x: 12, y: 8, width: 616, height: 340)
        label.maximumNumberOfLines = 0
        window.contentView!.addSubview(label)
        window.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)

        let mask: NSEvent.EventTypeMask = [.mouseMoved, .leftMouseDragged, .rightMouseDragged,
                                           .otherMouseDragged, .leftMouseDown, .keyDown]
        NSEvent.addLocalMonitorForEvents(matching: mask) { [weak self] e in
            self?.handle(e)
            return e
        }
        setupGC()
        NotificationCenter.default.addObserver(forName: .GCMouseDidConnect, object: nil, queue: .main) { [weak self] _ in self?.setupGC() }
        Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [weak self] _ in self?.refresh() }
        Timer.scheduledTimer(withTimeInterval: lifetime, repeats: false) { _ in NSApp.terminate(nil) }
    }

    func setupGC() {
        if let m = GCMouse.current ?? GCMouse.mice().first, let inp = m.mouseInput {
            gcMouse = m.vendorName ?? "mouse"
            gcSeen = true
            inp.mouseMovedHandler = { [weak self] _, dx, dy in
                guard let s = self else { return }
                s.gcX += Double(dx); s.gcY += Double(dy)
                s.gcAbsX += Swift.abs(Double(dx)); s.gcAbsY += Swift.abs(Double(dy))
                s.log(String(format: "gc t=%.4f dx=%.1f dy=%.1f", Date().timeIntervalSince(s.t0), dx, dy))
            }
        }
    }

    func enterGame() {
        guard !game else { return }
        game = true
        gm = Totals(); gcX = 0; gcY = 0; gcAbsX = 0; gcAbsY = 0
        CGAssociateMouseAndMouseCursorPosition(0)
        if !cursorHidden { CGDisplayHideCursor(CGMainDisplayID()); cursorHidden = true }
        log("# game mode ON t=\(String(format: "%.3f", Date().timeIntervalSince(t0)))")
        gameTimer?.invalidate()
        gameTimer = Timer.scheduledTimer(withTimeInterval: gameTimeout, repeats: false) { [weak self] _ in
            self?.leaveGame("timeout")
        }
    }

    func leaveGame(_ why: String) {
        gameTimer?.invalidate(); gameTimer = nil
        CGAssociateMouseAndMouseCursorPosition(1)
        if cursorHidden { CGDisplayShowCursor(CGMainDisplayID()); cursorHidden = false }
        if game {
            game = false
            log("# game mode OFF (\(why)) \(summary("game", gm)) gc=(\(gcX),\(gcY)) gcAbs=(\(gcAbsX),\(gcAbsY)) maxGap=\(String(format: "%.3f", maxGap))s")
            flush()
        }
    }

    func summary(_ n: String, _ t: Totals) -> String {
        "\(n): events=\(t.events) nonzero=\(t.nonzero) sumDX=\(t.sx) sumDY=\(t.sy) absDX=\(t.ax) absDY=\(t.ay) locEqDelta=\(t.matchLoc) locX=[\(t.minX),\(t.maxX)]"
    }

    func handle(_ e: NSEvent) {
        if e.type == .keyDown {
            if e.keyCode == 53 { leaveGame("esc") }
            return
        }
        if e.type == .leftMouseDown {
            if e.window === window { enterGame() }
            return
        }
        guard e.window === window || game else { return }
        let now = Date()
        if game { maxGap = max(maxGap, now.timeIntervalSince(lastEventT)) }
        lastEventT = now
        let loc = NSEvent.mouseLocation
        let dx = Double(e.deltaX), dy = Double(e.deltaY)
        var t = game ? gm : abs
        t.events += 1
        if dx != 0 || dy != 0 { t.nonzero += 1 }
        t.sx += dx; t.sy += dy; t.ax += Swift.abs(dx); t.ay += Swift.abs(dy)
        t.minX = min(t.minX, Double(loc.x)); t.maxX = max(t.maxX, Double(loc.x))
        var eq = "-"
        if let l = t.lastLoc {
            // Cocoa y is up; deltaY is down-positive.
            let ex = Double(loc.x - l.x), ey = -Double(loc.y - l.y)
            if Swift.abs(ex - dx) <= 1 && Swift.abs(ey - dy) <= 1 { t.matchLoc += 1; eq = "eq" } else { eq = "ne" }
        }
        t.lastLoc = loc
        if game { gm = t } else { abs = t }
        log(String(format: "%@ t=%.4f loc=(%.1f,%.1f) dx=%.1f dy=%.1f sub=%d type=%d %@",
                   game ? "game" : "abs", now.timeIntervalSince(t0), loc.x, loc.y, dx, dy,
                   Int(e.subtype.rawValue), Int(e.type.rawValue), eq))
    }

    func refresh() {
        let sw = NSScreen.main?.frame.width ?? 0
        let loc = NSEvent.mouseLocation
        func line(_ n: String, _ t: Totals) -> String {
            String(format: "%@ events=%d nonzero=%d\n  sumDX=%.0f sumDY=%.0f  |dx|=%.0f |dy|=%.0f\n  delta==locdiff: %d  locX range %.0f..%.0f",
                   n, t.events, t.nonzero, t.sx, t.sy, t.ax, t.ay, t.matchLoc,
                   t.minX.isFinite ? t.minX : 0, t.maxX.isFinite ? t.maxX : 0)
        }
        label.stringValue = """
        mode: \(game ? "GAME (Esc, 20 s timeout, deactivation exits)" : "absolute (click inside to enter game mode)")
        location now: \(Int(loc.x)), \(Int(loc.y))   screen width: \(Int(sw))
        \(line("ABS ", abs))
        \(line("GAME", gm))
        GCMouse (\(gcMouse)): sumX=\(Int(gcX)) sumY=\(Int(gcY)) |x|=\(Int(gcAbsX)) |y|=\(Int(gcAbsY))
        game sumDX in screen widths: \(sw > 0 ? String(format: "%.2f", gm.sx / Double(sw)) : "?")
        uptime \(Int(Date().timeIntervalSince(t0))) s (quits at 180 s)
        """
    }

    func windowDidResignKey(_ n: Notification) { leaveGame("deactivation") }
    func applicationDidResignActive(_ n: Notification) { leaveGame("deactivation") }
    func applicationWillTerminate(_ n: Notification) {
        leaveGame("quit")
        CGAssociateMouseAndMouseCursorPosition(1)
        log("# run end \(summary("abs", abs))")
        flush()
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ a: NSApplication) -> Bool { true }
}

let app = NSApplication.shared
app.setActivationPolicy(.regular)
let lab = Lab()
app.delegate = lab
app.run()
