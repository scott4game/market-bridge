# go-server 部署、验证与故障排查

本文面向只部署 `go-server` 的运维场景，覆盖 Docker Compose、Nginx/OpenResty、历史数据接口、Longbridge/Binance 实时行情和常见故障排查。示例域名统一使用 `stock.example.com`，执行前请替换成实际域名。

## 1. Provider 分工

`go-server` 的历史数据与实时行情由两组独立配置控制：

```dotenv
# 美股历史数据：mock 或 massive
GO_SERVER_PROVIDER=massive
MASSIVE_API_KEY=...

# 港股/A 股历史和实时行情
GO_SERVER_LONGBRIDGE_HISTORY_ENABLED=true
LONGBRIDGE_APP_KEY=...
LONGBRIDGE_APP_SECRET=...
LONGBRIDGE_ACCESS_TOKEN=...

# Binance Spot 历史和实时公共行情（不需要 Binance API Key）
GO_SERVER_BINANCE_ENABLED=true

# 同时运行多个实时源
GO_SERVER_LIVE_PROVIDERS=longbridge,binance
```

不要把 `GO_SERVER_PROVIDER` 设置为 `longbridge` 或 `binance`。它只选择裸代码美股的基础历史 Provider；服务会根据代码后缀把 `.HK/.SH/.SZ` 路由到 Longbridge，把 `.BINANCE` 路由到 Binance。旧配置 `GO_SERVER_LIVE_PROVIDER=longbridge` 仍可用；需要多个实时源时使用复数配置。

默认不开启 Longbridge 深度订阅，以减少权限和订阅额度要求。如账号已有深度权限，可设置 `GO_SERVER_LONGBRIDGE_DEPTH_ENABLED=true`；Web 页面会在 K 线右侧显示逐笔成交和盘口，并在该开关关闭时明确提示。Longbridge 单次历史接口最多返回 1000 根，服务会自动分页；供应商的频率、市场权限和每月标的额度仍然适用。

### 1.1 市场代码和默认参数

| 市场 | 示例 | 默认 session | 页面 / API `auto` adjustment |
| --- | --- | --- | --- |
| 美股 | `AAPL` 或 `AAPL.US` | `regular` | `forward_adjusted` / `split_adjusted` |
| 港股 | `700.HK` | `regular` | `forward_adjusted` |
| 沪市 | `600519.SH` | `regular` | `forward_adjusted` |
| 深市 | `000001.SZ` | `regular` | `forward_adjusted` |
| Binance Spot | `BTCUSDT.BINANCE` | `continuous` | `raw` |

一个 dataset 不能混合证券与币圈代码。页面对美股显式发送 `forward_adjusted`；API 为兼容已有调用，省略 adjustment 或传 `auto` 时，美股仍解析为 `split_adjusted`。港股/A 股的 `auto` 为 `forward_adjusted`，Binance 为 `raw`。

美股 `regular` 的 `1h/2h/3h/4h` 统一从 Massive 30 分钟母线按纽约时间 `09:30` 锚定聚合，最后不足完整周期的 K 线仍返回。美股 `forward_adjusted` 使用拆股调整行情叠加累计分红因子；Stocks Basic 只支持最近两年，超出范围需升级 Massive 套餐或改用 `split_adjusted`。浏览器不提供起止时间控件，会按周期分块加载并在左拖时补齐至少两年；REST 接口仍要求显式 `from/to`。

## 2. Docker Compose 运维

### 2.1 Compose V2/V5 安装

推荐使用插件形式的 Compose，命令为 `docker compose`。Ubuntu/Debian 在配置了 Docker 官方软件源后可安装：

```bash
apt-get update
apt-get install -y docker-compose-plugin
docker compose version
```

如果 APT 找不到 `docker-compose-plugin`，可以只安装 CLI 插件，不升级或重启 Docker Engine。下面以 Linux x86-64 和固定版本为例：

```bash
mkdir -p /usr/local/lib/docker/cli-plugins
curl -fSL \
  https://github.com/docker/compose/releases/download/v5.5.0/docker-compose-linux-x86_64 \
  -o /usr/local/lib/docker/cli-plugins/docker-compose
chmod +x /usr/local/lib/docker/cli-plugins/docker-compose
docker compose version
```

安装 Compose 插件不会停止 Docker daemon 或现有容器。原来的 `docker-compose` V1 可以继续共存。

