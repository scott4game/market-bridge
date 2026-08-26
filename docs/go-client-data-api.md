# go-client 本地数据接口与策略验证指南

Grok、Claude Code、Codex 等本地 Agent 需要一份精简接口契约时，优先使用
[go-client 本地 AI Agent API](local-ai-agent-api.md)。本文继续说明缓存、ClickHouse、
dataset 生命周期和策略验证细节。

本文面向运行在本机的交易机器人、研究脚本和回测程序。机器人只需要访问
`go-client`，不应直接访问远端 `go-server`，也不需要持有远端 API Key。

默认地址：`http://127.0.0.1:17600`

```text
机器人 / 回测程序
        |
        | REST + WebSocket（本机）
        v
    go-client
        |
        +-- Redis 热缓存
        +-- Parquet 本地缓存
        +-- ClickHouse 实时镜像（可选）
        +-- go-server（缓存未命中时）
```

## 1. 开始前检查

确认客户端已经启动：

```bash
curl -fsS http://127.0.0.1:17600/healthz
curl -fsS http://127.0.0.1:17600/readyz
```

正常响应：

```json
{"status":"ok"}
{"status":"ready"}
```

`readyz` 当前只表示 HTTP 进程可以接收请求，不保证远端行情服务或 Redis
一定可用。正式读取前可同时查看：

```bash
curl -fsS http://127.0.0.1:17600/v1/providers/status
```

本地接口默认不要求机器人再传认证头。`go-client` 使用自己的
`GO_CLIENT_SERVER_TOKEN` 访问远端服务。应保持监听地址为 `127.0.0.1:17600`，
不要直接暴露到公网。

## 2. 客户端 ClickHouse 实时镜像

远端服务器内存不足时，不需要在 `go-server` 所在机器部署 ClickHouse。客户端可以
独立运行 Redis、go-client 和 ClickHouse。go-client 每5分钟探测一次服务端能力，
并保证只有一侧 ClickHouse 承担业务读写：

```dotenv
GO_SERVER_CLICKHOUSE_ENABLED=false
```

客户端 Docker 配置示例：

```dotenv
COMPOSE_PROFILES=clickhouse
GO_CLIENT_CLICKHOUSE_ENABLED=true
GO_CLIENT_CLICKHOUSE_COMPLETED_BARS_ONLY=true
GO_CLIENT_CLICKHOUSE_URL=http://clickhouse:8123
CLICKHOUSE_DATABASE=market
CLICKHOUSE_USER=market
CLICKHOUSE_PASSWORD=使用随机长密码
CLICKHOUSE_MEMORY_LIMIT=2g
CLICKHOUSE_CPUS=2
```

go-client 根据页面或 SDK 的活跃 WebSocket 连接维持共享的上游按需订阅；没有客户端时
不会保留长期实时代码池。默认只把订阅期间收到的已完成 bar 写入 `market.kline_1m`，
以降低本地 ClickHouse 的写入和磁盘压力。若确实需要逐笔、深度和未完成 bar，可以把
`GO_CLIENT_CLICKHOUSE_COMPLETED_BARS_ONLY` 改为 `false`。

若服务端设置 `GO_SERVER_CLICKHOUSE_ENABLED=true`，客户端自动使用远端 ClickHouse，
停止本地 CH 的业务读写并在页面显示“本地 ClickHouse 存储关闭”。远端 CH 暂时故障
时客户端保持远端模式并告警，不会自动切成本地双写。服务端明确关闭 CH 后，本地 CH
才重新开始工作。

最近730天的一分钟数据进入当前唯一启用的 ClickHouse；清理器每720小时删除超过730天
的完整日分区。早于730天的查询始终绕过两侧 ClickHouse，经 go-server 直接访问
Massive/Longbridge，并按股票、周期和自然月缓存在本地 Redis，默认 TTL 为24小时。
跨越730天边界的请求会合并 CH 近期数据与 Provider 历史数据后去重排序。

