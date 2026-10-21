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

func status(_ phase: PairingStatus.Phase, notice: PairingStatus.Notice? = nil, other: Bool = false, devices: [PairedDevice] = []) -> PairingStatus {
    var s = PairingStatus()
    s.phase = phase; s.notice = notice; s.anotherDeviceTrying = other; s.devices = devices
    return s
}
let desk = PairedDevice(name: "Desk PC", key: Data(repeating: 1, count: 32))
let laptop = PairedDevice(name: "Living room laptop", key: Data(repeating: 2, count: 32))
let states: [(String, PairingStatus)] = [
    ("mac-1-closed", status(.closed)),
    ("mac-2-waiting", status(.waiting, devices: [desk])),
    ("mac-3-confirm", status(.confirm(code: "647197", device: "Desk PC"), devices: [desk, laptop])),
    ("mac-4-another-device", status(.confirm(code: "647197", device: "Desk PC"), other: true)),
    ("mac-5-paired", status(.closed, notice: .paired("Desk PC"), devices: [desk])),
    ("mac-6-rejected", status(.waiting, notice: .rejected)),
    ("mac-7-locked", status(.lockedOut(seconds: 23 * 3600), notice: .closedAfterFailures, devices: [desk])),
]
let controller = PairingWindowController()
for (name, s) in states {
    controller.update(s)
    guard let png = controller.renderPNG() else {
        FileHandle.standardError.write(Data("could not render \(name)\n".utf8))
        exit(1)
    }
    try png.write(to: dir.appendingPathComponent(name + ".png"))
    print(name)
}
