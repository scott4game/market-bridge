# market-bridge

**English** | [简体中文](README.md)

A market-data caching gateway for quantitative research, chart analysis, and local backtesting.

market-bridge separates remote provider access from the local analysis environment. It reduces repeated downloads through layered caching and gives charts, strategies, and AI agents a consistent data interface.

```text
KLineChart / Go Strategy / AI Agent
                 |
             go-client
                 |
  Redis hot cache + active ClickHouse (recent 1,825 days)
                 |
             go-server
                 |
       Market Data Providers
```

## Components

- **go-server** connects to market-data providers and handles historical data, live feeds, data versions, authentication, quotas, and audit records.
- **go-client** runs in the local analysis environment and exposes the chart UI, REST API, and MCP endpoint while managing local caches.
- **Redis** stores hot data and monthly archive cache entries older than 1,825 days. Cache entries can be rebuilt from the source data.
- **ClickHouse** stores completed canonical bars for the most recent 1,825 days. Only one ClickHouse instance is active: the server-side instance takes precedence over the client-side instance.
- **Parquet** provides a compatible dataset cache for offline analysis and backtesting.

A mock provider is enabled by default, so the complete request path can be tested without third-party credentials. A typical deployment runs go-client on a workstation and go-server close to the upstream providers.

## Configuration

Server and client settings are kept in separate files so provider credentials do not need to be copied to local clients:

```bash
cp .env.server.example .env.server
cp .env.client.example .env.client
chmod 600 .env.server .env.client
```

Secrets, authorization data, and account settings are injected through environment variables. `.env` is ignored by Git and is not copied into container images. The installers reject empty values and example passwords.

### Multiple users

`GO_SERVER_TOKEN` remains available as a legacy administrator credential. Team members should use individual API keys so access can be revoked, metered, and audited independently. Users, keys, personal watchlists, and audit records are stored in `auth.db` in the server data volume. A complete API key is shown only once, when it is created.

```bash
# Create a member
docker compose exec go-server go-server admin user create --name alice --role member

# Issue a key with the default one-year lifetime
docker compose exec go-server go-server admin key create --user alice --name laptop

# List users and key metadata (complete keys are never displayed)
docker compose exec go-server go-server admin user list
docker compose exec go-server go-server admin key list --user alice

# Revoke a key or disable a user
docker compose exec go-server go-server admin key revoke --prefix KEY_PREFIX
docker compose exec go-server go-server admin user disable --name alice
```

Set the issued key as `GO_CLIENT_SERVER_TOKEN` on the member's machine. Default member quotas are 600 ordinary requests per minute, 20 dataset requests per minute, two concurrent builds, three live connections, and 200 live symbols in total. Override them with `go-server admin quota set`.

## Install go-server

Create an empty deployment directory and run the preparation script:

```bash
mkdir -p market-bridge-deploy && cd market-bridge-deploy
curl -fsSL https://raw.githubusercontent.com/scott4game/market-bridge/dev/deploy/docker-deploy.sh | bash

# Review the generated file and configure provider credentials before startup
vi .env

docker compose up -d
docker compose logs -f go-server
```

The script downloads `compose.yaml` and `.env.example`, generates an editable `.env` with a random `GO_SERVER_TOKEN`, and asks for a Massive API key. Supplying a key enables Massive; pressing Enter starts the mock provider. Keep `GO_SERVER_TOKEN` private and issue individual API keys to ordinary users.

When `.env` changes, recreate the container so Compose loads the new values:

```bash
docker compose up -d --force-recreate
```

Before upgrading an existing deployment, refresh the Compose template while preserving `.env`:

```bash
curl -fsSL \
  https://raw.githubusercontent.com/scott4game/market-bridge/dev/deploy/refresh-server-compose.sh |
  bash
docker compose pull
docker compose up -d --force-recreate go-server
```

The server image is `docker.io/otsgame/market-bridge-server:latest`. It binds to `127.0.0.1:17601`; expose it through an HTTPS/WSS reverse proxy when remote clients need access.