可查看实际存储模式：

```bash
curl -fsS http://127.0.0.1:17600/v1/storage/status
```

Docker 部署在宿主机只绑定 `127.0.0.1:18123`。检查数据：

```bash
curl --user 'market:你的密码' \
  'http://127.0.0.1:18123/?query=SELECT%20count()%20FROM%20market.kline_1m'
```

每 3 分钟分析一次时，无需让大模型直接消费逐笔流。让调度器查询最近已完成的 1 分钟
bar，并在 SQL 中合成 3 分钟窗口：

```sql
SELECT
    symbol,
    toStartOfInterval(timestamp, INTERVAL 3 MINUTE) AS ts,
    argMin(open, timestamp) AS open,
    max(high) AS high,
    min(low) AS low,
    argMax(close, timestamp) AS close,
    sum(volume) AS volume
FROM market.kline_1m FINAL
WHERE completed = 1
  AND interval = '1m'
  AND symbol IN ('AAPL', 'NVDA')
  AND timestamp >= now() - INTERVAL 2 HOUR
GROUP BY symbol, ts
ORDER BY symbol, ts;
```

这里的 ClickHouse 是实时行情事实库；Redis 和 Parquet 仍承担现有请求缓存职责。首次启用
时执行 `docker compose up -d`，Compose 会等待本地 ClickHouse 健康后再启动 go-client。

首次回填最近一年的全市场 1 分钟数据（美股来自 Massive，A/H 股来自 Longbridge）：

```bash
docker compose run --rm go-client market-history --days 730 --workers 2
```

回填中途失败后可安全重跑，ClickHouse 使用版本列替换重复行；同一精确查询区间登记完整后
可以直接命中当前 ClickHouse。需要每日补最近两天时，可设置
`GO_CLIENT_MARKET_HISTORY_SYNC_ENABLED=true`；若 ClickHouse 部署在服务端，则改用对应的
`GO_SERVER_MARKET_HISTORY_SYNC_ENABLED=true`，两侧不要同时开启。

## 3. 推荐的历史 K 线接口

### 3.1 单标的：GET `/v1/bars/{symbol}`

这是单只股票策略最简单的读取方式。

```bash
curl --get 'http://127.0.0.1:17600/v1/bars/AAPL' \
  --data-urlencode 'interval=1d' \
  --data-urlencode 'from=2025-01-01T00:00:00Z' \
  --data-urlencode 'to=2026-01-01T00:00:00Z' \
  --data-urlencode 'session=regular' \
  --data-urlencode 'adjustment=forward_adjusted'
```

查询参数：

| 参数 | 必填 | 取值 | 说明 |
| --- | --- | --- | --- |
| `from` | 是 | RFC3339 时间 | 起点，按闭区间处理 |
| `to` | 是 | RFC3339 时间 | 终点，按开区间处理 |
| `interval` | 否 | 见下表 | 默认 `1m` |
| `session` | 否 | `regular` / `extended` / `continuous` | 按代码市场推断 |
| `adjustment` | 否 | `auto` / `raw` / `split_adjusted` / `forward_adjusted` | 默认 `auto`，按代码市场推断 |

支持的周期：

```text
1m  3m  5m  10m  15m  30m
1h  2h  3h  4h
1d  1w  1mo  1y
```

标的代码不区分大小写。`aapl`、`AAPL` 和 `AAPL.US` 都会规范化为 `AAPL`；
港股、A 股和币圈分别使用 `700.HK`、`600519.SH`、`000001.SZ`、
`BTCUSDT.BINANCE`。`.HK/.SH/.SZ` 由 Longbridge 提供，`.BINANCE` 由 Binance
Spot 提供。证券和币圈代码不能放进同一个 dataset。

`session` 会参与数据集标识并写入 bar。美股 `regular` 分钟线会过滤到美东
`09:30–16:00`，`1h/2h/3h/4h` 从 30 分钟母线按 `09:30` 锚点重新聚合；
`extended` 仍采用 Massive 原生边界。

