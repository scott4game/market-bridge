# go-client 本地 AI Agent API

本文是提供给本机 Grok、Claude Code、Codex 及自动化脚本的接口契约。Agent 应只访问
`go-client`，不要直接访问远端 `go-server` 或行情供应商。

```text
REST Base URL: http://127.0.0.1:17600
WebSocket URL: ws://127.0.0.1:17600/v1/live/ws
Content-Type: application/json
时间格式: RFC3339，统一使用 UTC
```

本地接口默认不需要 `Authorization`。远端凭据由 `go-client` 持有并自动转发，不要把
`GO_CLIENT_SERVER_TOKEN` 写进 Agent 的提示词、代码或请求头。服务应继续只监听
`127.0.0.1`，不要直接暴露到公网。

## 1. Agent 调用规则

1. 开始任务前调用 `/healthz`、`/v1/providers/status` 和 `/v1/storage/status`。
2. 不确定代码时先查询 `/v1/market-history/universe`，不要猜测股票代码。
3. 历史查询始终显式传 `from`、`to`、`interval`、`session` 和 `adjustment`。
4. 时间范围采用 `[from, to)`：包含 `from`，不包含 `to`。
5. `open/high/low/close/turnover` 是十进制字符串。资金或指标计算应使用 Decimal，
   不要先转为二进制浮点数。
6. WebSocket 收到 `gap`、发生重连，或本地超过预期时间没有行情时，重新拉取最近一段
   REST K 线，并按 `(symbol, timestamp, interval)` 去重。
7. `completed=false` 是仍会变化的快照；`completed=true` 才是已定型 K 线。

## 2. 市场与代码格式

| 市场 | 代码示例 | 默认 session | 默认 adjustment |
| --- | --- | --- | --- |
| 美股 | `SNDK`、`AAPL`（也接受 `AAPL.US`） | `regular` | 页面 `forward_adjusted`；API `auto` 为 `split_adjusted` |
| 港股 | `700.HK` | `regular` | `forward_adjusted` |
| A 股上海 | `600519.SH` | `regular` | `forward_adjusted` |
| A 股深圳 | `000001.SZ` | `regular` | `forward_adjusted` |
| Binance Spot | `BTCUSDT.BINANCE` | `continuous` | `raw` |

代码不区分大小写，响应中返回规范化后的大写代码。一个 dataset 内不能混合证券与币圈
代码。

## 3. 接口速查

| 方法 | 路径 | 用途 |
| --- | --- | --- |
| GET | `/healthz` | 检查 go-client HTTP 进程 |
| GET | `/readyz` | 检查 HTTP 是否可接收请求 |
| GET | `/v1/providers/status` | 查看历史/实时行情供应商状态 |
| GET | `/v1/storage/status` | 查看当前 ClickHouse、本地缓存模式 |
| GET | `/v1/market-history/universe` | 获取可搜索的全部标的代码 |
| GET | `/v1/market-history/security-profiles` | 获取活跃美股普通股的通用公司资料、SIC和市值 |
| GET | `/v1/market-history/adjustments/{symbol}` | 获取美股前复权累计因子及版本 |
| GET | `/v1/bars/{symbol}` | 查询单标的历史 K 线 |
| GET | `/v1/live/trades/{symbol}?limit=100` | 查询 Longbridge 最近逐笔成交，最多 1000 笔 |
| GET | `/v1/news?symbols=AAPL&limit=50` | 查询本地新闻镜像，默认返回最新 50 条 |
| GET | `/v1/news/stream?symbols=AAPL` | 使用 SSE 持续监听新闻 |
| WS | `/v1/news/ws` | 使用 WebSocket 订阅全局或指定股票新闻 |
| POST | `/v1/datasets/ensure` | 查询多个同市场标的的历史 K 线 |
| GET | `/v1/me/usage` | 查看当前账号用量和配额 |
| GET/PUT | `/v1/me/watchlist` | 读取/保存个人收藏；不触发实时订阅 |
| WS | `/v1/live/ws` | 按连接、按需订阅实时行情 |

`/readyz` 只表示本地 HTTP 已就绪，不保证上游行情一定可用，因此不能替代 provider
状态检查。

## 4. 查询标的目录

```bash
curl -fsS 'http://127.0.0.1:17600/v1/market-history/universe'
```

响应：

```json
{
  "symbols": ["000001.SZ", "600519.SH", "700.HK", "AAPL", "SNDK"],
  "securities": [
    {"symbol": "600519.SH", "name_cn": "贵州茅台", "name_en": "Kweichow Moutai"},
    {"symbol": "AAPL", "name_cn": "苹果", "name_en": "Apple Inc."}
  ],
  "updated_at": "2026-08-25T08:00:00Z",
  "data_version": "v1"
}
```

