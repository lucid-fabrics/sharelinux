// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "machelper",
    platforms: [.macOS(.v13)],
    products: [
        .library(name: "HelperCore", targets: ["HelperCore"]),
    ],
    targets: [
        .target(name: "HelperCore"),
        .testTarget(
            name: "HelperCoreTests",
            dependencies: ["HelperCore"],
            resources: [.copy("Resources/noise-vectors.json")]
        ),
    ]
)