页面默认值为：美股、港股/A 股 `regular + forward_adjusted`，Binance Spot
`continuous + raw`。API 的美股 `auto` 为兼容已有调用仍解析为 `split_adjusted`，
Agent 若要与页面一致应显式传 `forward_adjusted`。币圈成交量可能含
小数，读取时优先使用 `volume_decimal`；旧的 `volume` int64 字段为兼容字段。

响应示例：

```json
{
  "source": "parquet",
  "bars": [
    {
      "symbol": "AAPL",
      "timestamp": "2025-01-02T00:00:00Z",
      "open": "242.000000",
      "high": "245.000000",
      "low": "241.000000",
      "close": "244.000000",
      "volume": 32100456,
      "turnover": "7800000000.000000",
      "session": "regular",
      "source": "massive",
      "completed": true
    }
  ]
}
```

`turnover` 没有数据时会整个字段缺省，而不是 `0`。

### 3.2 多标的：POST `/v1/datasets/ensure`

组合策略或横截面策略应一次提交多个标的：

```bash
curl -fsS -X POST 'http://127.0.0.1:17600/v1/datasets/ensure' \
  -H 'Content-Type: application/json' \
  -d '{
    "symbols": ["AAPL", "MSFT", "NVDA"],
    "interval": "1h",
    "from": "2025-01-01T00:00:00Z",
    "to": "2026-01-01T00:00:00Z",
    "session": "regular",
    "adjustment": "forward_adjusted"
  }'
```

响应结构：

```json
{
  "source": "go-server",
  "count": 1234,
  "bars": []
}
```

`bars` 的字段与单标的接口相同。结果按 `timestamp` 升序排列；相同时间再按
`symbol` 升序排列。`symbols` 会去重、转大写、去掉 `.US` 后排序。

两个历史接口都会等待数据准备和下载完成后才返回。大时间范围首次请求可能较慢，
机器人应设置分钟级超时；仓库 Go SDK 的默认 HTTP 超时为 10 分钟。

### 3.3 字段约定

| 字段 | 类型 | 策略侧处理 |
| --- | --- | --- |
| `symbol` | string | 规范化后的标的代码 |
| `timestamp` | RFC3339 string | UTC K 线起始时间 |
| `open/high/low/close` | decimal string | 使用 Decimal 解析，不要先转二进制浮点再做资金结算 |
| `volume` | int64 | 兼容用整数成交量；币圈可能被截断 |
| `volume_decimal` | string | 精确成交量；存在时优先读取 |
| `turnover` | decimal string，可缺省 | 成交额；缺省代表未知，不代表零 |
| `session` | string | `regular`、`extended` 或 `continuous` |
| `source` | string | 行情提供者，如 `massive`、`mock`、`longbridge`、`binance` |
| `completed` | bool | 是否已经收盘定型 |

价格在 JSON 中是字符串，当前内部精度为小数点后 6 位。Python 推荐
`decimal.Decimal`，Go 可直接使用 SDK 的 `Bar` 类型。

响应头 `X-Cache-Source` 与响应体 `source` 相同，可能值为：

| 值 | 含义 |
| --- | --- |
| `redis` | 命中 Redis 热缓存 |
| `parquet` | 命中本地 Parquet 缓存 |
| `go-server` | 本地未命中，刚从远端下载并落盘 |

这个 `source` 说明数据从哪一层取出；每根 K 线自身的 `bar.source` 才是原始行情
提供者。缓存命中不代表数据一定是最新版本。

## 4. 机器人读取示例

### 4.1 Python

依赖：`pip install requests`

