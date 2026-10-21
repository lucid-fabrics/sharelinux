// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "machelper",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "HelperCore", targets: ["HelperCore"]),
        .executable(name: "corepeer", targets: ["corepeer"]),
    ],
    targets: [
        .target(name: "HelperCore"),
        // Line-driven test peer for the Go interop test. Not shipped.
        .executableTarget(name: "corepeer", dependencies: ["HelperCore"]),
        .testTarget(
            name: "HelperCoreTests",
            dependencies: ["HelperCore"],
            resources: [.copy("Resources/noise-vectors.json")]
        ),
    ]
)
