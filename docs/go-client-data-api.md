# go-client 本地数据接口与策略验证指南

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

## 2. 推荐的历史 K 线接口

### 2.1 单标的：GET `/v1/bars/{symbol}`

这是单只股票策略最简单的读取方式。

```bash
curl --get 'http://127.0.0.1:17600/v1/bars/AAPL' \
  --data-urlencode 'interval=1d' \
  --data-urlencode 'from=2025-01-01T00:00:00Z' \
  --data-urlencode 'to=2026-01-01T00:00:00Z' \
  --data-urlencode 'session=regular' \
  --data-urlencode 'adjustment=split_adjusted'
```

查询参数：

| 参数 | 必填 | 取值 | 说明 |
| --- | --- | --- | --- |
| `from` | 是 | RFC3339 时间 | 起点，按闭区间处理 |
| `to` | 是 | RFC3339 时间 | 终点，按开区间处理 |
| `interval` | 否 | 见下表 | 默认 `1m` |
| `session` | 否 | `regular` / `extended` | 默认 `regular` |
| `adjustment` | 否 | `raw` / `split_adjusted` | 默认 `split_adjusted` |

支持的周期：

```text
1m  3m  5m  10m  15m  30m
1h  2h  3h  4h
1d  1w  1mo  1y
```

标的代码不区分大小写，末尾 `.US` 会被去掉。例如 `aapl`、`AAPL` 和
`AAPL.US` 都会规范化为 `AAPL`。

`session` 当前会参与数据集标识并写入 bar，但 Massive Provider 没有把它转换成
交易时段过滤条件。若策略严格区分盘前、盘中和盘后，机器人仍需按交易所日历和 UTC
时间自行过滤，不能只依赖返回的 `session` 字段。

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

### 2.2 多标的：POST `/v1/datasets/ensure`

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
    "adjustment": "split_adjusted"
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

### 2.3 字段约定

| 字段 | 类型 | 策略侧处理 |
| --- | --- | --- |
| `symbol` | string | 规范化后的标的代码 |
| `timestamp` | RFC3339 string | UTC K 线起始时间 |
| `open/high/low/close` | decimal string | 使用 Decimal 解析，不要先转二进制浮点再做资金结算 |
| `volume` | int64 | 成交量 |
| `turnover` | decimal string，可缺省 | 成交额；缺省代表未知，不代表零 |
| `session` | string | `regular` 或 `extended` |
| `source` | string | 行情提供者，如 `massive`、`mock`、`longbridge` |
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

## 3. 机器人读取示例

### 3.1 Python

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
    "adjustment": "split_adjusted",
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
        "volume": int(raw["volume"]),
        "completed": bool(raw["completed"]),
    })

# 历史策略只消费已经定型的 K 线。
bars = [bar for bar in bars if bar["completed"]]
assert all(bar["timestamp"].tzinfo is not None for bar in bars)
print(payload["source"], len(bars))
```

### 3.2 Go SDK

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

## 4. 实时 WebSocket

地址：`ws://127.0.0.1:17600/v1/live/ws`

连接后第一条消息必须包含至少一个标的：

```json
{
  "action": "subscribe",
  "symbols": ["AAPL", "NVDA"],
  "events": ["bar"]
}
```

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

实时标的必须在服务端全局关注列表中。个人关注列表用于保存当前账号常用的标的，
并会受全局列表和账号标的配额约束；当前订阅协议不要求标的预先出现在个人列表里。
可通过本地代理查询全局允许范围并修改个人列表：

```bash
curl -fsS http://127.0.0.1:17600/v1/me/watchlist

curl -fsS -X PUT http://127.0.0.1:17600/v1/me/watchlist \
  -H 'Content-Type: application/json' \
  -d '{"symbols":["AAPL","NVDA"]}'
```

## 5. 策略验证的最小流程

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

复权模式会直接改变历史价格：研究收益连续性通常使用 `split_adjusted`，核对真实历史
成交价时使用 `raw`。同一组训练、验证和实盘对照不得混用两种模式。当前 Massive 的
`split_adjusted` 表示拆股调整，不等同于股息复权。

## 6. 与内置图表策略对照

本地页面 `http://127.0.0.1:17600` 的指标是在浏览器中根据当前加载的 K 线计算，
接口不会直接返回指标值或买卖信号。

- NX 默认参数为 `24/23/89/90`：蓝线上下轨为 `EMA(high,24)` / `EMA(low,23)`，
  黄线上下轨为 `EMA(high,89)` / `EMA(low,90)`。
- MX 使用 MACD 默认参数 `12/26/9`，页面还在其上计算背离 B/S 信号。
- 浏览器 EMA 以加载范围的第一根数据作为初始值，所以机器人要与页面逐点比对时，
  必须使用完全相同的标的、周期、起止时间、时段、复权模式和参数。

NX 和 MX 的精确实现位于
[`internal/localclient/ui/app.js`](../internal/localclient/ui/app.js)。需要验证内置 B/S
信号时，应把该实现及其版本一并记录为策略依据。

若机器人实现的公式与页面不同，应该把它视为另一个策略版本，不要只比较最终收益。
建议先逐根比较指标值和信号时间，再比较成交与绩效。

## 7. 缓存与可复现性

默认 Redis TTL 为 24 小时，Parquet TTL 为 720 小时。相同的规范化查询会优先复用
缓存。查看本地缓存：

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

## 8. 错误处理

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

## 9. 其他本地接口

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