```python
from datetime import datetime, timezone
from decimal import Decimal
import requests

BASE_URL = "http://127.0.0.1:17600"

params = {
    "interval": "1d",
    "from": "2025-01-01T00:00:00Z",
    "to": "2026-01-01T00:00:00Z",
    "session": "regular",
    "adjustment": "forward_adjusted",
}

response = requests.get(
    f"{BASE_URL}/v1/bars/AAPL",
    params=params,
    timeout=(3, 600),
)
response.raise_for_status()
payload = response.json()

bars = []
for raw in payload["bars"]:
    bars.append({
        "symbol": raw["symbol"],
        "timestamp": datetime.fromisoformat(raw["timestamp"].replace("Z", "+00:00")),
        "open": Decimal(raw["open"]),
        "high": Decimal(raw["high"]),
        "low": Decimal(raw["low"]),
        "close": Decimal(raw["close"]),
        "volume": Decimal(raw.get("volume_decimal", raw["volume"])),
        "completed": bool(raw["completed"]),
    })

# 历史策略只消费已经定型的 K 线。
bars = [bar for bar in bars if bar["completed"]]
assert all(bar["timestamp"].tzinfo is not None for bar in bars)
print(payload["source"], len(bars))
```

### 4.2 Go SDK

仓库提供 `github.com/scott4game/market-bridge/pkg/client`：

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	marketclient "github.com/scott4game/market-bridge/pkg/client"
)