```nginx
location / {
    proxy_pass http://127.0.0.1:17601;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

To pin a release, set `MARKET_BRIDGE_VERSION` in `.env`, for example `v0.2.0`, and recreate the container.

The systemd installer is also available:

```bash
sudo ./scripts/install-server.sh --env .env.server --version v0.1.0
# Or build the current source before a release exists:
sudo ./scripts/install-server.sh --env .env.server --local-build
```

Upgrade or uninstall a systemd deployment with:

```bash
sudo ./scripts/upgrade-server.sh --version v0.2.0
sudo ./scripts/uninstall-server.sh
sudo ./scripts/uninstall-server.sh --purge-data
```

## Install go-client

On a workstation with Docker Desktop or Docker Engine and Compose v2:

```bash
mkdir -p market-bridge-client-deploy && cd market-bridge-client-deploy
curl -fsSL https://raw.githubusercontent.com/scott4game/market-bridge/dev/deploy/docker-client-deploy.sh | bash

# Set the go-server URL and the user's API key
vi .env

docker compose pull
docker compose up -d
docker compose ps
```

The default stack starts go-client, Redis, and a local ClickHouse instance. At minimum, review these values:

```dotenv
GO_CLIENT_SERVER_URL=https://stock.example.com
GO_CLIENT_SERVER_TOKEN=complete_personal_API_key
COMPOSE_PROFILES=local-redis,clickhouse
GO_CLIENT_REDIS_ENABLED=true
GO_CLIENT_CLICKHOUSE_ENABLED=true
REDIS_MAXMEMORY=1gb
CLICKHOUSE_MEMORY_LIMIT=2g
CLICKHOUSE_CPUS=2
```

If go-server already provides both Redis and ClickHouse, disable the local instances:

```dotenv
COMPOSE_PROFILES=
GO_CLIENT_REDIS_ENABLED=false
GO_CLIENT_CLICKHOUSE_ENABLED=false
```

Open <http://127.0.0.1:17600> after startup. Check logs and the active storage topology with:

```bash
docker compose logs -f go-client clickhouse
curl -fsS http://127.0.0.1:17600/v1/storage/status
```

The client image is `docker.io/otsgame/market-bridge-client:latest`. Client, Redis, and ClickHouse ports bind only to localhost or the Compose network. The default local ClickHouse HTTP port is `127.0.0.1:18123`.

After changing `.env`, recreate containers instead of using `docker compose restart`:

```bash
docker compose up -d --force-recreate redis clickhouse go-client
```

To build locally or install a verified native release:

```bash
./scripts/install-client.sh --env .env.client --mode docker --local-build
./scripts/install-client.sh --env .env.client --version v0.1.0 --mode native
```

## Local MCP server

`go-client serve` exposes the web UI, REST API, and a Streamable HTTP MCP endpoint in one process. No separate `go-mcp` program is required.

Run go-client directly on the host after configuring the upstream server and API key:

```bash
export GO_CLIENT_SERVER_URL=http://127.0.0.1:17601
export GO_CLIENT_SERVER_TOKEN=your-personal-api-key
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client serve
```

Configure an MCP client with:

```text
http://127.0.0.1:17600/mcp
```

For Codex, either run:

```bash
codex mcp add market_bridge --url http://127.0.0.1:17600/mcp
```

or add this to `~/.codex/config.toml`:

```toml
[mcp_servers.market_bridge]
url = "http://127.0.0.1:17600/mcp"
enabled = true
startup_timeout_sec = 10
tool_timeout_sec = 120
```

Available tools:

| Tool | Purpose |
| --- | --- |
| `get_bars` | Query bars using `symbol`, `interval`, RFC3339 `from/to`, and optional session, adjustment, and limit values. |
| `get_recent_trades` | Query recent Longbridge trades for a symbol. |
| `get_news` | Query the local news mirror using symbol/kind filters and sequence cursors. |
| `get_option_contracts` | Find US option contracts using expiry, type, strike, date, and offset filters. |
| `get_option_bars` | Query option bars for an `O:`-prefixed OCC ticker. |
| `get_provider_status` | Report upstream provider availability. |

Example `get_bars` arguments:

```json
{
  "symbol": "NVDA",
  "interval": "30m",
  "from": "2026-09-01T00:00:00Z",
  "to": "2026-09-08T00:00:00Z",
  "limit": 100
}
```

The MCP endpoint accepts loopback connections only and validates Host and Origin headers. Docker bridge networking does not satisfy this policy and may return HTTP 403 even when accessed through a host port. Run go-client directly on the host when using MCP. Set `GO_CLIENT_MCP_ENABLED=false` to disable the endpoint.

## Interactive charts and formula indicators

go-client embeds KLineChart `10.0.2`, so the chart does not depend on a CDN. It supports zooming, panning, crosshairs, OHLC tooltips, lazy historical loading, and one-minute Longbridge live bars.

The indicator manager accepts TongdaXin-style technical formulas. Formulas are parsed and previewed before saving, then evaluated in a dedicated Web Worker. A calculation is limited to 250,000 bars and ten seconds. Each client may store up to 50 personal indicators and enable up to 18 simultaneously.

Built-in templates include MA, EMA, BOLL, VOL, RSI, and KDJ. Common price fields, arithmetic and logical expressions, technical functions such as EMA/MA/REF/LLV/HHV/BARSLAST, and drawing statements such as `STICKLINE` and `DRAWTEXT` are supported. Formulas containing future functions such as `REFX` are marked because historical signals may repaint.

KLineChart is licensed under Apache License 2.0. The formula parser is based on a pinned formula-ts commit under the MIT license with project compatibility patches. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Development

Run both processes with the mock provider:

```bash
GO_SERVER_DATA_VERSION=mock-v1 go run ./cmd/go-server
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client serve
```

Prefetch data and manage the cache:

```bash
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client fetch \
  --symbols AAPL,NVDA --interval 1m \
  --from 2025-01-02T14:30:00Z --to 2025-01-02T16:00:00Z