### 2.2 Compose V1 兼容

旧版 `docker-compose` 遇到以下错误：

```text
'name' does not match any of the regexes: '^x-'
```

说明它不支持 Compose Specification 的顶层 `name`；它通常也不支持 `pull_policy`。暂时只能使用 V1 时：

1. 删除顶层 `name:`。
2. 删除服务中的 `pull_policy:`。
3. 添加顶层 `version: "3.8"`。
4. 用 `-p market-bridge-server` 固定项目名。
5. 用独立的 `pull` 命令实现每次拉取镜像。

```bash
docker-compose -p market-bridge-server pull
docker-compose -p market-bridge-server up -d
```

### 2.3 配置校验、拉取与启动

```bash
cd ~/market-bridge

# 成功时没有输出
docker compose config --quiet

# 只下载镜像，不停止或重建容器
docker compose pull

# 创建或更新当前 Compose 项目
docker compose up -d
```

`docker compose ps` 只显示当前 Compose 项目的容器，`docker ps` 显示整台机器上所有运行中的容器：

```bash
docker compose ps
docker compose ps -a
docker ps
docker ps -a
```

执行 `docker compose pull` 只会拉取镜像，不会创建容器。`docker compose up -d` 只管理当前项目，不会重启其他独立项目的容器；但当当前服务的镜像或配置发生变化时，它可能重建当前服务。单实例部署会因此产生短暂中断。

### 2.4 修改 `.env` 后重新加载

`docker compose restart` 不会重新读取 `.env`。修改 `.env` 后必须重建：

```bash
docker compose up -d --force-recreate go-server
docker compose ps
docker compose logs --tail=100 go-server
```

只是在配置未变化时重启进程，才使用：

```bash
docker compose restart go-server
```

### 2.5 团队成员与个人 API Key

多账号模式有两类凭据：

- `GO_SERVER_TOKEN`：配置在 go-server 的 `.env` 中，是兼容旧部署的系统级管理员票据。它具有管理员权限，不应分发给普通成员。
- 个人 API Key：由管理员为团队成员签发，可单独撤销、设置配额和统计用量。一个用户可以为不同设备持有多个 Key。

#### 2.5.1 配置系统管理员票据

服务端 `.env` 必须保留一个足够长的随机 Token：

```dotenv
GO_SERVER_TOKEN=replace-with-a-long-random-token
```

修改后需要重建容器才能加载新配置：

```bash
docker compose up -d --force-recreate go-server
```

#### 2.5.2 创建团队用户

新增团队成员时，不要复制管理员 Token，而应先创建独立用户：

```bash
docker compose exec go-server go-server admin user create --name alice --role member
docker compose exec go-server go-server admin user list
```

`--role` 支持 `member` 和 `admin`。普通团队成员应使用 `member`；`admin` 可以访问管理级接口，只应授予受信任的管理员。

#### 2.5.3 签发个人 API Key

为 Alice 的电脑签发默认有效期一年的 Key：

```bash
docker compose exec go-server go-server admin key create --user alice --name laptop
```

命令会输出类似下面的内容：

```text
API key (shown once): mbk_前缀.完整密钥
```

完整 Key 只显示一次，应立即通过安全方式交给用户。需要自定义有效期时使用 Go duration 格式，例如签发一个有效期 30 天的 Key：

```bash
docker compose exec go-server go-server admin key create \
  --user alice \
  --name temporary \
  --expires-in 720h
```

一个用户可以拥有多个 Key，建议按设备或用途分别签发，方便单独撤销：

```bash
docker compose exec go-server go-server admin key create --user alice --name laptop
docker compose exec go-server go-server admin key create --user alice --name server
```

#### 2.5.4 配置用户的 go-client

成员应将自己的完整 API Key 写入其 go-client 的 `.env`，不要填写管理员的 `GO_SERVER_TOKEN`：

```dotenv
GO_CLIENT_SERVER_URL=https://stock.example.com
GO_CLIENT_SERVER_TOKEN=mbk_前缀.完整密钥
```

配置完成后更新并启动客户端：

```bash
docker compose pull
docker compose up -d
docker compose logs -f go-client
```

直接请求 go-server API 时，通过 Bearer Header 携带相同的个人 API Key：

