import XCTest

@testable import MarketBridgeSDK

/// Opt-in read-only check. No credentials in source, logs, or arguments.
final class LiveSmokeTests: XCTestCase {
  func testConfiguredServerReadOnly() async throws {
    let env = ProcessInfo.processInfo.environment
    guard env["MARKETBRIDGE_LIVE_TEST"] == "1", let base = env["MARKETBRIDGE_TEST_URL"],
      let url = URL(string: base), let token = env["MARKETBRIDGE_TEST_TOKEN"]
    else { throw XCTSkip("Live server test not configured") }
    let c = try MarketBridgeClient(
      baseURL: url, allowDevelopmentHTTP: url.scheme == "http", tokenProvider: { token })
    _ = try await c.storageStatus()
    let status = try await c.providers()
    XCTAssertNotEqual(status, .null)
    let bars = try await c.bars(
      .init(
        symbols: ["INTC"], interval: "1d", from: Date().addingTimeInterval(-7 * 86400), to: Date(),
        adjustment: "forward_adjusted"))
    XCTAssertFalse(bars.bars.isEmpty)
    XCTAssertTrue(bars.bars.allSatisfy { $0.symbol == "INTC" && $0.close != nil })
    print(
      "Read-only SDK smoke: provider status OK; INTC daily count=\(bars.bars.count); completeness=\(String(describing:bars.complete)); warning=\(bars.warning != nil)"
    )
  }
}
