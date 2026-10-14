import Foundation

/// Lossless decimal JSON. Unknown object fields are retained for evolving server contracts.
public enum JSONValue: Codable, Equatable, Sendable {
  case object([String: JSONValue])
  case array([JSONValue])
  case string(String)
  case number(Decimal)
  case bool(Bool)
  case null
  public init(from decoder: Decoder) throws {
    let c = try decoder.singleValueContainer()
    if c.decodeNil() {
      self = .null
    } else if let v = try? c.decode(Bool.self) {
      self = .bool(v)
    } else if let v = try? c.decode(String.self) {
      self = .string(v)
    } else if let v = try? c.decode(Decimal.self) {
      self = .number(v)
    } else if let v = try? c.decode([String: JSONValue].self) {
      self = .object(v)
    } else {
      self = .array(try c.decode([JSONValue].self))
    }
  }
  public func encode(to encoder: Encoder) throws {
    var c = encoder.singleValueContainer()
    switch self {
    case .object(let v): try c.encode(v)
    case .array(let v): try c.encode(v)
    case .string(let v): try c.encode(v)
    case .number(let v): try c.encode(v)
    case .bool(let v): try c.encode(v)
    case .null: try c.encodeNil()
    }
  }
  public subscript(_ key: String) -> JSONValue {
    if case .object(let v) = self { return v[key] ?? .null }
    return .null
  }
  public var string: String? {
    if case .string(let v) = self { return v }
    return nil
  }
  public var array: [JSONValue] {
    if case .array(let v) = self { return v }
    return []
  }
  public var object: [String: JSONValue] {
    if case .object(let v) = self { return v }
    return [:]
  }
  public var bool: Bool? {
    if case .bool(let v) = self { return v }
    return nil
  }
  public var decimal: Decimal? {
    switch self {
    case .number(let v): return v
    case .string(let v): return Decimal(string: v, locale: Locale(identifier: "en_US_POSIX"))
    default: return nil
    }
  }
  public var text: String {
    String(data: (try? JSONEncoder().encode(self)) ?? Data(), encoding: .utf8) ?? "null"
  }
  public static func parse(_ text: String) throws -> JSONValue {
    try JSONDecoder().decode(Self.self, from: Data(text.utf8))
  }
}
public enum MarketTime {
  public static func parse(_ value: String) -> Date? {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = f.date(from: value) { return d }
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: value)
  }
  public static func format(_ date: Date) -> String { ISO8601DateFormatter().string(from: date) }
}
public struct Bar: Sendable, Identifiable {
  public let raw: JSONValue
  public var id: String { symbol + "/" + timestamp }
  public var symbol: String { raw["symbol"].string ?? "" }
  public var timestamp: String { raw["timestamp"].string ?? "" }
  public var date: Date? { MarketTime.parse(timestamp) }
  public var open: Decimal? { raw["open"].decimal }
  public var high: Decimal? { raw["high"].decimal }
  public var low: Decimal? { raw["low"].decimal }
  public var close: Decimal? { raw["close"].decimal }
  public var volume: Decimal? { raw["volume"].decimal }
  public var completed: Bool? { raw["completed"].bool }
  public init(raw: JSONValue) { self.raw = raw }
}
public struct BarsResponse: Sendable {
  public let raw: JSONValue
  public init(raw: JSONValue) { self.raw = raw }
  public var bars: [Bar] { raw["bars"].array.map(Bar.init) }
  public var complete: Bool? { raw["complete"].bool }
  public var truncated: Bool? { raw["truncated"].bool }
  public var warning: String? { raw["warning"].string }
  public var source: String? { raw["source"].string }
}
public struct BarsRequest: Encodable, Sendable {
  public var symbols: [String], interval: String, from: String, to: String, session: String,
    adjustment: String
  public init(
    symbols: [String], interval: String, from: Date, to: Date, session: String = "regular",
    adjustment: String = "raw"
  ) {
    self.symbols = symbols
    self.interval = interval
    self.from = MarketTime.format(from)
    self.to = MarketTime.format(to)
    self.session = session
    self.adjustment = adjustment
  }
}
public struct MarketBridgeError: Error, LocalizedError, Sendable {
  public let status: Int
  public let code: String?
  public let message: String
  public let retryable: Bool?
  public let retryAfter: String?
  public var errorDescription: String? { "HTTP \(status): \(message)" }
}
