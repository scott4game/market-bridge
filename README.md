# market-bridge

面向量化研究、行情分析与本地回测的数据缓存网关。

market-bridge 将远程行情服务与本地分析环境分离，通过多级缓存减少重复拉取，并为 K 线图表和策略回测提供一致的数据访问入口。

```text
KLineChart / Go Strategy
          ↓
      go-client
          ↓
 Redis 热缓存 → 当前唯一启用的 ClickHouse（近 730 天）
          ↓
      go-server
          ↓
 Market Data Provider
```

## 核心组件

- **go-server**：连接行情供应商，统一处理历史数据、实时行情、数据版本和访问认证。
- **go-client**：运行在本地分析环境中，为图表和策略提供统一 API，并管理 Redis 与 Parquet 缓存。
- **Redis**：保存高频访问和超过 730 天的按月历史缓存，可在数据丢失后自动重建。
- **ClickHouse**：保存最近 730 天的全市场已完成 1 分钟 K 线；服务端启用时客户端本地实例自动停用，服务端关闭时由客户端实例承担存储。
- **Parquet**：保留兼容的数据集缓存，支持离线分析和回测。

项目默认提供 Mock Provider，无需配置第三方行情密钥即可运行并验证完整链路。go-client 可以部署在个人工作站，go-server 则可部署在靠近行情供应商的云服务器。

## 配置

服务端与客户端使用彼此隔离的配置，避免将供应商密钥传入本地环境：

```bash
cp .env.server.example .env.server
cp .env.client.example .env.client
chmod 600 .env.server .env.client
```

所有密钥、授权和账户配置均由环境变量注入；`.env` 已被 Git 忽略，不会复制进镜像。安装器会拒绝空值和示例占位密码。服务镜像默认从公开的 Docker Hub 仓库拉取，无需登录镜像仓库。

### 团队多账号

`GO_SERVER_TOKEN` 继续作为兼容的管理员凭据。团队成员应使用独立 API Key，以便单独撤销、设置配额和记录用量。用户、Key、个人关注列表和审计记录保存在服务端数据卷的 `auth.db`，完整 Key 只在创建时显示一次。

```bash
# 创建成员
docker compose exec go-server go-server admin user create --name alice --role member

# 为成员签发默认有效期一年的 Key
docker compose exec go-server go-server admin key create --user alice --name laptop

# 查看用户和 Key 元数据（不会显示完整 Key）
docker compose exec go-server go-server admin user list
docker compose exec go-server go-server admin key list --user alice

# 单独撤销或禁用
docker compose exec go-server go-server admin key revoke --prefix KEY_PREFIX
docker compose exec go-server go-server admin user disable --name alice
```

成员把创建时得到的 Key 配置为本地 go-client 的 `GO_CLIENT_SERVER_TOKEN`。默认 member 配额为每分钟 600 个普通请求、20 个数据集请求、2 个并发构建、3 条实时连接和合计 200 个实时标的；可使用 `go-server admin quota set` 单独覆盖。实时行情根据活跃 WebSocket 连接按需订阅，不需要在服务端预先配置代码池。

## 一键安装

### go-server

推荐在服务器上创建一个空目录并运行部署准备脚本：

```bash
mkdir -p market-bridge-deploy && cd market-bridge-deploy
curl -fsSL https://raw.githubusercontent.com/scott4game/market-bridge/dev/deploy/docker-deploy.sh | bash

# 部署脚本运行后会生成 .env；请在启动容器前检查并配置
vi .env

docker compose up -d
docker compose logs -f go-server
```

脚本会下载 `compose.yaml` 和 `.env.example`，随后生成可直接编辑的 `.env` 和随机 `GO_SERVER_TOKEN`，并询问 Massive API Key；输入 Key 会自动启用 Massive Provider，直接回车则以 Mock Provider 启动。请保管好 `.env` 中的 `GO_SERVER_TOKEN`：它是兼容旧部署的系统级管理员票据，不应分发给普通成员。团队成员连接 go-server 时应使用管理员为其签发的个人 API Key。`docker compose` 会自动读取当前目录中的 `.env`。

如果服务启动后再次修改 `.env`，需要重新创建容器才能应用新配置：

```bash
docker compose up -d --force-recreate
```

镜像直接从 `docker.io/otsgame/market-bridge-server:latest` 拉取，不需要下载源码或本机编译。端口仅绑定 `127.0.0.1:17601`，由宿主机 Nginx 提供公网 HTTPS/WSS。

