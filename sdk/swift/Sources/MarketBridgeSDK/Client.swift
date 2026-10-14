import Foundation

public final class MarketBridgeClient: @unchecked Sendable {
  public typealias TokenProvider = @Sendable () async throws -> String?
  public let baseURL: URL
  private let session: URLSession
  private let token: TokenProvider
  public let timeout: TimeInterval
  public let historyTimeout: TimeInterval
  public init(
    baseURL: URL, session: URLSession = .shared, timeout: TimeInterval = 30,
    historyTimeout: TimeInterval = 180, allowDevelopmentHTTP: Bool = false,
    tokenProvider: @escaping TokenProvider = { nil }
  ) throws {
    guard baseURL.host != nil, baseURL.user == nil, baseURL.password == nil, baseURL.query == nil,
      baseURL.fragment == nil,
      baseURL.scheme == "https" || (allowDevelopmentHTTP && baseURL.scheme == "http")
    else { throw URLError(.badURL) }
    self.baseURL = baseURL
    self.session = session
    self.token = tokenProvider
    self.timeout = timeout
    self.historyTimeout = historyTimeout
  }
  /// Discover configured plugin IDs and allowlisted routes; no backend secrets are returned.
  public func plugins() async throws -> JSONValue { try await request("/v1/plugins") }
  /// A scoped client reuses the go-client remote credential, never a backend plugin credential.
  public func pluginClient(_ id: String) throws -> MarketBridgeClient {
    guard id.range(of: "^[a-z][a-z0-9_-]{0,31}$", options: .regularExpression) != nil else { throw URLError(.badURL) }
    var parts = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)!
    parts.path = parts.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    parts.path = (parts.path.isEmpty ? "" : "/" + parts.path) + "/v1/plugins/" + id
    guard let url = parts.url else { throw URLError(.badURL) }
    return try MarketBridgeClient(baseURL: url, session: session, timeout: timeout,
      historyTimeout: historyTimeout, allowDevelopmentHTTP: baseURL.scheme == "http", tokenProvider: token)
  }
  public func request(
    _ path: String, method: String = "GET", query: [String: String] = [:], body: JSONValue? = nil,
    timeout: TimeInterval? = nil
  ) async throws -> JSONValue {
    let req = try await makeRequest(
      path, method: method, query: query, body: body, timeout: timeout)
    let (data, response) = try await session.data(for: req, delegate: NoRedirect())
    guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
    return try Self.decode(
      data, response: http, secret: req.value(forHTTPHeaderField: "Authorization"))
  }
  public func makeRequest(
    _ path: String, method: String = "GET", query: [String: String] = [:], body: JSONValue? = nil,
    timeout: TimeInterval? = nil
  ) async throws -> URLRequest {
    guard path.hasPrefix("/v1/"), !path.contains(".."), !path.contains("?"), !path.contains("#")
    else { throw URLError(.badURL) }
    var parts = URLComponents(url: baseURL, resolvingAgainstBaseURL: false)!
    parts.path = parts.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    parts.path = (parts.path.isEmpty ? "" : "/" + parts.path) + path
    parts.queryItems =
      query.isEmpty
      ? nil : query.sorted { $0.key < $1.key }.map { URLQueryItem(name: $0.key, value: $0.value) }
    guard let url = parts.url else { throw URLError(.badURL) }
    var req = URLRequest(url: url)
    req.httpMethod = method
    req.timeoutInterval = timeout ?? self.timeout
    req.setValue("application/json", forHTTPHeaderField: "Accept")
    if let t = try await token(), !t.isEmpty {
      req.setValue("Bearer " + t, forHTTPHeaderField: "Authorization")
    }
    if let body {
      req.httpBody = try JSONEncoder().encode(body)
      req.setValue("application/json", forHTTPHeaderField: "Content-Type")
    }
    return req
  }
  public static func decode(_ data: Data, response: HTTPURLResponse, secret: String? = nil) throws
    -> JSONValue
  {
    let parsed = try? JSONDecoder().decode(JSONValue.self, from: data)
    guard (200..<300).contains(response.statusCode) else {
      let detail = parsed?["error_details"] ?? .null
      // Do not echo unstructured upstream HTML or arbitrary secret-bearing bodies.
      var message = parsed?["error"].string ?? "服务返回非JSON或未知错误"
      if let secret, !secret.isEmpty {
        message = message.replacingOccurrences(of: secret, with: "[redacted]").replacingOccurrences(
          of: secret.replacingOccurrences(of: "Bearer ", with: ""), with: "[redacted]")
      }
      message = message.replacingOccurrences(
        of: #"(?i)(bearer\s+|(?:api[_-]?key|token|secret)\s*[=:]\s*)[^\s\"<>&]+"#,
        with: "$1[redacted]", options: .regularExpression)
      throw MarketBridgeError(
        status: response.statusCode, code: detail["code"].string,
        message: String(message.prefix(300)), retryable: detail["retryable"].bool,
        retryAfter: response.value(forHTTPHeaderField: "Retry-After")
          ?? detail["retry_after_seconds"].decimal.map { NSDecimalNumber(decimal: $0).stringValue })
    }
    if response.statusCode == 204 { return .null }
    guard let parsed else { throw URLError(.cannotParseResponse) }
    return parsed
  }
  public func bars(_ spec: BarsRequest) async throws -> BarsResponse {
    let value = try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(spec))
    return BarsResponse(
      raw: try await request(
        "/v1/datasets/ensure", method: "POST", body: value, timeout: historyTimeout)
    )
  }
  /// Validate that the configured endpoint is a go-client, not a go-server.
  public func storageStatus() async throws -> JSONValue {
    let result = try await request("/v1/storage/status")
    guard result["mode"].string != nil, result["data_version"].string != nil else {
      throw URLError(.cannotParseResponse)
    }
    return result
  }
  public func profiles() async throws -> JSONValue {
    try await request("/v1/market-history/security-profiles", timeout: historyTimeout)
  }
  public func universe() async throws -> JSONValue {
    try await request("/v1/market-history/universe", timeout: historyTimeout)
  }
  public func adjustments(symbol: String) async throws -> JSONValue {
    try await request("/v1/market-history/adjustments/" + Self.symbol(symbol))
  }
  public func news(query: [String: String] = [:]) async throws -> JSONValue {
    try await request("/v1/news", query: query)
  }
  public func options(_ resource: String, query: [String: String]) async throws -> JSONValue {
    guard ["expirations", "chain", "quotes", "greeks", "contracts"].contains(resource) else {
      throw URLError(.unsupportedURL)
    }
    return try await request("/v1/options/" + resource, query: query)
  }
  public func optionBars(contract: String, from: Date, to: Date) async throws -> JSONValue {
    try await request(
      "/v1/options/bars/" + Self.symbol(contract),
      query: ["from": MarketTime.format(from), "to": MarketTime.format(to)], timeout: historyTimeout
    )
  }
  public func profile() async throws -> JSONValue { try await request("/v1/me") }
  public func watchlist() async throws -> JSONValue { try await request("/v1/me/watchlist") }
  public func updateWatchlist(_ body: JSONValue) async throws -> JSONValue {
    try await request("/v1/me/watchlist", method: "PUT", body: body)
  }
  public func providers() async throws -> JSONValue { try await request("/v1/providers/status") }
  private static func symbol(_ s: String) throws -> String {
    guard !s.isEmpty, s.range(of: #"^[A-Za-z0-9:.^_-]+$"#, options: .regularExpression) != nil
    else { throw URLError(.badURL) }
    return s
  }
  public func live(symbols: [String], events: [String] = ["quote"]) async throws -> LiveSubscription
  {
    var req = try await makeRequest("/v1/live/ws")
    var parts = URLComponents(url: req.url!, resolvingAgainstBaseURL: false)!
    parts.scheme = parts.scheme == "https" ? "wss" : "ws"
    req.url = parts.url
    let task = session.webSocketTask(with: req)
    task.delegate = NoRedirect()
    task.resume()
    let stream = LiveSubscription(task: task)
    do { try await stream.send(action: "subscribe", symbols: symbols, events: events) } catch {
      stream.close()
      throw error
    }
    return stream
  }
}
private final class NoRedirect: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
  func urlSession(
    _ session: URLSession, task: URLSessionTask,
    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
    completionHandler: @escaping (URLRequest?) -> Void
  ) { completionHandler(nil) }
}
protocol LiveTransport: Sendable {
  func send(_ message: URLSessionWebSocketTask.Message) async throws
  func receive() async throws -> URLSessionWebSocketTask.Message
  func cancel()
}
private final class URLSocketTransport: LiveTransport, @unchecked Sendable {
  let task: URLSessionWebSocketTask
  init(_ task: URLSessionWebSocketTask) { self.task = task }
  func send(_ message: URLSessionWebSocketTask.Message) async throws {
    try await task.send(message)
  }
  func receive() async throws -> URLSessionWebSocketTask.Message { try await task.receive() }
  func cancel() { task.cancel(with: .normalClosure, reason: nil) }
}
public final class LiveSubscription: @unchecked Sendable {
  private let task: any LiveTransport
  private let lock = NSLock()
  private var subscription: (Set<String>, Set<String>)?
  init(task: URLSessionWebSocketTask) { self.task = URLSocketTransport(task) }
  init(transport: any LiveTransport) { self.task = transport }
  public func send(action: String, symbols: [String], events: [String]) async throws {
    let accepted = lock.withLock { () -> Bool in
      guard action == "subscribe", subscription == nil else { return false }
      subscription = (Set(symbols), Set(events))
      return true
    }
    guard accepted else { throw URLError(.unsupportedURL) }
    let body = JSONValue.object([
      "action": .string(action), "symbols": .array(symbols.map(JSONValue.string)),
      "events": .array(events.map(JSONValue.string)),
    ])
    try await task.send(.string(body.text))
  }
  public func unsubscribe(symbols: [String], events: [String]) async throws {
    let whole = lock.withLock {
      subscription.map { Set(symbols).isSuperset(of: $0.0) && Set(events).isSuperset(of: $0.1) }
        ?? false
    }
    guard whole else { throw URLError(.unsupportedURL) }
    // go-client uses a fixed subscription per connection; no second data frame is accepted.
    close()
  }
  public func close() { task.cancel() }
  public func messages() -> AsyncThrowingStream<JSONValue, Error> {
    AsyncThrowingStream { continuation in
      let reader = Task {
        do {
          while !Task.isCancelled {
            let message = try await task.receive()
            let data: Data
            switch message {
            case .data(let v): data = v
            case .string(let v): data = Data(v.utf8)
            @unknown default: continue
            }
            continuation.yield(try JSONDecoder().decode(JSONValue.self, from: data))
          }
          continuation.finish()
        } catch { continuation.finish(throwing: error) }
      }
      continuation.onTermination = { [weak self] _ in
        reader.cancel()
        self?.close()
      }
    }
  }
  deinit { close() }
}
