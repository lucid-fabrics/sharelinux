import AppKit
import HelperRuntime
import PairingUI

// Renders the pairing window in each of its states to PNG files, offscreen,
// with made-up devices and codes. Nothing is shown on screen, no Keychain or
// key is touched, no permission is asked for. Not shipped.
//
// usage: pairingpreview <output directory>

guard CommandLine.arguments.count == 2 else {
    FileHandle.standardError.write(Data("usage: pairingpreview <output directory>\n".utf8))
    exit(2)
}
let dir = URL(fileURLWithPath: CommandLine.arguments[1], isDirectory: true)
try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)

let app = NSApplication.shared
app.setActivationPolicy(.prohibited)

func status(
    _ phase: PairingStatus.Phase, notice: PairingStatus.Notice? = nil, other: Bool = false, devices: [PairedDevice] = [],
    pending: Data? = nil, collision: String? = nil, ignored: Int = 0
) -> PairingStatus {
    var s = PairingStatus()
    s.phase = phase; s.notice = notice; s.anotherDeviceTrying = other; s.devices = devices
    s.pendingKey = pending; s.nameCollision = collision; s.ignoredRecords = ignored
    return s
}
func key(_ seed: UInt8) -> Data { Data((0..<32).map { seed &+ UInt8($0) &* 7 }) }
let desk = PairedDevice(name: "Desk PC", key: key(1))
let laptop = PairedDevice(name: "Living room laptop", key: key(2))
let incoming = key(3)
let states: [(String, PairingStatus, Bool)] = [
    ("mac-1-closed", status(.closed), false),
    ("mac-2-waiting", status(.waiting, devices: [desk]), false),
    ("mac-3-confirm", status(.confirm(code: "647197", device: "Desk PC"), devices: [desk, laptop], pending: incoming), false),
    ("mac-3-confirm-dark", status(.confirm(code: "647197", device: "Desk PC"), devices: [desk, laptop], pending: incoming), true),
    ("mac-4-another-device", status(.confirm(code: "647197", device: "Desk PC"), other: true, pending: incoming), false),
    ("mac-5-paired", status(.closed, notice: .paired("Desk PC"), devices: [desk]), false),
    ("mac-6-rejected", status(.waiting, notice: .rejected), false),
    ("mac-7-locked", status(.lockedOut(seconds: 23 * 3600), notice: .closedAfterFailures, devices: [desk]), false),
    ("mac-8-lookalike", status(.confirm(code: "208514", device: "Desk \u{0420}\u{0421}"), devices: [desk], pending: incoming, collision: "Desk PC"), false),
    ("mac-9-session-active", status(.closed, notice: .refused(.sessionActive), devices: [desk, laptop]), false),
    ("mac-10-covered", status(.confirm(code: "647197", device: "Desk PC"), devices: [desk], pending: incoming), false),
]
let controller = PairingWindowController()
for (name, s, dark) in states {
    controller.update(s)
    if name == "mac-10-covered" {
        controller.isFullyVisible = { _ in false }
        controller.now = { 1_000_000 }
        controller.confirmClicked()  // refused, with the reason in the notice
    }
    guard let png = controller.renderPNG(dark: dark) else {
        FileHandle.standardError.write(Data("could not render \(name)\n".utf8))
        exit(1)
    }
    try png.write(to: dir.appendingPathComponent(name + ".png"))
    print(name)
}
