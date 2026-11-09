// swift-tools-version: 6.0
import PackageDescription

let package = Package(
  name: "MyContextCLI",
  platforms: [.macOS(.v13)],
  products: [.executable(name: "myctx", targets: ["myctx"])],
  targets: [
    .target(name: "MyContextCLI"),
    .executableTarget(name: "myctx", dependencies: ["MyContextCLI"]),
    .testTarget(name: "MyContextCLITests", dependencies: ["MyContextCLI"]),
  ]
)