go run ./cmd/go-client cache list
go run ./cmd/go-client cache prune --expired
go run ./cmd/go-client cache refresh DATASET_ID
```

Build and test:

```bash
go test ./...
go build ./cmd/go-server ./cmd/go-client
```

The Dockerfile contains separate targets:

```bash
docker build --target go-server -t market-bridge-server:local .
docker build --target go-client -t market-bridge-client:local .
make docker
```

## Providers and symbols

- `GO_SERVER_PROVIDER=massive` with `MASSIVE_API_KEY` enables US stocks and Massive futures history.
- `GO_SERVER_INDEX_PROVIDER=longbridge|fmp|massive|mock` selects the independent `I:` index-history provider.
- `GO_SERVER_A_SHARE_PROVIDER=tushare` with `TUSHARE_TOKEN` enables Shanghai and Shenzhen daily, weekly, and monthly history.
- `GO_SERVER_HK_PROVIDER=longbridge` enables Hong Kong history through Longbridge.
- `GO_SERVER_NEWS_PROVIDER=fmp` with `FMP_API_KEY` enables stock news and press releases.
- `GO_SERVER_OPTIONS_PROVIDER=massive` enables US option contracts and daily option bars.
- `GO_SERVER_BINANCE_ENABLED=true` enables public Binance Spot data without a Binance API key.
- `GO_SERVER_LIVE_PROVIDERS=longbridge,binance` enables both securities and crypto live feeds.

Canonical symbol examples are `AAPL`, `AAPL.US`, `I:VIX`, `F:MNQZ6`, `700.HK`, `600519.SH`, `000001.SZ`, and `BTCUSDT.BINANCE`. US, Hong Kong, and A-share charts default to adjusted prices; crypto uses raw prices. US `1h`, `2h`, `3h`, and `4h` bars are anchored at 09:30 America/New_York in Futu style.

## Releases and further documentation

Creating a signed `vX.Y.Z` tag runs race tests and vet, builds Linux/macOS amd64/arm64 archives, generates checksums and a GitHub Release, and publishes multi-architecture Docker images:

```bash
./scripts/release.sh v0.1.0
```

Additional documentation is currently maintained in Chinese:

- [Architecture and acceptance plan](docs/architecture-plan.md)
- [Local go-client data API and strategy guide](docs/go-client-data-api.md)
- [Local AI Agent API](docs/local-ai-agent-api.md)
- [Server operations and troubleshooting](docs/server-operations.md)
