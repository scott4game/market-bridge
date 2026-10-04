# 更新日志

## 未发布

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
