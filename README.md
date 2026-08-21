# massive-go

本项目实现日本笔记本本地缓存与新加坡行情服务的两层架构：

```text
KLineChart / Go strategy -> go-client -> Redis -> Parquet -> go-server -> provider
```

`go-server` 提供版本化 Parquet 数据集和实时 WebSocket；`go-client` 是本地唯一入口，托管 KLineChart，并提供可配置 TTL 的本地缓存。默认使用确定性 mock provider，无需密钥即可运行完整历史链路。

## 本地快速启动

终端一：

```bash
GO_SERVER_DATA_VERSION=mock-v1 go run ./cmd/go-server
```

终端二（没有 Redis 时会自动回退 Parquet）：

```bash
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client serve
```

打开 <http://127.0.0.1:17600>。也可以预取数据：

```bash
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client fetch \
  --symbols AAPL,NVDA --interval 1m \
  --from 2025-01-02T14:30:00Z --to 2025-01-02T16:00:00Z
```

缓存管理：

```bash
go run ./cmd/go-client cache list
go run ./cmd/go-client cache prune --expired
go run ./cmd/go-client cache refresh DATASET_ID
```

## Docker Compose

Dockerfile 提供两个互相独立的 production target，镜像中只包含对应服务：

```bash
docker build --target go-server -t massive-go-server:local .
docker build --target go-client -t massive-go-client:local .
```

也可以执行 `make docker` 一次构建两个镜像。两个镜像都使用 UID/GID `10001` 的非 root 用户运行，数据目录为 `/data`。

复制 `.env.example` 为 `.env`，然后按部署端启动对应 Compose profile：

```bash
docker compose --profile server up --build
docker compose --profile local up --build
```

Compose 会分别生成 `massive-go-server:${IMAGE_TAG:-local}` 和 `massive-go-client:${IMAGE_TAG:-local}`。新加坡与日本应使用各自的 Compose 项目；profile 只是将两端配置保存在同一仓库。

不使用 Compose 时，可以单独运行 mock go-server：

```bash
docker volume create massive-go-server-data
docker run --rm -p 17601:17601 \
  -e GO_SERVER_PROVIDER=mock \
  -e GO_SERVER_LIVE_PROVIDER=mock \
  -e GO_SERVER_DATA_DIR=/data \
  -v massive-go-server-data:/data \
  massive-go-server:local
```

go-client 通常与 Redis 一起通过 Compose 启动；若不使用 Redis，可直接运行：

```bash
docker volume create massive-go-client-data
docker run --rm -p 127.0.0.1:17600:17600 \
  --add-host host.docker.internal:host-gateway \
  -e GO_CLIENT_LISTEN=0.0.0.0:17600 \
  -e GO_CLIENT_SERVER_URL=http://host.docker.internal:17601 \
  -e GO_CLIENT_REDIS_ENABLED=false \
  -e GO_CLIENT_CACHE_DIRECTORY=/data \
  -v massive-go-client-data:/data \
  massive-go-client:local
```

## Massive

设置 `GO_SERVER_PROVIDER=massive` 和 `MASSIVE_API_KEY` 即可启用 Massive 历史 aggregates。Massive 的调整模式仅映射拆股调整，不表示股息复权。

设置 `GO_SERVER_LIVE_PROVIDER=longbridge` 并提供 `LONGBRIDGE_APP_KEY`、`LONGBRIDGE_APP_SECRET`、`LONGBRIDGE_ACCESS_TOKEN` 后启用 Longbridge 单连接采集。关注池由 `GO_SERVER_WATCHLIST` 配置，最多 200 只。Quote、Trade 和 Depth 会写入配置的 ClickHouse；表会自动创建，bars TTL 为一年，trades/depth TTL 为七天。真实运行仍需账户具备对应行情权限。

完整设计与验收标准见 [docs/architecture-plan.md](docs/architecture-plan.md)。
