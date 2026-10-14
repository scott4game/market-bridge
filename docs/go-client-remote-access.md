# go-client HTTPS remote access

Local and remote are distinct listeners sharing one cache/live/news backend. Native local default remains `127.0.0.1:17600`; existing local scripts/UI/MCP continue without the new remote credential. Docker's existing container listener remains `0.0.0.0:17600`, **published only to host loopback**. Never publish or proxy the local unauthenticated listener to a remote network.

## Enable by specifying a port

There is no enable/disable boolean and no default remote port.

- `GO_CLIENT_REMOTE_PORT` absent: remote server OFF.
- Set `GO_CLIENT_REMOTE_PORT=17601`, or `go-client serve --remote-port 17601`: remote listener ON at `0.0.0.0:17601`.
- CLI port overrides environment, including correcting an invalid environment value.
- Explicitly empty, zero, negative, nonnumeric, >65535 ports are invalid. Remove the variable to disable remote mode.
- `GO_CLIENT_REMOTE_TOKEN` is required when enabled: at least 32 characters, no whitespace. It must differ from `GO_CLIENT_SERVER_TOKEN`.
- Token is environment-only, not a command argument. Do not commit it in configuration.
- Local/remote port conflicts and already occupied sockets fail startup; no partial listener is left running. Choose a free port: a go-server on the same host may already use 17601.

Both listeners shut down together. This is a personal instance with **one upstream account**, not independent users or tenant isolation.

## HTTPS gateway

TLS terminates at your existing reverse proxy. The proxy forwards to the **authenticated remote port**, never port 17600. Token validation is performed by go-client even when the connection originates from loopback or has spoofed forwarding headers. Missing/wrong token returns 401; URL tokens, cookies and Basic auth are not accepted.

Example nginx (certificate directives/domain supplied by your deployment):

```nginx
# http context
map $http_upgrade $connection_upgrade {
    default upgrade;
    '' close;
}
# inside an HTTPS server block
location / {
    proxy_pass http://127.0.0.1:17601;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Authorization $http_authorization;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection $connection_upgrade;
    proxy_buffering off;
    proxy_read_timeout 600s;
}
```

Protect the upstream HTTP hop using loopback/private networking. Native remote mode binds all interfaces, so firewall it to the TLS gateway. Docker opt-in override `deploy/compose.client-remote.yaml` publishes the remote port to host loopback only. The base Compose file deliberately does not inject an empty remote-port variable or publish a remote port.

## Exposed surface

Remote allowlist: dataset ensure/read, bars, security catalogue/adjustments, provider/storage status, profile/usage/watchlist, news including SSE/WS, live trades/WS, options, market analytics, health/readiness. All require the remote token, including health checks and WebSocket handshakes.

Remote `/mcp` additionally requires the existing MCP enable setting. Its remote transport uses the same tool implementations behind authentication. Local `/mcp` keeps its existing loopback/Host/Docker checks. No existing local protection is relaxed.

Cache listing/pruning, dataset refresh, indicator/basket configuration writes, static web UI, and unlisted routes are unavailable remotely. The remote credential is removed before dispatch; upstream requests keep the configured **server** token. Neither credential is logged.

## iOS and Swift

Configure the HTTPS **go-client** URL and remote token, not go-server. SDK history uses `POST /v1/datasets/ensure` with a flat specification. `/v1/storage/status` validates the go-client endpoint. Data errors, warnings, truncation and optional completeness remain intact.

Live WS uses one fixed subscription per connection. Close/reconnect to change symbols; whole-subscription `unsubscribe` closes the socket. Partial unsubscribe is explicitly unsupported. Use `wss` with Authorization in the handshake, never query parameters.

After upgrading the iOS app, old direct-server configuration is cleared for reconnection. Model credentials, chat history, holdings and private strategy configuration are retained. The old market token is not reused; new token storage uses a separate Keychain namespace.

This change supplies code and deployment examples only; it does not automatically change running containers, TLS certificates, firewall rules or public DNS.
