// swift-tools-version: 5.9
import PackageDescription
let package = Package(name: "MarketBridgeSDK", platforms: [.iOS(.v16), .macOS(.v13)], products: [.library(name: "MarketBridgeSDK", targets: ["MarketBridgeSDK"])], targets: [.target(name: "MarketBridgeSDK"), .testTarget(name: "MarketBridgeSDKTests", dependencies: ["MarketBridgeSDK"])])