需要固定版本时，部署完成后把 `.env` 中的 `MARKET_BRIDGE_VERSION` 改为发布标签，例如 `v0.2.0`，再执行 `docker compose up -d`。

原有的 systemd 安装方式仍可使用：

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

推荐在笔记本或其他客户端机器上安装 Docker Desktop（或 Docker Engine + Compose v2），创建空目录后通过部署准备脚本生成 `compose.yaml`、`.env.example` 和 `.env`。默认会同时启动 go-client、Redis 和本地 ClickHouse，并自动选择与机器匹配的 `linux/amd64` 或 `linux/arm64` 镜像：

```bash
mkdir -p market-bridge-client-deploy && cd market-bridge-client-deploy
curl -fsSL https://raw.githubusercontent.com/scott4game/market-bridge/dev/deploy/docker-client-deploy.sh | bash

# 填写 go-server 地址和个人 API Key（单用户兼容模式也可使用 GO_SERVER_TOKEN）
vi .env

docker compose pull
docker compose up -d
docker compose ps
```

准备脚本会下载客户端 Compose 和配置模板，并自动生成 Redis、ClickHouse 随机密码。至少需要修改：

```dotenv
GO_CLIENT_SERVER_URL=https://stock.example.com
GO_CLIENT_SERVER_TOKEN=管理员为当前用户签发的完整_API_Key
COMPOSE_PROFILES=clickhouse
GO_CLIENT_CLICKHOUSE_ENABLED=true
REDIS_MAXMEMORY=1gb
CLICKHOUSE_MEMORY_LIMIT=2g
CLICKHOUSE_CPUS=2
```

使用客户端本地 ClickHouse 时，服务端应设置 `GO_SERVER_CLICKHOUSE_ENABLED=false`；如果服务端已经启用 ClickHouse，go-client 会自动停止本地 ClickHouse 的业务读写，避免两侧双写。客户端仅在页面或 API 存在活跃订阅时接收并保存实时数据。

启动成功后打开 <http://127.0.0.1:17600>，也可以检查容器日志和实际存储模式：

```bash
docker compose logs -f go-client clickhouse
curl -fsS http://127.0.0.1:17600/v1/storage/status
```

Docker Hub 镜像为 `docker.io/otsgame/market-bridge-client:latest`。go-client、Redis 和 ClickHouse 端口均只绑定本机或 Compose 内部网络；ClickHouse HTTP 接口默认是 `127.0.0.1:18123`。

`REDIS_MAXMEMORY` 控制 Redis 容器内存上限，默认是 `1gb`；`CLICKHOUSE_MEMORY_LIMIT` 默认是 `2g`。内存较小的笔记本可以分别改为 `256mb` 和 `1g`，同时把 `CLICKHOUSE_CPUS` 改为 `1`。修改 `.env` 后必须重新创建容器才能应用，单纯执行 `docker compose restart` 不会重新读取配置：

```bash
docker compose up -d --force-recreate redis clickhouse go-client

# 1gb 应返回 1073741824
docker compose exec redis sh -c \
  'redis-cli -a "$REDIS_PASSWORD" CONFIG GET maxmemory'
```

Redis 达到上限后使用 `allkeys-lru` 淘汰较少访问的缓存；Parquet 磁盘缓存不受该内存上限影响。

已有客户端部署需要更新 Compose 和镜像时，保留现有 `.env`，在部署目录执行：

```bash
curl -fsSL \
  https://raw.githubusercontent.com/scott4game/market-bridge/dev/deploy/compose.client.yaml \
  -o compose.yaml
docker compose pull
docker compose up -d
```

需要从当前源码构建 Docker 模式时，仍可使用仓库内安装器：

```bash
./scripts/install-client.sh --env .env.client --mode docker --local-build
```

不使用 Docker 时，可以安装经过 SHA-256 校验的原生 Release：

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

### 交互式 K 线与公式指标

go-client 内置并自行托管 KLineChart `10.0.2`，页面运行时不依赖外部 CDN。图表支持鼠标滚轮缩放、拖动平移、十字光标和 OHLC 提示；`1m` 周期会继续接收 Longbridge 实时 bar，其他周期只展示历史数据，避免混入错误周期的数据。

页面的“管理指标”支持创建主图或副图公式，直接粘贴通达信技术指标语法，先执行参数识别和当前 K 线预览，再保存。指标在独立 Web Worker 中计算，单次最多 250000 根 K 线并有 10 秒超时，不会阻塞图表交互。系统最多保存 50 个个人指标，同时启用 12 个；公式最大 64 KiB、参数最多 32 个。

