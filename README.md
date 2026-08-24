# market-bridge

面向量化研究、行情分析与本地回测的数据缓存网关。

market-bridge 将远程行情服务与本地分析环境分离，通过多级缓存减少重复拉取，并为 K 线图表和策略回测提供一致的数据访问入口。

```text
KLineChart / Go Strategy
          ↓
      go-client
          ↓
 Redis 热缓存 → Parquet 磁盘缓存
          ↓
      go-server
          ↓
 Market Data Provider
```

## 核心组件

- **go-server**：连接行情供应商，统一处理历史数据、实时行情、数据版本和访问认证。
- **go-client**：运行在本地分析环境中，为图表和策略提供统一 API，并管理 Redis 与 Parquet 缓存。
- **Redis**：保存高频访问的热数据，可在数据丢失后自动重建。
- **Parquet**：保存可配置 TTL 的本地磁盘缓存，支持离线分析和回测。

项目默认提供 Mock Provider，无需配置第三方行情密钥即可运行并验证完整链路。go-client 可以部署在个人工作站，go-server 则可部署在靠近行情供应商的云服务器。

## 配置

服务端与客户端使用彼此隔离的配置，避免将供应商密钥传入本地环境：

```bash
cp .env.server.example .env.server
cp .env.client.example .env.client
chmod 600 .env.server .env.client
```

所有密钥、授权和账户配置均由环境变量注入；`.env` 已被 Git 忽略，不会复制进镜像。安装器会拒绝空值和示例占位密码。若 GHCR package 是私有的，在 `.env` 中配置 `GHCR_USERNAME` 和只具有 `read:packages` 权限的 `GHCR_TOKEN`。

## 一键安装

### go-server

从 GHCR 安装指定版本：

```bash
sudo ./scripts/install-server.sh --env .env.server --version v0.1.0
```

尚未发布 Release 时，可从当前源码构建：

```bash
sudo ./scripts/install-server.sh --env .env.server --local-build
```

安装目录默认为 `/opt/market-bridge`。Linux systemd 可用时会安装并启用 `market-bridge-server.service`，系统重启后自动恢复 Compose stack。服务仅发布到宿主机 `127.0.0.1:17601`，不启动 Caddy，也不要求 `MARKET_DOMAIN`；公网 HTTPS/WSS 由宿主机 Nginx 提供。

Nginx 站点可将请求反向代理到：

```nginx
location / {
    proxy_pass http://127.0.0.1:17601;
    proxy_http_version 1.1;
    proxy_set_header Host $host;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
```

升级与卸载：

```bash
sudo ./scripts/upgrade-server.sh --version v0.2.0
sudo ./scripts/uninstall-server.sh
sudo ./scripts/uninstall-server.sh --purge-data  # 同时删除配置和 Docker volumes
```

升级失败时会恢复之前的 `.env` 版本并尝试重新启动旧镜像。普通卸载保留配置和数据。

### go-client

Docker + Redis 模式：

```bash
./scripts/install-client.sh --env .env.client --version v0.1.0 --mode docker
```

从当前源码构建 Docker 模式：

```bash
./scripts/install-client.sh --env .env.client --mode docker --local-build
```

不使用 Docker 的原生模式：

```bash
./scripts/install-client.sh --env .env.client --version v0.1.0 --mode native
```

原生 Linux 使用 systemd user service，macOS 使用 LaunchAgent；下载的 GitHub Release 二进制必须通过 `SHA256SUMS` 校验后才会安装。

升级与卸载：

```bash
./scripts/upgrade-client.sh --version v0.2.0
./scripts/uninstall-client.sh
./scripts/uninstall-client.sh --purge-data
```

安装成功后打开 <http://127.0.0.1:17600>。普通卸载保留 Parquet 缓存和 `.env`。

## 开发运行

```bash
GO_SERVER_DATA_VERSION=mock-v1 go run ./cmd/go-server
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client serve
```

预取和缓存管理：

```bash
GO_CLIENT_REDIS_ENABLED=false go run ./cmd/go-client fetch \
  --symbols AAPL,NVDA --interval 1m \
  --from 2025-01-02T14:30:00Z --to 2025-01-02T16:00:00Z

go run ./cmd/go-client cache list
go run ./cmd/go-client cache prune --expired
go run ./cmd/go-client cache refresh DATASET_ID
```

## Docker

Dockerfile 提供两个独立 target：

```bash
docker build --target go-server -t market-bridge-server:local .
docker build --target go-client -t market-bridge-client:local .
make docker
```

开发 Compose：

```bash
docker compose --profile server up --build
docker compose --profile local up --build
```

生产安装器分别使用 `deploy/compose.server.yaml` 和 `deploy/compose.client.yaml`。两个镜像都使用非 root UID/GID `10001`，数据目录为 `/data`。

## 供应商

- `GO_SERVER_PROVIDER=massive` 与 `MASSIVE_API_KEY` 启用 Massive 历史 aggregates。调整模式只代表拆股调整，不表示股息复权。
- Massive 调用量会持久化到服务端数据目录的 `usage.db`。免费档使用 `MASSIVE_PLAN_NAME=stocks_basic`、`MASSIVE_REQUESTS_PER_MINUTE=5`；`MASSIVE_REQUESTS_PER_MONTH=0` 表示月度不限额。通过受保护的 `GET /v1/providers/massive/usage` 或本地页面查看最近 60 秒、本月和累计调用量。计数只覆盖本 go-server 发出的请求，不包含同一 API Key 被其他程序使用的次数。
- `GO_SERVER_LIVE_PROVIDER=longbridge` 与 Longbridge 三项凭据启用单连接采集。关注池由 `GO_SERVER_WATCHLIST` 配置，最多 200 只。
- ClickHouse 默认关闭且不随项目部署。设置 `GO_SERVER_CLICKHOUSE_ENABLED=true` 并提供 `CLICKHOUSE_URL`、`CLICKHOUSE_DATABASE`、`CLICKHOUSE_USER`、`CLICKHOUSE_PASSWORD` 后，可将实时 bars、trades 和 depth 写入外部 ClickHouse；它用于实时行情留存，不作为请求缓存。

## 发布

推送 `vX.Y.Z` 签名 tag 后，GitHub Actions 会：

1. 执行 race test 和 vet。
2. 构建 Linux/macOS、amd64/arm64 的 client/server 压缩包。
3. 生成 `SHA256SUMS` 并创建 GitHub Release。
4. 构建并推送两个 amd64/arm64 GHCR 镜像，同时发布版本 tag 和 `latest`。

```bash
./scripts/release.sh v0.1.0
```

完整设计与验收标准见 [docs/architecture-plan.md](docs/architecture-plan.md)。