`symbols` 保留用于兼容现有脚本；页面或新客户端应优先读取 `securities`。名称来自已配置的
行情目录，优先展示 `name_cn`，缺失时回退到 `name_en`，两者都缺失时仍显示代码。

按市场筛选时使用后缀：`.HK` 为港股，`.SH/.SZ` 为 A 股，`.BINANCE` 为币圈；没有
这些后缀的代码为美股。目录可能包含数千个代码，Agent 应在本地做前缀或子串筛选，
不要为每个候选代码发一次网络请求。

需要行业分类或市值时读取：

```bash
curl -fsS 'http://127.0.0.1:17600/v1/market-history/security-profiles'
```

go-server 通过 Massive Reference Data 刷新活跃美股普通股资料，并持久缓存在
`security-profiles.db`。响应包含 `source`、`updated_at`、`complete`、`profiles` 和可选的
逐代码 `errors`；资料字段包括 `symbol/name/cik/type/active/locale/market/market_cap`、
`sic_code/sic_description/primary_exchange/provider/fetched_at/stale`。默认24小时刷新，刷新
失败时最多保留30天旧资料并标记 `stale=true`。该接口只提供通用资料，不直接判定行业龙头。

Python 搜索示例：

```python
import json
import urllib.request

BASE = "http://127.0.0.1:17600"
with urllib.request.urlopen(f"{BASE}/v1/market-history/universe", timeout=30) as r:
    symbols = json.load(r)["symbols"]

query = "SNDK"
matches = [s for s in symbols if query.upper() in s][:20]
print(matches)
```

## 5. 单标的历史 K 线

```http
GET /v1/bars/{symbol}?from=...&to=...&interval=...&session=...&adjustment=...
```

查询 SNDK 最近一段 1 小时 K 线：

```bash
curl --fail --get 'http://127.0.0.1:17600/v1/bars/SNDK' \
  --data-urlencode 'from=2026-08-01T00:00:00Z' \
  --data-urlencode 'to=2026-08-26T00:00:00Z' \
  --data-urlencode 'interval=1h' \
  --data-urlencode 'session=regular' \
  --data-urlencode 'adjustment=forward_adjusted'
```

参数：

| 参数 | 必填 | 说明 |
| --- | --- | --- |
| `from` | 是 | RFC3339 UTC 起点，闭区间 |
| `to` | 是 | RFC3339 UTC 终点，开区间，必须晚于 `from` |
| `interval` | 建议显式传 | `1m/3m/5m/10m/15m/30m/1h/2h/3h/4h/1d/1w/1mo/1y`；缺省为 `1m` |
| `session` | 建议显式传 | `regular`、`extended` 或 `continuous` |
| `adjustment` | 建议显式传 | `auto`、`raw`、`split_adjusted` 或 `forward_adjusted` |

响应：

```json
{
  "source": "clickhouse",
  "bars": [
    {
      "symbol": "SNDK",
      "timestamp": "2026-08-24T14:00:00Z",
      "open": "45.120000",
      "high": "45.900000",
      "low": "44.980000",
      "close": "45.700000",
      "volume": 1234567,
      "turnover": "56123456.000000",
      "session": "regular",
      "source": "massive",
      "completed": true
    }
  ]
}
```

响应头 `X-Cache-Source` 与响应体 `source` 表示本次读取层，可能是 `redis`、`parquet`、
`clickhouse`、`go-server` 或混合来源；每根 bar 的 `source` 才表示原始行情供应商。
`turnover` 未知时会缺省，不代表零。币圈应优先读取可能存在的精确字符串字段
`volume_decimal`。

### SNDK 1 小时线注意事项

美股 `regular` 分钟线会过滤到美东 `09:30–16:00`；`1h/2h/3h/4h` 使用 Futu 风格的
`09:30` 开盘锚点，最后不足一个完整周期的 K 线仍会返回。`forward_adjusted` 使用
Massive 的拆股调整行情和累计现金分红因子实现美股连乘前复权。时间边界应与 Futu
一致，但两家供应商对有效成交的筛选可能不同，因此 OHLC 和成交量不保证逐值相同。

受保护的 `GET /v1/market-history/adjustments/{symbol}` 返回 `symbol`、`mode`、`as_of`、
`version` 和按 `effective_date` 升序排列的 Decimal `factors`。每条 factor 已包含该日
及之后公司行动的累计连乘值，只应用于生效日前的 OHLC；因子错误或上游不可用时请求
会失败，不会静默退回 `split_adjusted`。go-client 缓存曲线至下一个纽约自然日，QFQ
数据缓存身份同时包含 factor version。

