# MarketBridgeSDK (Swift)

A dependency-free Swift Package for iOS 16+ / macOS 13+. Calls **go-client directly**, using Foundation/URLSession. No running go-client, Go runtime, Redis, ClickHouse, SQLite, Parquet, or disk cache on the device. Server infrastructure remains server-side.

Add this **local directory** as a Swift Package in Xcode. The package is nested in the repository; adding the repository root as a remote Swift Package is not supported. Future standalone releases can publish this directory separately.

```swift
import MarketBridgeSDK
let client = try MarketBridgeClient(
    baseURL: URL(string: "https://your-client.example")!,
    tokenProvider: { /* read the user's scoped token from Keychain */ nil }
)
let response = try await client.bars(BarsRequest(
    symbols: ["INTC"], interval: "1d",
    from: Date().addingTimeInterval(-30 * 86400), to: Date(),
    adjustment: "forward_adjusted"
))
// response.complete == nil means UNKNOWN, not complete.
for bar in response.bars { print(bar.timestamp, bar.close as Any) }
```

## API coverage

- `bars`: POST `/v1/datasets/ensure`, sends a flat dataset specification.
- `universe`, `profiles`, `adjustments(symbol:)`.
- `news(query:)`: server news query fields and cursor pagination.
- `options(_:query:)`: expirations, chain, quotes, greeks, contracts; `optionBars`.
- `profile`, `watchlist`, `updateWatchlist`: update payload is `{"symbols":[...]}`. Caller must confirm user mutations.
- `providers` and `request` for explicit authenticated `/v1/` extensions.
- `live(symbols:events:)` returns a subscription. Consume `messages()`, explicitly unsubscribe the whole subscription or `close`; cancellation closes the socket. Disconnections are surfaced, not silently retried.

All responses retain unknown fields using `JSONValue`; numeric values retain Decimal precision and bar timestamps retain their wire strings. Bars expose optional `completed`. The SDK does not repair exchange calendars or infer missing candles. Existing HK segmentation/completion limitations must be handled by the application.

HTTP defaults: 30 seconds; history: 180 seconds. Both configurable. No automatic retries. Error status, structured code/retryability and Retry-After are exposed. Non-JSON failure bodies are not echoed. HTTPS required, redirects disabled; development HTTP requires explicit opt-in. Tokens are fetched per request, never persisted by the SDK. Use the independent go-client remote-access token, not the upstream server credential. Credentials are not sent to model providers by this SDK.

## Verification

```sh
swift test
```

`LiveSmokeTests` is skipped by default. For an explicitly authorized read-only smoke test, set `MARKETBRIDGE_LIVE_TEST=1`, `MARKETBRIDGE_TEST_URL`, `MARKETBRIDGE_TEST_TOKEN` in a protected process environment, never command arguments. Queries provider status and INTC recent daily bars; does not mutate watchlists or positions.

Private trading rules and AI prompts are deliberately outside this public package.

Remote access setup: [dual listeners and HTTPS](../../docs/go-client-remote-access.md). Local server mode needs no remote token; HTTPS clients use the authenticated remote listener. Call `storageStatus()` to validate a go-client endpoint. A connection has a fixed WS subscription: partial unsubscribe or a second subscribe is rejected; close and reconnect instead.
