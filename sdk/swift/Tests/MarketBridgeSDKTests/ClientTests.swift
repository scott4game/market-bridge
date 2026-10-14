import XCTest

@testable import MarketBridgeSDK

final class Stub: URLProtocol {
  static var handler: ((URLRequest) throws -> (Int, Data, [String: String]))!
  override class func canInit(with request: URLRequest) -> Bool { true }
  override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
  override func startLoading() {
    do {
      let (code, data, headers) = try Self.handler(request)
      client?.urlProtocol(
        self,
        didReceive: HTTPURLResponse(
          url: request.url!, statusCode: code, httpVersion: nil, headerFields: headers)!,
        cacheStoragePolicy: .notAllowed)
      client?.urlProtocol(self, didLoad: data)
      client?.urlProtocolDidFinishLoading(self)
    } catch { client?.urlProtocol(self, didFailWithError: error) }
  }
  override func stopLoading() {}
}
final class ClientTests: XCTestCase {
  func client() throws -> MarketBridgeClient {
    let config = URLSessionConfiguration.ephemeral
    config.protocolClasses = [Stub.self]
    return try MarketBridgeClient(
      baseURL: URL(string: "https://example.test/gateway")!,
      session: URLSession(configuration: config), tokenProvider: { "test-secret" })
  }
  func testHistoryWireAndPrecision() async throws {
    Stub.handler = { r in
      XCTAssertEqual(r.url?.path, "/gateway/v1/datasets/ensure")
      XCTAssertEqual(r.httpMethod, "POST")
      var body = r.httpBody ?? Data()
      if body.isEmpty, let stream = r.httpBodyStream {
        stream.open()
        defer { stream.close() }
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
          let n = stream.read(&buffer, maxLength: buffer.count)
          if n <= 0 { break }
          body.append(contentsOf: buffer.prefix(n))
        }
      }
      let payload = try JSONDecoder().decode(JSONValue.self, from: body)
      XCTAssertEqual(payload["symbols"].array, [.string("A")])
      XCTAssertEqual(payload["interval"].string, "1h")
      XCTAssertEqual(payload["spec"], .null)

      XCTAssertEqual(r.value(forHTTPHeaderField: "Authorization"), "Bearer test-secret")
      return (
        200,
        Data(
          #"{"bars":[{"symbol":"A","timestamp":"2026-10-13T14:30:00.123Z","close":"0.0000000123456789","completed":false}],"complete":false,"warning":"partial","new_field":42}"#
            .utf8), [:]
      )
    }
    let out = try await client().bars(
      .init(symbols: ["A"], interval: "1h", from: Date(), to: Date()))
    XCTAssertEqual(out.bars[0].close, Decimal(string: "0.0000000123456789"))
    XCTAssertNotNil(out.bars[0].date)
    XCTAssertEqual(out.complete, false)
    XCTAssertEqual(out.bars[0].completed, false)
    XCTAssertEqual(out.raw["new_field"].decimal, 42)
  }
  func testMissingCompletenessIsUnknown() throws {
    XCTAssertNil(BarsResponse(raw: try .parse("{\"bars\":[]}")).complete)
  }
  func testErrorsAndRedaction() async throws {
    for status in [401, 403, 429, 502] {
      Stub.handler = { _ in
        (
          status,
          Data(
            #"{"error":"token=test-secret","error_details":{"code":"upstream","retryable":true}}"#
              .utf8), ["Retry-After": "60"]
        )
      }
      do {
        _ = try await client().providers()
        XCTFail()
      } catch let e as MarketBridgeError {
        XCTAssertEqual(e.status, status)
        XCTAssertEqual(e.retryAfter, "60")
        XCTAssertFalse(e.message.contains("test-secret"))
        XCTAssertEqual(e.retryable, true)
      }
    }
    Stub.handler = { _ in (502, Data("<html>secret</html>".utf8), [:]) }
    do {
      _ = try await client().profile()
      XCTFail()
    } catch let e as MarketBridgeError { XCTAssertFalse(e.message.contains("<html>")) }
  }
  func testTransportCancellationAndTimeout() async throws {
    for code in [URLError.cancelled, .timedOut] {
      Stub.handler = { _ in throw URLError(code) }
      do {
        _ = try await client().providers()
        XCTFail()
      } catch let e as URLError { XCTAssertEqual(e.code, code) }
    }
  }
  func testURLAndDates() throws {
    XCTAssertThrowsError(try MarketBridgeClient(baseURL: URL(string: "http://example.test")!))
    XCTAssertThrowsError(
      try MarketBridgeClient(baseURL: URL(string: "https://user:secret@example.test")!))
    XCTAssertNotNil(MarketTime.parse("2026-10-13T14:30:00Z"))
    XCTAssertEqual(
      try JSONValue.parse("123.123456789123456789").decimal,
      Decimal(string: "123.123456789123456789"))
  }
}
final class FakeLive: LiveTransport, @unchecked Sendable {
  var sent: [String] = []
  var cancelled = false
  var delivered = false
  func send(_ message: URLSessionWebSocketTask.Message) async throws {
    if case .string(let s) = message { sent.append(s) }
  }
  func receive() async throws -> URLSessionWebSocketTask.Message {
    if delivered { throw URLError(.networkConnectionLost) }
    delivered = true
    return .string("{\"symbol\":\"INTC\",\"price\":\"30.123456789\"}")
  }
  func cancel() { cancelled = true }
}
final class LiveTests: XCTestCase {
  func testSubscribeUnsubscribeDisconnectAndClose() async throws {
    let socket = FakeLive()
    let live = LiveSubscription(transport: socket)
    try await live.send(action: "subscribe", symbols: ["INTC"], events: ["quote"])
    XCTAssertEqual(try JSONValue.parse(socket.sent[0])["action"].string, "subscribe")
    var iterator = live.messages().makeAsyncIterator()
    let first = try await iterator.next()
    XCTAssertEqual(first?["price"].decimal, Decimal(string: "30.123456789"))
    do {
      _ = try await iterator.next()
      XCTFail()
    } catch {}
    try await live.unsubscribe(symbols: ["INTC"], events: ["quote"])
    XCTAssertEqual(socket.sent.count, 1)
    XCTAssertTrue(socket.cancelled)
  }
}
extension ClientTests {
  func testRejectServerAtClientConnectionCheck() async throws {
    Stub.handler = { _ in (200, Data("{\"status\":\"ok\"}".utf8), [:]) }
    do {
      _ = try await client().storageStatus()
      XCTFail("not a go-client response")
    } catch {}
    Stub.handler = { r in
      XCTAssertEqual(r.url?.path, "/gateway/v1/storage/status")
      return (200, Data("{\"mode\":\"remote_clickhouse\",\"data_version\":\"v4\"}".utf8), [:])
    }
    let status = try await client().storageStatus()
    XCTAssertEqual(status["data_version"].string, "v4")
  }
}

final class PluginClientTests: XCTestCase {
  func testScopedClientKeepsGatewayAndRemoteCredential() async throws {
    let market = try MarketBridgeClient(baseURL: URL(string: "https://client.example/gateway")!, tokenProvider: { "fixture-remote-token" })
    let plugin = try market.pluginClient("strategy")
    let request = try await plugin.makeRequest("/v1/capabilities")
    XCTAssertEqual(request.url?.absoluteString, "https://client.example/gateway/v1/plugins/strategy/v1/capabilities")
    XCTAssertEqual(request.value(forHTTPHeaderField: "Authorization"), "Bearer fixture-remote-token")
    for invalid in ["../secret", "a/b", "", "Upper", "a?b"] { XCTAssertThrowsError(try market.pluginClient(invalid)) }
  }
}