## 6. 多标的历史 K 线

同一市场、相同周期和时间范围的多标的查询使用：

```bash
curl -fsS -X POST 'http://127.0.0.1:17600/v1/datasets/ensure' \
  -H 'Content-Type: application/json' \
  -d '{
    "symbols": ["SNDK", "AAPL", "NVDA"],
    "interval": "1h",
    "from": "2026-01-01T00:00:00Z",
    "to": "2026-08-26T00:00:00Z",
    "session": "regular",
    "adjustment": "forward_adjusted"
  }'
```

响应为：

```json
{"source":"go-server","count":3,"bars":[]}
```

实际 `bars` 字段与单标的格式相同，按 `timestamp`、`symbol` 排序。首次查询两年范围
可能触发上游下载，Agent 应使用分钟级 HTTP 超时。默认 ClickHouse 保留最近 1825 天。
浏览器页面会自动分块加载至少两年并在左拖时继续追溯，但这是 UI 策略；Agent API
不会猜时间范围，仍必须显式传 `from/to`。

## 7. 新闻查询与监听

新闻由 go-server 的 FMP Provider 采集，go-client 持续镜像到本机。Agent 应连接本地
`127.0.0.1:17600`，不需要持有 FMP API Key。查询当前股票最近新闻：

```bash
curl -fsS 'http://127.0.0.1:17600/v1/news?symbols=AAPL&limit=50'
```

`kinds` 可取 `stock_news`、`press_release`；省略 `symbols` 表示全部股票。响应中的
`sequence` 是单调游标，断线后通过 `after_sequence` 补齐：

```bash
curl -fsS 'http://127.0.0.1:17600/v1/news?after_sequence=123&limit=500'
```

Grok CLI、Claude Code、Codex 等工具最方便的持续监听方式是 SSE：

```bash
curl -N 'http://127.0.0.1:17600/v1/news/stream?symbols=AAPL,NVDA&kinds=stock_news,press_release'
```

SSE 使用 `id` 作为新闻序号，并接受标准 `Last-Event-ID` 请求头。也可以使用 WebSocket，
连接后发送一次订阅消息：

```json
{
  "symbols": ["AAPL", "NVDA"],
  "kinds": ["stock_news", "press_release"],
  "after_sequence": 123,
  "status": true
}
```

地址为 `ws://127.0.0.1:17600/v1/news/ws`。`symbols` 为空表示接收全部新闻。事件格式：

```json
{
  "type": "news",
  "action": "created",
  "sequence": 124,
  "article": {
    "id": "...",
    "kind": "stock_news",
    "symbols": ["AAPL"],
    "title": "...",
    "summary": "...",
    "url": "https://...",
    "publisher": "...",
    "published_at": "2026-08-27T10:20:30Z",
    "received_at": "2026-08-27T10:21:02Z",
    "provider": "fmp"
  }
}
```

收到 `gap` 时记录其 `sequence`，然后调用 REST 接口补齐。新闻内容只保存标题、摘要和
原文链接；Agent 不应假定摘要等于完整正文。

## 8. 实时 WebSocket

如需在订阅建立时立即初始化逐笔列表，可先请求最近成交；响应中的 `timestamp` 为 Unix
秒，随后用 WebSocket 的 `trade` 事件续接：

```bash
curl -fsS 'http://127.0.0.1:17600/v1/live/trades/AAPL?limit=100'
```

连接后立即发送一次 JSON 订阅消息：

```json
{
  "action": "subscribe",
  "symbols": ["SNDK", "AAPL"],
  "events": ["bar", "quote", "trade", "depth"],
  "status": true
}
```

`symbols` 是必填项。`status:true` 是 go-client 的本地扩展，用于接收连接状态帧；纯行情
SDK 如果不需要状态帧可以省略。当前本地代理按 `symbols` 过滤，Agent 仍应检查每条消息
的 `type`，不要假设 `events` 已在本地完成过滤。

Python 示例需要先安装 `websockets`：

```python
import asyncio
import json
import websockets

async def main():
    uri = "ws://127.0.0.1:17600/v1/live/ws"
    async with websockets.connect(uri, open_timeout=10) as ws:
        await ws.send(json.dumps({
            "action": "subscribe",
            "symbols": ["SNDK"],
            "events": ["bar", "quote", "trade", "depth"],
            "status": True,
        }))
        async for raw in ws:
            event = json.loads(raw)
            print(event)
            if event.get("type") == "gap":
                # 重新调用 REST 拉取最近 K 线，再按时间戳去重。
                pass

asyncio.run(main())
```

状态帧示例：

