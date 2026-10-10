// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "WrinkleCore",
    // iOS 26 is the app's floor. macOS is listed so `swift test` runs headless on
    // the Mac, against the backend on localhost.
    platforms: [
        .iOS("26.0"),
        .macOS("26.0"),
    ],
    products: [
        // The contract types, the connection and the local store. No UI.
        .library(name: "WrinkleCore", targets: ["WrinkleCore"]),
        // The session and the SwiftUI screens. Depends on WrinkleCore.
        .library(name: "WrinkleKit", targets: ["WrinkleKit"]),
    ],
    dependencies: [
        .package(url: "https://github.com/apple/swift-protobuf", from: "1.38.1")
    ],
    targets: [
        .target(
            name: "WrinkleCore",
            dependencies: [.product(name: "SwiftProtobuf", package: "swift-protobuf")],
            swiftSettings: [.swiftLanguageMode(.v6)]
        ),
        .target(
            name: "WrinkleKit",
            dependencies: ["WrinkleCore"],
            swiftSettings: [.swiftLanguageMode(.v6)]
        ),
        // WrinkleKit is a dependency so `swift test` compiles it for macOS too.
        .testTarget(
            name: "WrinkleCoreTests",
            dependencies: ["WrinkleCore", "WrinkleKit"],
            swiftSettings: [.swiftLanguageMode(.v6)]
        ),
    ]
)