NX 牛熊分界线和 MX MACD 背离作为内置模板随每个团队账号自动创建。模板公式只读，但可以修改参数、开关显示，或点击“复制”生成可编辑的个人公式。配置通过 `GET/POST/PUT/DELETE /v1/me/indicators` 保存到 go-server，按账号隔离；go-client 会在浏览器中保留最近一次配置，在服务端暂时不可用时只读回退。API Key 需要 `indicators:read` / `indicators:write` scope，升级时现有角色 Key 会自动补齐。

公式支持常见行情字段、算术与逻辑表达式、EMA/MA/REF/LLV/HHV/BARSLAST 等技术函数，以及 `STICKLINE`、`DRAWTEXT` 等绘图语句。含 `REFX` 等未来函数时界面会醒目标注“历史信号可能重绘”。跨标的、选股、交易下单和公式中的外部数据源不在本功能范围；缺少参数或预览 K 线时不会保存。指标只根据当前加载范围计算，长周期 EMA 与背离指标应保留足够的预热数据。

KLineChart 使用 Apache License 2.0；公式解析器基于固定提交的 formula-ts（MIT）并包含项目兼容补丁。版本、许可证和修改说明记录在 [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md)。

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
- 设置 `GO_SERVER_LONGBRIDGE_HISTORY_ENABLED=true` 后，`700.HK`、`600519.SH`、`000001.SZ` 的历史 K 线由 Longbridge 提供；Longbridge 三项凭据仍由服务端统一保存。裸代码和 `.US` 继续走原有美股 Provider。
- 设置 `GO_SERVER_BINANCE_ENABLED=true` 后，`BTCUSDT.BINANCE` 这类代码使用 Binance Spot 公共行情，无需 Binance API Key。币种成交量同时返回精确字符串字段 `volume_decimal`。
- `GO_SERVER_LIVE_PROVIDERS=longbridge,binance` 可同时采集证券和币圈实时行情；兼容旧的单值 `GO_SERVER_LIVE_PROVIDER`。代码由活跃 WebSocket 连接按需订阅，无需服务端代码池。
- 标准代码格式为 `AAPL`/`AAPL.US`、`700.HK`、`600519.SH`、`000001.SZ` 和 `BTCUSDT.BINANCE`。证券默认 `regular` 时段，币圈默认 `continuous`；页面中的美股、港股和 A 股默认前复权，币圈固定原始价格。美股 `1h/2h/3h/4h` 按 Futu 风格从美东 `09:30` 锚定。
- 服务端 ClickHouse 默认关闭且不随服务端部署。资源受限的 `go-server` 可以一直保持 `GO_SERVER_CLICKHOUSE_ENABLED=false`。
- 客户端可设置 `GO_CLIENT_CLICKHOUSE_ENABLED=true`。go-client 会自动探测服务端：服务端 CH 开启时只使用远端 CH，并将本地 CH 逻辑关闭；服务端明确未开启 CH 时才写本地 CH。两者不会自动双写。最近730天进入唯一启用的 CH，超过730天的数据绕过 CH、按需从 Provider 拉取并缓存到客户端 Redis。详细配置见 [go-client 本地数据接口与策略验证指南](docs/go-client-data-api.md#2-客户端-clickhouse-实时镜像)。

## 发布

推送 `vX.Y.Z` 签名 tag 后，GitHub Actions 会：

1. 执行 race test 和 vet。
2. 构建 Linux/macOS、amd64/arm64 的 client/server 压缩包。
3. 生成 `SHA256SUMS` 并创建 GitHub Release。
4. 构建并推送两个 amd64/arm64 Docker Hub 与 GHCR 镜像，同时发布版本 tag 和 `latest`。

```bash
./scripts/release.sh v0.1.0
```

完整设计与验收标准见 [docs/architecture-plan.md](docs/architecture-plan.md)。

Grok、Claude Code、Codex 等本地 Agent 通过 API 读取行情时，见
[go-client 本地 AI Agent API](docs/local-ai-agent-api.md)。缓存、ClickHouse 和策略验证的
完整说明见 [go-client 本地数据接口与策略验证指南](docs/go-client-data-api.md)。

生产部署后的 Compose 升级、Nginx/OpenResty 配置、REST/WebSocket 验证及 Longbridge 常见故障排查，见 [go-server 部署、验证与故障排查](docs/server-operations.md)。