```bash
curl \
  -H 'Authorization: Bearer mbk_前缀.完整密钥' \
  https://stock.example.com/v1/me
```

#### 2.5.5 配额管理

`member` 的默认配额为每分钟 600 个普通请求、20 个数据集请求、2 个并发数据集构建、3 条实时连接和合计 200 个实时标的。可以为指定用户覆盖默认值：

```bash
docker compose exec go-server go-server admin quota set --user alice \
  --requests-per-minute 1000 \
  --datasets-per-minute 30 \
  --concurrent-builds 3 \
  --live-connections 5 \
  --live-symbols 200
```

清除覆盖值并恢复角色默认配额：

```bash
docker compose exec go-server go-server admin quota clear --user alice
```

#### 2.5.6 查看、撤销与禁用

Key 列表只显示元数据和前缀，不会显示完整 Key：

```bash
docker compose exec go-server go-server admin key list --user alice
docker compose exec go-server go-server admin key revoke --prefix KEY_PREFIX
```

遗失完整 Key 时不能找回，应撤销旧 Key 并重新签发。需要停用成员的所有 Key 时，可以禁用整个用户：

```bash
docker compose exec go-server go-server admin user disable --name alice
docker compose exec go-server go-server admin user enable --name alice
```

撤销、到期或禁用用户后，新请求立即失败，已有 WebSocket 最迟 60 秒断开。

#### 2.5.7 数据保存与个人接口

认证数据保存在服务端数据卷的 `/data/auth.db`。备份服务端数据卷时应同时备份该文件；数据库中只保存 Key 哈希，不保存可恢复的完整 Key。

成员可通过以下接口查看自己的身份、配额和关注列表：

```text
GET /v1/me
GET /v1/me/usage
GET /v1/me/watchlist
PUT /v1/me/watchlist
GET /v1/providers/status
```

`GET /v1/providers/massive/usage` 只允许 admin 使用。个人 Watchlist 只是收藏列表；实时代码由活跃 WebSocket 连接按需订阅，并受账户实时标的额度约束。

## 3. Nginx/OpenResty 反向代理

如果域名专用于 `go-server`，整个域名应直接代理到 `17601`，不需要额外增加 `/api` 前缀，也不需要前端服务或 Authelia 配置：

```nginx
server {
    listen      443 ssl;
    server_name stock.example.com;

    charset utf-8;
    access_log /data/log/stock.example.com.access.log;
    error_log  /data/log/stock.example.com.error.log;

    # SSL 证书配置按当前环境保留

    location / {
        proxy_pass http://GO_SERVER_IP:17601;
        proxy_http_version 1.1;

        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Forwarded-Host $http_host;
        proxy_set_header X-Forwarded-Uri $request_uri;
        proxy_set_header X-Forwarded-Ssl on;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;

        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";

        proxy_redirect off;
        proxy_next_upstream error timeout invalid_header http_500 http_502 http_503;
        proxy_buffers 64 256k;
        client_body_buffer_size 128k;

        send_timeout 5m;
        proxy_read_timeout 600s;
        proxy_send_timeout 600s;
        proxy_connect_timeout 60s;
    }
}
```

如果 OpenResty 与 `go-server` 在同一台机器，优先使用：

```nginx
proxy_pass http://127.0.0.1:17601;
```

当前生产 Compose 默认只发布 `127.0.0.1:17601`。跨机器反向代理时，应使用受保护的内网链路，或调整端口监听并通过防火墙仅放行反向代理服务器。

检查并平滑重载：

```bash
openresty -t
systemctl reload openresty
```

## 4. 快速验证

以下示例先设置公共地址，并从部署目录中的 `.env` 加载 Token：

```bash
cd ~/market-bridge
export MARKET_BRIDGE_URL=https://stock.example.com
set -a
. ./.env
set +a
```

不要把 `.env` 或 Token 输出到日志、工单和聊天记录。

### 4.1 健康检查

```bash
curl -i "$MARKET_BRIDGE_URL/healthz"
```

预期：

```http
HTTP/1.1 200 OK
Content-Type: application/json

{"status":"ok"}
```

`/healthz` 只表示 HTTP 进程可用，不代表 Massive 或 Longbridge 上游已经连接成功。

### 4.2 鉴权检查

受保护的 `/v1/*` 接口需要 Bearer Token：