func main() {
	c := marketclient.NewLocalClient(marketclient.Config{
		BaseURL: "http://127.0.0.1:17600",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	dataset, err := c.EnsureDataset(ctx, marketclient.DatasetSpec{
		Symbols:    []string{"AAPL", "MSFT"},
		Interval:   "1d",
		From:       time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		To:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Session:    marketclient.RegularSession,
		Adjustment: marketclient.SplitAdjusted,
	})
	if err != nil {
		log.Fatal(err)
	}

	err = dataset.ScanBars(ctx, func(bar marketclient.Bar) error {
		if bar.Completed {
			fmt.Println(bar.Symbol, bar.Timestamp, bar.Close.String())
		}
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
}
```

## 5. 实时 WebSocket

地址：`ws://127.0.0.1:17600/v1/live/ws`

连接后第一条消息必须包含至少一个标的：

```json
{
  "action": "subscribe",
  "symbols": ["AAPL", "NVDA"],
  "events": ["bar"],
  "status": true
}
```

`status=true` 会额外返回本地代理的 `connecting`、`connected` 和 `reconnecting`
状态消息；省略时保持原有纯行情事件协议，适合现有 SDK 客户端。

K 线事件示例：

```json
{
  "type": "bar",
  "symbol": "AAPL",
  "timestamp": "2025-08-25T14:31:42Z",
  "cursor": {
    "stream_epoch": "lb-1756132302000",
    "event_type": "bar",
    "symbol": "AAPL",
    "sequence": 12345
  },
  "bar": {
    "symbol": "AAPL",
    "timestamp": "2025-08-25T14:31:00Z",
    "open": "227.100000",
    "high": "227.300000",
    "low": "227.000000",
    "close": "227.250000",
    "volume": 1800,
    "session": "regular",
    "source": "longbridge",
    "completed": false
  }
}
```

事件类型可能为 `bar`、`trade`、`depth` 或 `gap`。当前本地代理只按
`symbols` 过滤，客户端请求中的 `events` 过滤条件不会在本地生效，因此机器人必须
再次检查 `event.type`，不能假定只会收到 `bar`。

同一分钟内会多次收到 `completed=false` 的 K 线快照。策略默认只在
`completed=true` 时确认信号；若策略明确支持盘中信号，应按 `(symbol, timestamp)`
覆盖当前 bar，而不是追加成多根 bar。

当消费者过慢导致队列丢消息时会收到：

```json
{"type":"gap","symbol":"AAPL","reason":"slow_consumer"}
```

收到 `gap`、WebSocket 重连、`stream_epoch` 改变或 `sequence` 非递增时，不应继续
盲算。应重新调用历史 REST 接口补齐缺口，按 `(symbol, timestamp)` 去重后再恢复策略。

实时标的由活跃 WebSocket 连接按需订阅，并受账号标的配额约束；不需要预先配置服务端
代码池，也不要求标的出现在个人收藏中。个人收藏只用于保存当前账号常用标的，可通过
本地代理查询和修改：

```bash
curl -fsS http://127.0.0.1:17600/v1/me/watchlist

curl -fsS -X PUT http://127.0.0.1:17600/v1/me/watchlist \
  -H 'Content-Type: application/json' \
  -d '{"symbols":["AAPL","NVDA"]}'
```

## 6. 策略验证的最小流程

机器人每次验证都应保存一份运行记录，至少包括：

1. 完整请求：标的、周期、`from`、`to`、交易时段和复权模式。
2. 数据信息：HTTP 缓存来源、每根 bar 的行情来源、行数、首尾时间。
3. 数据指纹：对规范化后的原始响应计算 SHA-256。
4. 策略信息：策略版本、参数、预热长度、手续费、滑点和成交规则。
5. 结果信息：信号、成交、持仓、资金曲线和评价指标。

在运行策略前至少执行以下校验：

```text
时间戳为 UTC 且严格递增（多标的时按 symbol 分组检查）
(symbol, timestamp) 没有重复
open/high/low/close 都可解析且 high >= max(open, close)
low <= min(open, close)，volume >= 0
历史回测只使用 completed=true 的 K 线
实际首尾时间和行数符合预期；空数组不是错误，但不能直接当作“无信号”
同一次测试只使用一种 session 和 adjustment
```

避免未来函数的建议执行顺序：

```text
bar[t] 收盘 -> 计算 signal[t] -> 最早在 bar[t+1] 的可成交价格执行
```

如果使用当根收盘价成交，必须明确证明该信号在收盘前已可获得，否则回测结果会产生
前视偏差。涉及 EMA、MACD 等递推指标时，查询范围应早于正式评估区间；例如最长参数
为 90，可先加载至少数倍于 90 的 K 线作为预热，只统计预热区间之后的结果。

复权模式会直接改变历史价格：核对真实历史成交价使用 `raw`。港股/A 股的
`forward_adjusted` 来自 Longbridge；美股前复权由 Massive 拆股调整行情叠加累计现金
分红因子生成。该算法与 Futu 美股连乘前复权语义一致，但公司行动覆盖和成交筛选来源
不同，不承诺价格逐值完全相同。Massive Stocks Basic 的前复权支持最近两个自然年。
每个生效日返回的因子都是从该公司行动起到查询截至日的累计连乘值；除息日及之后不应用
该次因子。因子缺失、格式错误或上游不可用会直接返回错误，不会降级为拆股调整数据。

## 7. 与浏览器公式指标对照

本地页面 `http://127.0.0.1:17600` 的指标是在浏览器中根据当前加载的 K 线计算，
接口不会直接返回指标值或买卖信号。

页面不提供起止时间控件。它先按周期加载最多约 5000 根，并在向左拖动时分块补齐，
至少覆盖最近两个自然年；Massive Stocks Basic 和 Mock 到两年边界停止，其他可用历史
Provider 会继续请求到空结果。这个页面策略不改变本章 REST API：程序调用仍必须显式传
`from/to`。

- MA、EMA、BOLL、VOL、RSI 和 KDJ 是随 go-client 提供的共享基础模板。
- 用户复制或创建的个人公式只保存在 go-client 本地 `/data/cache.db`，不会上传到 go-server，
  也不会与团队中的其他账号共享。
- 浏览器 EMA 以当前已加载范围的第一根数据作为初始值，所以机器人要与页面逐点比对时，
  必须使用完全相同的标的、周期、已加载历史边界、时段、复权模式和参数。

公式执行器位于 [`web/formula-worker.js`](../web/formula-worker.js)，基础模板和本地持久化
位于 [`internal/localclient/indicators.go`](../internal/localclient/indicators.go)。验证个人公式时，
应把公式内容、参数、K 线查询范围和本地配置版本一并记录为策略依据，但不要把私有公式
提交到源码仓库。

若机器人实现的公式与页面不同，应该把它视为另一个策略版本，不要只比较最终收益。
建议先逐根比较指标值和信号时间，再比较成交与绩效。

## 8. 缓存与可复现性

默认 Redis TTL 为 24 小时，Parquet TTL 为 720 小时。相同的规范化查询会优先复用
缓存。当前 Schema/cache 语义版本为 v2，旧 Redis、Parquet 和服务端 dataset 不再命中，
随后由既有 TTL 清理；ClickHouse 中的 `split_adjusted` 一分钟母数据无需删除。美股 QFQ
缓存身份额外包含公司行动因子版本，go-client 将受保护因子接口响应缓存至下一个纽约
自然日。查看本地缓存：

```bash
curl -fsS http://127.0.0.1:17600/v1/cache
```

响应示例：

```json
[
  {
    "dataset_id": "...",
    "spec_hash": "...",
    "last_accessed": "2026-08-25T08:00:00Z",
    "state": "ready"
  }
]
```

查看某个本地数据集的 manifest：

```bash
curl -fsS http://127.0.0.1:17600/v1/datasets/DATASET_ID
```

manifest 包含原始查询、provider、`schema_version`、`data_version`、生成时间以及每个
Parquet 分区的 SHA-256。它是审计和复现实验时最可靠的数据版本记录。

强制某个数据集在下次请求时重新下载：

```bash
curl -fsS -X POST \
  http://127.0.0.1:17600/v1/datasets/DATASET_ID/refresh
```

清理所有已过期数据集：

```bash
curl -fsS -X POST \
  'http://127.0.0.1:17600/v1/cache/prune?expired=true'
```

不带 `expired=true` 会删除全部当前未被读取的数据集，机器人不应在正常策略运行中调用。

## 9. 错误处理

错误响应统一包含 `error` 字段：

```json
{"error":"错误说明"}
```

常见状态码：

| 状态码 | 含义 | 机器人处理 |
| --- | --- | --- |
| `400` | 时间格式或 JSON 请求体错误 | 修正请求，不重试原请求 |
| `403` | 浏览器跨域被拒绝，或关注列表无权限 | 修正来源或权限 |
| `404` | 本地数据集不存在 | 重新执行历史查询 |
| `409` | 数据集正在使用，不能刷新 | 稍后重试 |
| `500` | 本地缓存管理错误 | 记录错误并告警 |
| `502` | 参数校验、远端服务、下载或缓存读取失败 | 指数退避重试；持续失败则告警 |

`429`、`503` 等远端错误在历史下载流程中通常会被本地接口包装成 `502`。不要无限快速
重试；建议使用带抖动的指数退避，并为每次策略运行设置总超时。

浏览器请求只允许 `Origin` 主机为 `localhost` 或 `127.0.0.1`。普通服务端机器人不发送
`Origin` 即可；从其他域名网页直接调用会得到 `403`，且接口没有开放通用 CORS。

## 10. 其他本地接口

| 方法与路径 | 用途 |
| --- | --- |
| `GET /healthz` | 进程存活检查 |
| `GET /readyz` | HTTP 就绪检查 |
| `GET /v1/cache` | 列出本地数据集 |
| `GET /v1/datasets/{id}` | 读取本地 manifest |
| `POST /v1/datasets/{id}/refresh` | 删除指定本地数据集 |
| `POST /v1/cache/prune?expired=true` | 清理过期缓存 |
| `GET /v1/providers/status` | 查看行情提供者状态 |
| `GET /v1/providers/massive/usage` | 查看 Massive 调用量 |
| `GET /v1/me` | 查看当前远端账号 |
| `GET /v1/me/usage` | 查看配额和用量 |
| `GET /v1/me/watchlist` | 查看个人实时关注列表 |
| `PUT /v1/me/watchlist` | 修改个人实时关注列表 |
| `GET /v1/live/ws` | 实时行情 WebSocket |