```json
{
  "type": "status",
  "state": "connected",
  "symbols": ["AAPL", "SNDK"],
  "detail": "上游 WebSocket 已连接",
  "timestamp": "2026-08-25T08:00:00Z"
}
```

`state` 可能为 `connecting`、`connected` 或 `reconnecting`。收到 `connected` 只代表
传输和订阅成功；应以最近一条实际行情事件时间判断数据是否仍在流动，并考虑休市状态。

Bar 事件示例：

```json
{
  "type": "bar",
  "symbol": "SNDK",
  "timestamp": "2026-08-25T14:31:00Z",
  "cursor": {
    "stream_epoch": "example-epoch",
    "event_type": "bar",
    "symbol": "SNDK",
    "sequence": 123
  },
  "bar": {
    "symbol": "SNDK",
    "timestamp": "2026-08-25T14:31:00Z",
    "open": "45.120000",
    "high": "45.180000",
    "low": "45.100000",
    "close": "45.160000",
    "volume": 3210,
    "session": "regular",
    "source": "longbridge",
    "completed": false
  }
}
```

Longbridge 股票还会返回服务端已计算的实时涨跌：

```json
{"type":"quote","symbol":"SNDK","quote":{"last_done":"46.510000","prev_close":"45.160000","change":"1.350000","change_percent":"2.989371","trade_session":"regular","source":"longbridge"}}
```

行情事件类型包括 `bar`、`quote`、`trade`、`depth` 和 `gap`。`gap` 示例：

```json
{"type":"gap","symbol":"SNDK","reason":"slow_consumer"}
```

本地每个 WebSocket 连接最多订阅 200 个不同代码，符合默认账号实时配额。多个本地连接
的代码会自动求并集并复用一个上游连接；最后一个连接关闭后，上游订阅自动取消。服务端
没有固定“实时盯盘代码配置”。供应商总并集另有 500 个代码的安全上限。

实时推送目前以供应商的 1 分钟 bar 为准；`1h/2h/3h/4h` 等高周期应通过历史 K 线
接口查询或由 Agent 从 1 分钟数据按明确的交易时段边界聚合。

## 9. 个人收藏与实时订阅的区别

读取收藏：

```bash
curl -fsS 'http://127.0.0.1:17600/v1/me/watchlist'
```

保存收藏：

```bash
curl -fsS -X PUT 'http://127.0.0.1:17600/v1/me/watchlist' \
  -H 'Content-Type: application/json' \
  -d '{"symbols":["SNDK","AAPL","700.HK"]}'
```

响应包含 `subscription_mode:"on_demand"`。收藏只用于保存用户偏好，不会让服务器常驻
订阅这些代码。真正的实时盯盘集合始终来自当前 WebSocket 连接中的 `symbols`。

## 10. 错误和恢复

| 状态/现象 | 含义 | Agent 行为 |
| --- | --- | --- |
| HTTP 400 | 参数、代码或时间格式错误 | 修正请求，不要原样重试 |
| HTTP 403 | 浏览器跨域 Origin 被拒绝 | 从本机同源调用或移除错误 Origin |
| HTTP 404 | dataset/cache 项不存在 | 重新 ensure 或查询 bars |
| HTTP 429 | 账号或供应商配额限制 | 读取 `/v1/me/usage`，指数退避 |
| HTTP 502/503 | go-server 或行情供应商不可用 | 查看 provider 状态，带抖动重试 |
| WS close 1008 | 缺少代码、代码无效或超过配额 | 修正订阅后重连 |
| `type=gap` | 慢消费者导致消息缺口 | REST 补最近数据并去重 |
| 长时间无行情 | 可能休市、断流或未配置真实 Provider | 对照 status、交易日历和最近事件时间 |

所有网络重试都应使用有上限的指数退避。历史 GET 可以安全重试；写收藏前避免并发覆盖。

## 11. 可交给 Agent 的最小提示词

```text
只通过 http://127.0.0.1:17600 访问 go-client，不直接连接 go-server 或供应商。
先检查 health、providers/status、storage/status；不确定代码时查询 market-history/universe。
历史 K 线显式使用 RFC3339 UTC 的 [from,to) 范围，并明确 interval/session/adjustment。
把价格字符串解析为 Decimal。实时连接使用 /v1/live/ws，订阅消息设置 status=true；
遇到 reconnecting、gap 或序列缺口时，用 REST 补最近 K 线并按 symbol+timestamp+interval 去重。
收藏列表不是实时订阅列表，不要通过修改收藏来启动盯盘。
```

更完整的缓存、ClickHouse、dataset 生命周期和策略验证说明见
[go-client 本地数据接口与策略验证指南](go-client-data-api.md)。
