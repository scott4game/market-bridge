# Environment-configured HTTP plugins

Optional go-client adapters for separately deployed private HTTP services. No dynamic code loading, private formulas or execution binaries belong in this repository. Absence of `GO_CLIENT_PLUGINS` leaves plugins disabled; `GET /v1/plugins` returns an empty list.

## Configuration

`GO_CLIENT_PLUGINS` is a JSON array (max 16 plugins). A definition contains:

- `id`: lowercase identifier, up to 32 characters, starting with a letter; remaining characters may include digits, `_` and `-`.
- `url`: fixed HTTP/HTTPS backend base URL, with optional path prefix. No userinfo, query, fragment or encoded path. Use HTTPS except on a trusted private/loopback hop.
- `token_env`: environment variable holding the backend Bearer token. Literal tokens in the JSON are rejected. Must differ from remote go-client and upstream market credentials.
- `timeout_seconds`: 1–180 seconds, default 30. No automatic retries.
- `routes`: 1–32 explicit `{method,path}` entries; GET/POST only. A whole `{id}` segment matches one simple identifier, never slashes or traversal. No wildcard routes.

Generic asynchronous-analysis example (replace paths to match your service):

```dotenv
GO_CLIENT_PLUGINS='[{"id":"analysis","url":"http://127.0.0.1:17890","token_env":"GO_CLIENT_PLUGIN_TOKEN","timeout_seconds":30,"routes":[{"method":"GET","path":"/v1/capabilities"},{"method":"POST","path":"/v1/scans"},{"method":"GET","path":"/v1/jobs/{id}"},{"method":"POST","path":"/v1/positions/evaluate"},{"method":"GET","path":"/v1/research/reports/{id}"}]}]'
GO_CLIENT_PLUGIN_TOKEN="REPLACE_WITH_THE_BACKEND_SERVICE_TOKEN"
```

Store real credentials in protected environment/secrets, not tracked files. Invalid configuration or missing credentials fails startup with sanitized errors. Restart go-client after changing configuration. This feature does not start, install or supervise the backend service.

Remote access remains opt-in through `GO_CLIENT_REMOTE_PORT` and `GO_CLIENT_REMOTE_TOKEN`; configuring plugins alone does not open a remote listener. Both local and remote listeners share the same configured plugin registry. Local access retains the existing trusted-local model; all remote plugin routes require the remote token.

## HTTP contract

- `GET /v1/plugins` returns IDs, public base paths and allowed routes; never backend URL, token or token-env name.
- `/v1/plugins/<id>/<backend-path>` forwards only an allowed method/path to the configured backend.
- Example: `POST /v1/plugins/analysis/v1/scans` → backend `POST /v1/scans`.
- The caller supplies the **go-client remote token**. go-client removes caller credentials/cookies/forwarding headers and attaches only the configured backend Bearer token. Plugin credentials stay on the server.
- GET query parameters are passed as data, not used to choose a backend. POST requires a JSON object, max 128 KiB; responses max 16 MiB. Responses exceeding the limit fail explicitly.
- Preserves JSON response bodies and HTTP status, including 202 task IDs, partial results, 401/403/429 and Retry-After. Non-JSON responses become sanitized structured errors with original error status; non-JSON success becomes 502.
- No redirects are followed. Network failures return 502, deadline 504, cancelled work is cancelled upstream. No background retry or result cache.
- Unknown plugin/path returns 404; unlisted methods and encoded/traversal paths cannot reach a backend.

This is one personal upstream identity per plugin, not independent multi-user authorization. Only configure trusted services/routes appropriate for everyone authorized to access this go-client instance.

## MCP

When MCP and at least one plugin are configured, discovery includes:

- `list_plugins`: read-only route discovery.
- `query_plugin`: allowlisted GET, arguments `plugin`, `path`.
- `submit_plugin`: allowlisted POST, arguments `plugin`, `path`, JSON object `body`; not advertised as read-only because it can enqueue work.

Results include `http_status`, `ok`, `result` and `retry_after`. A failed plugin request remains explicitly failed, never converted to an empty successful result. Existing market tools and local/remote MCP access checks remain unchanged.

## Swift / iOS

```swift
let catalogue = try await market.plugins()
let analysis = try market.pluginClient("analysis")
let capabilities = try await analysis.request("/v1/capabilities")
let job = try await analysis.request("/v1/scans", method: "POST",
    body: .object(["symbols": .array([.string("INTC")]), "strategy": .string("YOUR_STRATEGY")]))
```

Clients that already support a configurable backend base URL can point it to `https://your-go-client/v1/plugins/analysis` and use the **go-client remote token**, keeping backend credentials off the device. A strategy service capability response remains the service's own response; the public client implements no strategy formulas.

## Containers

`deploy/compose.client-plugins.yaml` is an opt-in example that passes the JSON and one referenced token into the existing go-client container; the base Compose config remains unchanged. For multiple plugins, explicitly pass each token environment variable too.

Inside Docker, `127.0.0.1` is the go-client container, not your host or another container. Use a reachable service name on a private Docker network for a separate backend, and ensure the backend binds to its container interface. Do not publish unauthenticated local market ports to the Internet. Host-side strategy processes that listen only on loopback may require a native go-client or a private networking adjustment; this feature does not change listeners or firewall rules automatically.