```bash
curl -i \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  "$MARKET_BRIDGE_URL/v1/providers/massive/usage"
```

未携带或携带错误 Token 时应返回 `401`。未启用 Massive 时，使用正确 Token 访问用量接口会返回 `404`。

### 4.3 创建历史数据集

下面请求 AAPL 的一小时 1 分钟 K 线。`GO_SERVER_PROVIDER=massive` 时数据来自 Massive；设置为 `mock` 时只会返回模拟数据。

```bash
response="$(curl -fsS -X POST \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  -H 'Content-Type: application/json' \
  "$MARKET_BRIDGE_URL/v1/datasets" \
  -d '{
    "symbols":["AAPL"],
    "interval":"1m",
    "from":"2025-01-02T14:30:00Z",
    "to":"2025-01-02T15:30:00Z",
    "session":"regular",
    "adjustment":"split_adjusted"
  }')"
printf '%s\n' "$response"
```

响应中的 `state` 为 `building` 或 `ready`。安装了 `jq` 时可以继续查询：

```bash
dataset_id="$(printf '%s' "$response" | jq -r .dataset_id)"

curl -fsS \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  "$MARKET_BRIDGE_URL/v1/datasets/$dataset_id" | jq

curl -fsS \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  "$MARKET_BRIDGE_URL/v1/datasets/$dataset_id/manifest" | jq
```

状态变成 `ready` 后，从 manifest 的 `partitions[].name` 取得文件名并下载：

```bash
partition_name="$(curl -fsS \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  "$MARKET_BRIDGE_URL/v1/datasets/$dataset_id/manifest" \
  | jq -r '.partitions[0].name')"

curl -fL \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  "$MARKET_BRIDGE_URL/v1/datasets/$dataset_id/files/$partition_name" \
  -o "$(basename "$partition_name")"
```

## 5. Longbridge 实时行情验证

### 5.1 确认容器配置

检查非敏感配置，并只报告密钥是否存在：

```bash
docker compose exec go-server sh -c '
for name in \
  GO_SERVER_LIVE_PROVIDER \
  GO_SERVER_LIVE_PROVIDERS \
  GO_SERVER_LONGBRIDGE_HISTORY_ENABLED \
  GO_SERVER_LONGBRIDGE_DEPTH_ENABLED \
  GO_SERVER_BINANCE_ENABLED \
  LONGBRIDGE_APP_KEY \
  LONGBRIDGE_APP_SECRET \
  LONGBRIDGE_ACCESS_TOKEN
do
  if [ -n "$(printenv "$name")" ]; then
    echo "$name=set"
  else
    echo "$name=MISSING"
  fi
done
'
```

日志应包含：

```text
Longbridge live provider enabled: watchlist=AAPL,NVDA
```

```bash
docker compose logs --tail=100 go-server
```

### 5.2 用 curl 验证 WebSocket 握手

`curl` 可以验证域名、HTTPS、Bearer Token、OpenResty WebSocket 转发和服务端握手，但不能方便地发送有效的 WebSocket 订阅帧：

```bash
curl --http1.1 -i -N --max-time 5 \
  -H "Authorization: Bearer $GO_SERVER_TOKEN" \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: SGVsbG9Xb3JsZDEyMzQ1Ng==" \
  "$MARKET_BRIDGE_URL/v1/live/ws"
```

成功时会先看到：

```http
HTTP/1.1 101 Switching Protocols
Connection: upgrade
Upgrade: websocket
```

随后出现 `curl: (28) Operation timed out` 是预期行为：服务端正在等待客户端发送订阅帧。某些网络代理还会在最前面显示 `HTTP/1.1 200 Connection established`，这也不是业务错误。

### 5.3 用 wscat 接收真实行情

macOS 或已安装 Node.js 的环境可以运行：

```bash
npx --yes wscat \
  -c "${MARKET_BRIDGE_URL/https:/wss:}/v1/live/ws" \
  -H "Authorization: Bearer $GO_SERVER_TOKEN"
```

连接成功后，在 `>` 提示符中输入：

```json
{"symbols":["AAPL"],"events":["bar","trade","depth"]}
```

服务端会按连接中的 `symbols` 动态订阅，单个 member 默认最多 200 个标的。直接把这段 JSON 输入普通 shell 会被 zsh/bash 当作命令，必须在 `wscat` 或 `websocat` 的连接内发送。

