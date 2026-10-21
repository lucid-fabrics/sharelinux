// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "machelper",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "HelperCore", targets: ["HelperCore"]),
        .library(name: "HelperRuntime", targets: ["HelperRuntime"]),
        .executable(name: "corepeer", targets: ["corepeer"]),
        .executable(name: "MouseHelper", targets: ["MouseHelper"]),
        .executable(name: "runtimeharness", targets: ["runtimeharness"]),
        .executable(name: "pairingpreview", targets: ["pairingpreview"]),
    ],
    targets: [
        .target(name: "HelperCore"),
        // The kernel's pcblist_n record layouts, mirrored from xnu with the
        // SDK's own in_addr_4in6. Offsets for TCPTable come from here.
        .target(name: "CTCPTable"),
        // Everything the app does except AppKit: event poster, controller with
        // stuck-button protection, environment monitors, UDP socket, wiring.
        // The real CGEvent poster and system sources live here too but are
        // never instantiated by tests.
        .target(name: "HelperRuntime", dependencies: ["HelperCore", "CTCPTable"]),
        // The menu bar app (AppKit, Carbon hotkey). Not exercised by tests.
        .executableTarget(name: "MouseHelper", dependencies: ["HelperRuntime", "PairingUI"]),
        // The pairing window (AppKit). Split out so a preview can render it.
        .target(name: "PairingUI", dependencies: ["HelperRuntime"]),
        // Renders the pairing window's states to PNG files, offscreen, with made-up
        // data: no window is shown, no key store, no permission. Not shipped.
        .executableTarget(name: "pairingpreview", dependencies: ["PairingUI", "HelperRuntime"]),
        // Line-driven test peer for the Go interop test. Not shipped.
        .executableTarget(name: "corepeer", dependencies: ["HelperCore"]),
        // The app's runtime on a real UDP socket with a fake poster and fake
        // environment sources, driven from the Go tests. Not shipped.
        .executableTarget(name: "runtimeharness", dependencies: ["HelperRuntime", "HelperCore"]),
        .testTarget(
            name: "HelperCoreTests",
            dependencies: ["HelperCore"],
            resources: [.copy("Resources/noise-vectors.json")]
        ),
        .testTarget(name: "HelperRuntimeTests", dependencies: ["HelperRuntime", "HelperCore"]),
    ]
)
