// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "KeepgoingMenu",
    platforms: [.macOS(.v13)],
    products: [.executable(name: "KeepgoingMenu", targets: ["KeepgoingMenu"])],
    targets: [
        .target(name: "KeepgoingMenuCore"),
        .executableTarget(name: "KeepgoingMenu", dependencies: ["KeepgoingMenuCore"]),
        .testTarget(name: "KeepgoingMenuCoreTests", dependencies: ["KeepgoingMenuCore"])
    ]
)