macOS 也可以使用 `websocat`：

```bash
brew install websocat
websocat \
  -H="Authorization: Bearer $GO_SERVER_TOKEN" \
  "${MARKET_BRIDGE_URL/https:/wss:}/v1/live/ws"
```

真实 Longbridge 行情应包含：

```json
{"cursor":{"stream_epoch":"lb-..."},"bar":{"source":"longbridge"}}
```

如果收到以下字段，说明仍在使用 Mock 实时源：

```json
{"cursor":{"stream_epoch":"mock"},"bar":{"source":"mock-live"}}
```

此时确认 `.env` 中设置了 `GO_SERVER_LIVE_PROVIDERS=longbridge,binance`（只用 Longbridge 时也可保留旧的单值配置），再执行：

```bash
docker compose up -d --force-recreate go-server
```

连接成功但暂时没有推送时，还需确认当前交易时段、订阅标的是否在 watchlist，以及 Longbridge 账户是否具备对应市场和数据类型的 OpenAPI 行情权限。未购买深度行情时，能收到 `bar`/`trade` 但收不到 `depth` 不代表连接失败。

## 6. Longbridge `1006 unexpected EOF` 排查

以下日志表示 `go-server` 已选择 Longbridge，但上游行情连接被远端或中间网络直接断开，当前不能稳定接收真实行情：

```text
close conn, err: websocket: close 1006 (abnormal closure): unexpected EOF
start reconnecting.
reconnect success
```

按以下顺序排查：

1. 停止其他使用相同 Longbridge 凭据的程序或服务器，排除重复连接。
2. 检查 Access Token 是否过期、撤销或已经重新生成；必要时生成新 Token。
3. 如果 Longbridge 开发者后台启用了 IP 白名单，加入当前服务器出口 IP。
4. 检查服务器到 Longbridge 行情网关的网络链路。
5. 检查区域配置；非明确需要时不要强制设置 `LONGBRIDGE_REGION=cn`。

查询服务器 IPv4 出口地址：

```bash
curl -4 https://api.ipify.org
echo
```

测试到 Longbridge `.com` 行情网关的基础 WebSocket 链路：

```bash
curl --http1.1 -i -N --max-time 5 \
  -H "Connection: Upgrade" \
  -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" \
  -H "Sec-WebSocket-Key: SGVsbG9Xb3JsZDEyMzQ1Ng==" \
  "https://openapi-quote.longbridge.com?version=1&codec=1&platform=9"
```

基础链路正常时应返回 `101 Switching Protocols` 并保持到超时。响应中出现 CloudFront、CDN 或接入点响应头不代表错误。这个测试不发送 Longbridge 二进制鉴权包，因此只能证明 DNS、TLS、WebSocket 升级和基础网络路径正常，不能证明 App Key、Access Token、IP 白名单或行情权限有效。

如果这里返回 `101` 并保持到超时，但 `go-server` 仍持续出现 `1006 unexpected EOF`，优先检查重复连接、Access Token、IP 白名单和 Longbridge 开发者权限。更新 Token、白名单或网络配置后重新创建容器：

```bash
docker compose up -d --force-recreate go-server
docker compose logs -f --since=30s go-server
```

恢复后再次通过 `wscat` 验证，最终以收到 `stream_epoch: lb-...` 或 `source: longbridge` 为准。只看到启动日志或 `/healthz` 成功不足以证明实时行情已经可用。

相关官方说明：

- [Longbridge Socket 控制命令与断开原因](https://open.longbridge.com/docs/socket/control-command)
- [Longbridge 接入地址与区域选择](https://open.longbridge.com/docs/getting-started)
- [Longbridge 行情权限等级](https://open.longbridge.com/docs/quote/overview)

## 7. 常用诊断命令

```bash
# 当前 Compose 项目状态
docker compose ps

# 最近日志
docker compose logs --tail=100 go-server

# 持续日志
docker compose logs -f go-server

# 容器健康状态（避免依赖项目生成的容器名称）
container_id="$(docker compose ps -q go-server)"
docker inspect \
  --format '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{end}}' \
  "$container_id"

# 本机绕过 Nginx 检查服务
curl -i http://127.0.0.1:17601/healthz

# 公网经过 Nginx 检查服务
curl -i https://stock.example.com/healthz
```
