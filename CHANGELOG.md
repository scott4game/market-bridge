# 更新日志

## 未发布

### 修复：美股复权、证券分类与历史请求错误

- Massive分红`historical_adjustment_factor`按上游累计语义直接使用，不再重复连乘；因子采用高精度十进制，OHLC仍输出六位小数。同日相同记录去重，冲突或非正因子报错。
- 前复权缓存语义升级为`us-qfq-v4`/`massive-qfq-v4`。新客户端拒绝已知错误的v1–v3因子曲线；需先更新服务端再更新客户端。旧前复权派生数据、指标和回测结果必须重新生成，原始行情保留。
- 证券档案增加类型与描述冲突检查，隔离误标为CS的优先股、债券、ADR、ETF、权证及组合单位；旧档案缓存同样重新校验。`excluded`记录原档案和原因，明确排除不当作拉取失败。
- 历史接口保留HTTP状态及脱敏错误摘要，增加`error_details`/`warning_details`，包含错误代码、上游状态、是否可重试及等待秒数；部分数据显式`complete=false`。不改变空分钟的成交含义，也不补造K线。
- 失败冷却按请求时间范围、来源版本和复权版本隔离；429遵循Retry-After（缺省60秒），网络/5xx短冷却30秒，其他失败使用原配置；取消请求不污染缓存。


### 新增：长桥美股期权行情

- 支持查询期权到期日、指定到期日的期权链、期权报价和 Greeks（Delta、Gamma、Theta、Vega、Rho）。期权链支持 Call/Put、行权价筛选和分页；报价包含最新价、OHLC、成交量、IV、持仓量及可用合约属性。
- 新增服务端 HTTP 接口及客户端同路径代理：`/v1/options/expirations`、`/v1/options/chain`、`/v1/options/quotes`、`/v1/options/greeks`。
- 新增四个只读 MCP 工具：`get_option_expirations`、`get_option_chain`、`get_option_quotes`、`get_option_greeks`；服务端接口使用 `live:read` 权限。
- 新增独立配置 `GO_SERVER_OPTIONS_LIVE_PROVIDER=longbridge`（默认 `disabled`），复用现有 `LONGBRIDGE_APP_KEY`、`LONGBRIDGE_APP_SECRET` 和 `LONGBRIDGE_ACCESS_TOKEN`。可独立于股票行情、Massive 启用。
- 增加有容量上限的内存缓存和相同请求的并发合并：到期日、期权链缓存 5 分钟，报价、Greeks 缓存 2 秒。响应标注数据源、抓取时间、缓存命中及缺失合约；缺失数值保留为 `null`。
- 增加权限不足、限流、超时等错误分类和脱敏处理；`/v1/providers/status` 新增 `options_live` 配置及查询状态。
- 新增只读验证命令：`go run ./cmd/options-probe -env-file .env.server -underlying AAPL.US`，动态选择未到期的 Call/Put 合约并检查报价、Greeks 字段完整性。

### 兼容性与部署

- 原有 `GO_SERVER_OPTIONS_PROVIDER=massive` 继续负责历史期权合约和日线，与长桥实时行情配置并存；不改变现有 `get_option_contracts`、`get_option_bars` 和 `get_bars` 工具。
- 新接口使用长桥原生期权代码；原有 Massive 接口继续使用 `O:` 合约代码，不自动转换或切换数据源。
- 使用新增 MCP 工具或客户端代理时，服务器与客户端都需更新。服务器 Compose 需传入新配置；修改环境配置后重新创建容器，单纯重启旧容器不会加载新环境变量。无数据迁移要求。
- 期权报价需要单独开通「OPRA 美股期权行情（OpenAPI）」；App/Web 的 OPRA 权限不等于 OpenAPI 权限。Greeks 是否有有效数据需按账户实测。
- 本次不包含下单、实时推送、前端页面或长桥期权历史 K 线回补；部分近期已到期合约可查询，不代表具有完整历史覆盖。

### 验证与已知限制

- 全量 `go test ./...` 及新增模块的定向 race 检查通过。
- MCP 实测 AAPL、AVGO：2025-01-17 到期、行权价 200–260 美元的看涨历史合约各返回 25 个；两只股票的 200 美元看涨合约在 2025-01-02 至 2025-01-17 各返回 11 根日线，股票日线也各返回 11 根。
- 原有 Massive 合约查询固定使用 `expired=true`；本次实测传入到期前的 `as_of` 日期返回空列表，而省略 `as_of` 或指定到期后日期可返回合约。历史时点的未到期合约查询仍需后续完善，本次未修复。
- 修正原有 Massive 历史范围测试在纽约午夜附近的窗口数量断言，并增加请求区间连续性检查。
