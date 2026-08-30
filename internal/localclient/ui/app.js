const $ = id => document.getElementById(id)
const UNIVERSE_STORAGE_KEY = 'market-bridge:market-universe-v3'
const Y_AXIS_ZOOM_STORAGE_KEY = 'market-bridge:y-axis-zoom-v1'
const BLUE = '#4f8cff'
const YELLOW = '#f2c94c'
const CYAN = '#0caee6'
const PURPLE = '#e970dc'
const RED = '#ff4d5a'
const GREEN = '#2ac99a'
let socket = null
let liveGeneration = 0
const liveFeed = { symbol: '', trades: [], bufferedTrades: [], initializing: false, recentCount: 0, liveCount: 0, depth: null, quote: null, recoverOnConnect: false }
let universeSecurities = []
const wsMonitor = { state: 'disabled', symbol: '', interval: '', count: 0, connectedAt: 0, lastMessageAt: 0, lastType: '', detail: '实时推送目前仅支持 1m 周期' }
let activeQuery = null
let lastBars = []
let providerStatus = null
let queryGeneration = 0
let localIndicators = []
let selectedIndicator = null
let activeFormulaCharts = []
const registeredFormulaNames = new Set()
let workerSequence = 0
let formulaWorker = null
const workerRequests = new Map()

function storedYAxisZoomEnabled() {
  try {
    return localStorage.getItem(Y_AXIS_ZOOM_STORAGE_KEY) === 'true'
  } catch (_) {
    return false
  }
}

let yAxisZoomEnabled = storedYAxisZoomEnabled()

function setStatus(text, state = '') {
  $('status').textContent = text
  $('status').className = state
}

function formatDuration(timestamp) {
  if (!timestamp) return '—'
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000))
  if (seconds < 2) return '不足 2 秒'
  if (seconds < 60) return `${seconds} 秒`
  return `${Math.floor(seconds / 60)} 分钟`
}

function formatAge(timestamp) {
  return timestamp ? `${formatDuration(timestamp)}前` : '—'
}

function renderWSStatus() {
  const labels = {
    disabled: ['未启用', ''],
    connecting: ['连接中', 'warn'],
    reconnecting: ['正在重连', 'warn'],
    connected: [wsMonitor.count ? '正在推送' : '已连接 · 等待数据', wsMonitor.count ? 'ok' : 'warn'],
    stale: ['已连接 · 暂无新数据', 'warn'],
    gap: ['推送有缺口', 'bad'],
    error: ['连接异常', 'bad'],
    closed: ['已断开', 'bad']
  }
  let state = wsMonitor.state
  const latestActivity = wsMonitor.lastMessageAt || wsMonitor.connectedAt
  if (state === 'connected' && latestActivity && Date.now() - latestActivity >= 15000) state = 'stale'
  const [label, className] = labels[state] || labels.error
  $('ws-status').textContent = label
  $('ws-status').className = className
  if (state === 'connected' || state === 'stale') {
    $('ws-detail').textContent = wsMonitor.count
      ? `${wsMonitor.symbol} · 累计 ${wsMonitor.count} 条 · 最后 ${wsMonitor.lastType} ${formatAge(wsMonitor.lastMessageAt)}`
      : `${wsMonitor.symbol} · 0 条 · 已等待 ${formatDuration(wsMonitor.connectedAt)}；可能未开市或上游无行情`
    return
  }
  $('ws-detail').textContent = wsMonitor.detail
}

function setWSState(state, values = {}) {
  Object.assign(wsMonitor, values, { state })
  renderWSStatus()
}

function closeSocket() {
  if (!socket) return
  socket.onopen = null
  socket.onmessage = null
  socket.onerror = null
  socket.onclose = null
  socket.close()
  socket = null
}

function selectedMarketSecurities() {
  const market = $('market').value
  return universeSecurities.filter(security => {
    const symbol = security.symbol
    if (market === 'hk') return symbol.endsWith('.HK')
    if (market === 'cn') return symbol.endsWith('.SH') || symbol.endsWith('.SZ')
    return !/\.(HK|SH|SZ|BINANCE)$/.test(symbol)
  })
}

function marketHistoryEnabled(market) {
  if (market === 'us') return true
  if (market === 'hk') return providerStatus?.hk?.history_enabled === true
  if (market === 'cn') return providerStatus?.ashare?.history_enabled === true
  return false
}

function symbolMarket(symbol) {
  const upper = symbol.toUpperCase()
  if (upper.endsWith('.HK')) return 'hk'
  if (upper.endsWith('.SH') || upper.endsWith('.SZ')) return 'cn'
  return 'us'
}

function updateMarketAvailability() {
  const longbridgeHistory = marketHistoryEnabled('hk')
  for (const market of ['hk', 'cn']) {
    const option = $('market').querySelector(`option[value="${market}"]`)
    if (option) option.disabled = !longbridgeHistory
  }
  $('market-state').textContent = longbridgeHistory
    ? '港股/A股历史行情已启用'
    : '港股/A股需服务端启用 Longbridge 历史行情'
}

function renderSymbolOptions() {
  const securities = selectedMarketSecurities()
  const fragment = document.createDocumentFragment()
  for (const security of securities) {
    const option = document.createElement('option')
    const name = security.name_cn || security.name_en
    option.value = security.symbol
    if (name) option.label = `${name}（${security.symbol}）`
    fragment.appendChild(option)
  }
  $('symbol-options').replaceChildren(fragment)
  const marketName = { us: '美股', hk: '港股', cn: 'A股' }[$('market').value]
  $('symbol-options-state').textContent = `可按代码或名称搜索 ${securities.length.toLocaleString()} 只${marketName}`
}

function normalizeSecurity(value) {
  if (typeof value === 'string') return { symbol: value.toUpperCase(), name_cn: '', name_en: '' }
  return {
    symbol: String(value?.symbol || '').toUpperCase(),
    name_cn: String(value?.name_cn || '').trim(),
    name_en: String(value?.name_en || '').trim()
  }
}

function securityForInput(value) {
  const query = value.trim().toLocaleLowerCase()
  return selectedMarketSecurities().find(security =>
    security.symbol.toLocaleLowerCase() === query ||
    security.name_cn.toLocaleLowerCase() === query ||
    security.name_en.toLocaleLowerCase() === query
  )
}

function renderSecurityIdentity(symbol) {
  const security = universeSecurities.find(item => item.symbol === symbol)
  const title = security?.name_cn || security?.name_en || symbol
  const subtitle = security?.name_cn && security?.name_en ? security.name_en : '证券行情'
  $('security-title').textContent = title
  $('security-subtitle').textContent = subtitle
  $('security-code').textContent = symbol || '—'
}

function normalizeSelectedMarketSymbol(value) {
  const matched = securityForInput(value)
  if (matched) return matched.symbol
  const symbol = value.trim().toUpperCase()
  if (!symbol || symbol.includes('.')) return symbol
  if ($('market').value === 'hk') return `${symbol}.HK`
  if ($('market').value === 'cn') return `${symbol}.${/^[569]/.test(symbol) ? 'SH' : 'SZ'}`
  return symbol
}

async function loadSymbolOptions() {
  try {
    const cached = JSON.parse(localStorage.getItem(UNIVERSE_STORAGE_KEY) || 'null')
    if (cached && Array.isArray(cached.securities) && Date.now() - cached.savedAt < 24 * 3600 * 1000) {
      universeSecurities = cached.securities.map(normalizeSecurity).filter(value => value.symbol && !value.symbol.endsWith('.BINANCE'))
      renderSymbolOptions()
      return
    }
  } catch (_) {}
  try {
    const data = await getJSON('/v1/market-history/universe')
    const values = Array.isArray(data.securities) && data.securities.length ? data.securities : (data.symbols || [])
    universeSecurities = values.map(normalizeSecurity).filter(value => value.symbol && !value.symbol.endsWith('.BINANCE'))
    renderSymbolOptions()
    const current = normalizeSelectedMarketSymbol($('symbol').value)
    if (current) renderSecurityIdentity(current)
    localStorage.setItem(UNIVERSE_STORAGE_KEY, JSON.stringify({ savedAt: Date.now(), securities: universeSecurities }))
  } catch (error) {
    $('symbol-options-state').textContent = `代码列表加载失败，仍可直接输入：${error.message}`
  }
}

async function getJSON(path, options) {
  const response = await fetch(path, options)
  const data = response.status === 204 ? null : await response.json()
  if (!response.ok) throw new Error(data.error || response.statusText)
  return data
}

async function refreshAccount() {
  try {
    const [me, usage, providers, watchlist, storage] = await Promise.all([
      getJSON('/v1/me'),
      getJSON('/v1/me/usage'),
      getJSON('/v1/providers/status'),
      getJSON('/v1/me/watchlist'),
      getJSON('/v1/storage/status')
    ])
    $('account-name').textContent = `${me.name} · ${me.role}`
    $('account-key').textContent = `Key ${me.key_name}${me.expires_at ? ` · ${new Date(me.expires_at).toLocaleDateString()} 到期` : ''}`
    $('account-requests').textContent = `${usage.current_minute.requests} / ${usage.quotas.requests_per_minute}`
    $('account-daily').textContent = `今日 ${usage.daily.requests ?? 0} 次`
    $('account-datasets').textContent = `${usage.current_minute.datasets} / ${usage.quotas.datasets_per_minute}`
    $('account-builds').textContent = `构建中 ${usage.active_builds} / ${usage.quotas.concurrent_builds}`
    $('account-live').textContent = `连接 ${usage.live.connections} / ${usage.quotas.live_connections}`
    $('account-symbols').textContent = `标的 ${usage.live.symbols} / ${usage.quotas.live_symbols}`
    const massive = providers.massive || {}
    providerStatus = providers
    updateMarketAvailability()
    $('massive-status').textContent = massive.state || 'unknown'
    $('massive-plan').textContent = massive.plan || '—'
    const indexProvider = providers.index || {}
    $('index-status').textContent = indexProvider.state || 'disabled'
    $('index-detail').textContent = indexProvider.provider || 'disabled'
    const longbridge = providers.longbridge || {}
    $('longbridge-status').textContent = longbridge.state || 'unknown'
    $('longbridge-detail').textContent = `订阅池 ${longbridge.subscribed_symbols ?? 0} · 重连 ${longbridge.reconnects ?? 0}`
    const binance = providers.binance || {}
    $('binance-status').textContent = binance.state || 'unknown'
    $('binance-detail').textContent = `订阅池 ${binance.subscribed_symbols ?? 0} · 重连 ${binance.reconnects ?? 0}`
    const fmpNews = providers.fmp_news || {}
    $('fmp-news-status').textContent = fmpNews.state || 'disabled'
    $('fmp-news-status').className = fmpNews.state === 'enabled' ? 'ok' : fmpNews.state === 'degraded' ? 'bad' : 'warn'
    $('fmp-news-detail').textContent = fmpNews.last_success_at ? `最近成功 ${new Date(fmpNews.last_success_at).toLocaleString()}` : (fmpNews.error || `轮询 ${fmpNews.polling_interval || '—'}`)
    const storageLabels = {
      remote_clickhouse: '远端 ClickHouse 存储',
      remote_clickhouse_degraded: '远端 ClickHouse 故障',
      local_clickhouse: '本地 ClickHouse 存储',
      provider_only: 'Provider / Parquet',
      unknown: '存储状态不可用'
    }
    $('storage-status').textContent = storageLabels[storage.mode] || storage.mode
    $('storage-status').className = storage.mode === 'remote_clickhouse_degraded' ? 'bad' : 'ok'
    $('storage-detail').textContent = storage.mode === 'remote_clickhouse'
      ? `本地 ClickHouse 存储关闭 · revision ${storage.history_revision ?? 0}`
      : `revision ${storage.history_revision ?? 0} · ${storage.data_version || '—'}`
    const redisLabels = {
      remote_redis: '远端 Redis 热缓存',
      remote_redis_degraded: '远端 Redis 故障',
      local_redis: '本地 Redis 热缓存',
      disabled: 'Redis 缓存关闭'
    }
    $('redis-storage-status').textContent = redisLabels[storage.redis_mode] || storage.redis_mode || 'Redis 状态不可用'
    $('redis-storage-status').className = storage.redis_mode === 'remote_redis_degraded' ? 'bad' : storage.redis_mode === 'disabled' ? 'warn' : 'ok'
    if (storage.redis_mode === 'remote_redis') {
      $('redis-storage-detail').textContent = '本地 Redis 已关闭'
    } else if (storage.redis_mode === 'remote_redis_degraded') {
      $('redis-storage-detail').textContent = '服务端正在绕过缓存回源'
    } else if (storage.redis_mode === 'local_redis') {
      $('redis-storage-detail').textContent = '服务端 Redis 未启用'
    } else {
      $('redis-storage-detail').textContent = '未启用 Redis 热缓存'
    }
    if (storage.capability_stale) $('redis-storage-detail').textContent += ' · 状态探测暂时不可用'
    $('watchlist-symbols').value = (watchlist.symbols || []).join(',')
    $('watchlist-state').textContent = `已收藏 ${(watchlist.symbols || []).length} / ${watchlist.max_symbols ?? usage.quotas.live_symbols} · 不自动订阅`
    if (!socket || socket.readyState !== WebSocket.OPEN) setStatus('账号在线', 'ok')
    return providers
  } catch (error) {
    setStatus('账号状态不可用', 'bad')
    return null
  }
}

function lineFigure(key, title, color) {
  return {
    key,
    title,
    type: 'line',
    styles: () => ({ color, size: 1, style: 'solid' })
  }
}

function stickFigure(key, upperKey, lowerKey, color) {
  return {
    key,
    type: 'rect',
    attrs: ({ data, coordinate, barSpace }) => {
      if (!data.current || data.current[key] === null || !coordinate.current) return null
      const upper = coordinate.current[upperKey]
      const lower = coordinate.current[lowerKey]
      if (!Number.isFinite(upper) || !Number.isFinite(lower)) return null
      const width = Math.max(1, barSpace.bar * 0.1)
      return {
        x: coordinate.current.x - width / 2,
        y: Math.min(upper, lower),
        width,
        height: Math.max(1, Math.abs(upper - lower))
      }
    },
    styles: () => ({ color, style: 'fill' })
  }
}

function normalizeBar(bar) {
  const normalized = {
    timestamp: new Date(bar.timestamp).getTime(),
    open: Number(bar.open),
    high: Number(bar.high),
    low: Number(bar.low),
    close: Number(bar.close),
    volume: Number(bar.volume_decimal ?? bar.volume ?? 0)
  }
  if (bar.turnover !== undefined && bar.turnover !== null) normalized.turnover = Number(bar.turnover)
  return normalized
}

function intervalToPeriod(interval) {
	const match = /^(\d+)(m|h|d|w|mo|y)$/.exec(interval)
  if (!match) return { type: 'day', span: 1 }
	const types = { m: 'minute', h: 'hour', d: 'day', w: 'week', mo: 'month', y: 'year' }
  return { type: types[match[2]], span: Number(match[1]) }
}

function periodToInterval(period) {
	const suffixes = { minute: 'm', hour: 'h', day: 'd', week: 'w', month: 'mo', year: 'y' }
  return `${period.span}${suffixes[period.type] || 'd'}`
}

function setChartEmptyState(message = '') {
  const empty = $('chart-empty')
  empty.textContent = message
  empty.hidden = !message
}

function marketDefaults(symbol) {
  const upper = symbol.toUpperCase()
  if (upper.startsWith('F:')) return { session: 'continuous', adjustment: 'raw', market: '美期期货', timezone: 'America/Chicago' }
  if (/^I:(HSI|HSCEI|HSTECH)$/.test(upper)) return { session: 'regular', adjustment: 'raw', market: '香港指数', timezone: 'Asia/Hong_Kong' }
  if (upper.startsWith('I:')) return { session: 'regular', adjustment: 'raw', market: '美国指数', timezone: 'America/New_York' }
  if (upper.endsWith('.BINANCE')) return { session: 'continuous', adjustment: 'raw', market: 'Binance Spot', timezone: 'UTC' }
  if (upper.endsWith('.HK')) return { session: 'regular', adjustment: 'forward_adjusted', market: '港股', timezone: 'Asia/Hong_Kong' }
  if (upper.endsWith('.SH') || upper.endsWith('.SZ')) return { session: 'regular', adjustment: 'forward_adjusted', market: 'A 股', timezone: 'Asia/Shanghai' }
  return { session: 'regular', adjustment: 'forward_adjusted', market: '美股', timezone: 'America/New_York' }
}

if (!window.klinecharts) {
  $('error').textContent = 'KLineChart 静态资源加载失败，请重新拉取 go-client 镜像。'
  throw new Error('klinecharts is unavailable')
}

const chart = window.klinecharts.init('chart', {
  locale: 'zh-CN',
  timezone: marketDefaults($('symbol').value).timezone,
  zoomAnchor: 'cursor',
  styles: {
    grid: { horizontal: { color: '#252a34' }, vertical: { color: '#252a34' } },
    candle: {
      bar: {
        upColor: RED, downColor: GREEN, noChangeColor: '#9aa4b2',
        upBorderColor: RED, downBorderColor: GREEN, noChangeBorderColor: '#9aa4b2',
        upWickColor: RED, downWickColor: GREEN, noChangeWickColor: '#9aa4b2'
      }
    },
    xAxis: { axisLine: { color: '#343a46' }, tickLine: { color: '#343a46' }, tickText: { color: '#9aa4b2' } },
    yAxis: { axisLine: { color: '#343a46' }, tickLine: { color: '#343a46' }, tickText: { color: '#9aa4b2' } },
    separator: { color: '#343a46' },
    crosshair: {
      horizontal: { line: { color: '#7f8a99' }, text: { backgroundColor: '#343a46' } },
      vertical: { line: { color: '#7f8a99' }, text: { backgroundColor: '#343a46' } }
    }
  }
})

if (!chart) throw new Error('failed to initialize klinecharts')
chart.setZoomEnabled(true)
chart.setScrollEnabled(true)
chart.setZoomAnchor('cursor')
chart.setOffsetRightDistance(64)
chart.overrideXAxis({ scrollZoomEnabled: true })
chart.overrideYAxis({ paneId: 'candle_pane', scrollZoomEnabled: yAxisZoomEnabled })
$('y-axis-zoom').checked = yAxisZoomEnabled

function setYAxisZoomEnabled(enabled, persist = true) {
  yAxisZoomEnabled = Boolean(enabled)
  $('y-axis-zoom').checked = yAxisZoomEnabled
  chart.overrideYAxis({ paneId: 'candle_pane', scrollZoomEnabled: yAxisZoomEnabled })
  if (persist) {
    try {
      localStorage.setItem(Y_AXIS_ZOOM_STORAGE_KEY, String(yAxisZoomEnabled))
    } catch (_) {}
  }
}

function setLivePanelState(id, text, className = '') {
  $(id).textContent = text
  $(id).className = className
}

function compactMarketNumber(value) {
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) return '—'
  return new Intl.NumberFormat('zh-CN', { notation: Math.abs(numeric) >= 10000 ? 'compact' : 'standard', maximumFractionDigits: 2 }).format(numeric)
}

function tradeTime(timestamp) {
  if (!timestamp) return '—'
  const timezone = marketDefaults(liveFeed.symbol).timezone
  return new Date(timestamp).toLocaleTimeString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit', timeZone: timezone })
}

function renderTrades() {
  $('recent-trade-count').textContent = `最近成交 ${liveFeed.recentCount} 笔`
  $('live-trade-count').textContent = `实时新增 ${liveFeed.liveCount} 笔`
  if (!liveFeed.trades.length) {
    const empty = document.createElement('p')
    empty.className = 'live-empty'
    empty.textContent = liveFeed.initializing ? '正在加载最近成交' : '等待新的逐笔成交'
    $('trade-rows').replaceChildren(empty)
    return
  }
  const fragment = document.createDocumentFragment()
  for (const trade of liveFeed.trades) {
    const row = document.createElement('div')
    row.className = `market-row trade-row ${trade.direction === 2 ? 'up' : trade.direction === 1 ? 'down' : 'neutral'}`
    const direction = trade.direction === 2 ? '↑' : trade.direction === 1 ? '↓' : '—'
    const values = [tradeTime(trade.timestamp), trade.price, compactMarketNumber(trade.volume), trade.tradeType || direction]
    for (const value of values) {
      const cell = document.createElement('span')
      cell.textContent = value
      row.appendChild(cell)
    }
    row.title = `方向 ${direction} · 交易时段 ${trade.tradeSession}`
    fragment.appendChild(row)
  }
  $('trade-rows').replaceChildren(fragment)
}

function renderDepth(depth = liveFeed.depth) {
  if (providerStatus?.longbridge?.depth_enabled === false) {
    setLivePanelState('depth-state', '服务端未启用深度', 'warn')
    const empty = document.createElement('p')
    empty.className = 'live-empty'
    empty.textContent = '设置 GO_SERVER_LONGBRIDGE_DEPTH_ENABLED=true 并确认 OpenAPI 行情权限'
    $('depth-rows').replaceChildren(empty)
    return
  }
  if (!depth || (!depth.asks.length && !depth.bids.length)) {
    setLivePanelState('depth-state', '等待盘口', 'warn')
    const empty = document.createElement('p')
    empty.className = 'live-empty'
    empty.textContent = '休市、权限不足或尚未收到深度快照'
    $('depth-rows').replaceChildren(empty)
    return
  }
  setLivePanelState('depth-state', '实时更新', 'ok')
  const fragment = document.createDocumentFragment()
  const rows = depth.asks.map(level => ({ ...level, side: 'ask' })).concat(depth.bids.map((level, index) => ({ ...level, side: 'bid', spread: index === 0 })))
  for (const level of rows) {
    const row = document.createElement('div')
    row.className = `market-row ${level.side}${level.spread ? ' book-spread' : ''}`
    const values = [level.side === 'ask' ? `卖${level.position}` : `买${level.position}`, level.price, compactMarketNumber(level.volume), compactMarketNumber(level.orderNum)]
    for (const value of values) {
      const cell = document.createElement('span')
      cell.textContent = value
      row.appendChild(cell)
    }
    fragment.appendChild(row)
  }
  $('depth-rows').replaceChildren(fragment)
}

function formatQuoteNumber(value, digits = 6) {
  if (!Number.isFinite(value)) return '—'
  return value.toLocaleString('zh-CN', { minimumFractionDigits: 2, maximumFractionDigits: digits })
}

function renderQuote(quote = liveFeed.quote) {
  const container = $('security-quote')
  if (!quote || !Number.isFinite(quote.lastDone)) {
    container.className = 'security-quote neutral'
    $('security-last').textContent = '—'
    $('security-change').textContent = '等待实时行情'
    $('security-session').textContent = '—'
    return
  }
  $('security-last').textContent = formatQuoteNumber(quote.lastDone)
  const sessionLabels = { regular: '正常交易', pre: '盘前', post: '盘后', overnight: '夜盘' }
  $('security-session').textContent = sessionLabels[quote.tradeSession] || quote.tradeSession
  if (!Number.isFinite(quote.change) || !Number.isFinite(quote.changePercent)) {
    container.className = 'security-quote neutral'
    $('security-change').textContent = '涨跌暂不可用'
    return
  }
  const direction = quote.change > 0 ? 'up' : quote.change < 0 ? 'down' : 'neutral'
  const sign = quote.change > 0 ? '+' : ''
  const percentSign = quote.changePercent > 0 ? '+' : ''
  container.className = `security-quote ${direction}`
  $('security-change').textContent = `${sign}${formatQuoteNumber(quote.change)} (${percentSign}${quote.changePercent.toFixed(2)}%)`
}

function consumeQuote(value) {
  liveFeed.quote = window.liveMarketUtils.normalizeQuote(value)
  renderQuote()
}

function resetLivePanel(symbol) {
  Object.assign(liveFeed, { symbol, trades: [], bufferedTrades: [], initializing: true, recentCount: 0, liveCount: 0, depth: null, quote: null, recoverOnConnect: false })
  setLivePanelState('trades-state', '正在加载', 'warn')
  renderQuote()
  renderTrades()
  renderDepth()
}

async function loadRecentTrades(symbol, generation) {
  liveFeed.initializing = true
  setLivePanelState('trades-state', '正在同步最近成交', 'warn')
  renderTrades()
  try {
    const data = await getJSON(`/v1/live/trades/${encodeURIComponent(symbol)}?limit=100`)
    if (generation !== liveGeneration || liveFeed.symbol !== symbol) return
    const recent = window.liveMarketUtils.normalizeTrades(data.trades)
    liveFeed.trades = window.liveMarketUtils.mergeInitialAndBuffered(recent, liveFeed.bufferedTrades, 100)
    liveFeed.recentCount = recent.length
    liveFeed.bufferedTrades = []
    liveFeed.initializing = false
    setLivePanelState('trades-state', socket?.readyState === WebSocket.OPEN ? '实时更新' : '最近成交', socket?.readyState === WebSocket.OPEN ? 'ok' : 'warn')
    renderTrades()
  } catch (error) {
    if (generation !== liveGeneration || liveFeed.symbol !== symbol) return
    liveFeed.trades = liveFeed.bufferedTrades.slice().sort((a, b) => b.timestamp - a.timestamp).slice(0, 100)
    liveFeed.bufferedTrades = []
    liveFeed.initializing = false
    setLivePanelState('trades-state', '最近成交加载失败，等待推送', 'warn')
    renderTrades()
  }
}

function consumeLiveTrades(value) {
  const trades = window.liveMarketUtils.normalizeTrades(value)
  if (!trades.length) return
  liveFeed.liveCount += trades.length
  if (liveFeed.initializing) liveFeed.bufferedTrades.push(...trades)
  else liveFeed.trades = trades.concat(liveFeed.trades).sort((a, b) => b.timestamp - a.timestamp).slice(0, 100)
  setLivePanelState('trades-state', '实时更新', 'ok')
  renderTrades()
}

function stopLive() {
  liveGeneration++
  closeSocket()
}

function startLive(symbol, period, callback) {
  stopLive()
  const interval = periodToInterval(period)
  const monitorBase = { symbol, interval, count: 0, connectedAt: 0, lastMessageAt: 0, lastType: '' }
  const generation = ++liveGeneration
  resetLivePanel(symbol)
  loadRecentTrades(symbol, generation)
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${scheme}://${location.host}/v1/live/ws`)
  socket = ws
  setWSState('connecting', { ...monitorBase, detail: `正在连接并订阅 ${symbol}` })
  ws.onopen = () => {
    if (socket !== ws) return
    ws.send(JSON.stringify({ action: 'subscribe', symbols: [symbol], events: ['bar', 'quote', 'trade', 'depth'], status: true }))
    setWSState('connecting', { detail: `本地 WebSocket 已连接，正在按需订阅 ${symbol}` })
  }
  ws.onmessage = event => {
    if (socket !== ws) return
    let payload
    try {
      payload = JSON.parse(event.data)
    } catch (_) {
      setWSState('error', { detail: '收到无法解析的 WebSocket 消息' })
      return
    }
    if (payload.type === 'status') {
      const state = payload.state === 'connected' ? 'connected' : payload.state === 'reconnecting' ? 'reconnecting' : 'connecting'
      const values = { detail: payload.detail || `正在按需订阅 ${symbol}` }
      if (state === 'connected') values.connectedAt = Date.now()
      setWSState(state, values)
      if (state === 'reconnecting') {
        liveFeed.recoverOnConnect = true
        liveFeed.initializing = true
        liveFeed.bufferedTrades = []
        liveFeed.depth = null
        setLivePanelState('trades-state', '连接恢复中', 'warn')
        setLivePanelState('depth-state', '连接恢复中', 'warn')
      } else if (state === 'connected' && liveFeed.recoverOnConnect) {
        liveFeed.recoverOnConnect = false
        loadRecentTrades(symbol, generation)
        renderDepth()
      }
      return
    }
    const now = Date.now()
    if (payload.type === 'gap') {
      setWSState('gap', { count: wsMonitor.count + 1, lastMessageAt: now, lastType: 'gap', detail: `${symbol} 推送出现缺口：${payload.reason || 'unknown'}` })
      liveFeed.depth = null
      liveFeed.bufferedTrades = []
      loadRecentTrades(symbol, generation)
      renderDepth()
      return
    }
    setWSState('connected', { count: wsMonitor.count + 1, lastMessageAt: now, lastType: payload.type || 'message' })
    if (payload.type === 'bar' && payload.bar && interval === '1m') callback(normalizeBar(payload.bar))
    if (payload.type === 'quote' && payload.quote) consumeQuote(payload.quote)
    if (payload.type === 'trade' && payload.trade) consumeLiveTrades(payload.trade)
    if (payload.type === 'depth' && payload.depth) {
      liveFeed.depth = window.liveMarketUtils.normalizeDepth(payload.depth, 10)
      renderDepth()
    }
  }
  ws.onerror = () => {
    if (socket === ws) {
      setWSState('error', { detail: `${symbol} WebSocket 连接异常` })
      setLivePanelState('trades-state', '连接异常', 'bad')
      setLivePanelState('depth-state', '连接异常', 'bad')
    }
  }
  ws.onclose = event => {
    if (socket !== ws) return
    socket = null
    const reason = event.reason ? `：${event.reason}` : ''
    setWSState('closed', { detail: `${symbol} 连接已关闭（code ${event.code}）${reason}` })
    setLivePanelState('trades-state', '连接已关闭', 'bad')
    setLivePanelState('depth-state', '连接已关闭', 'bad')
  }
}

chart.setDataLoader({
  async getBars({ type, timestamp, symbol, period, callback }) {
    if ((type !== 'init' && type !== 'forward') || !activeQuery) {
      callback([], false)
      return
    }
    const generation = activeQuery.generation
    const interval = periodToInterval(period)
    const ticker = symbol.ticker
    if (!window.marketHistory.isCurrentQuery(activeQuery, generation, ticker, interval)) {
      callback([], { forward: false, backward: false })
      return
    }
    let range
    if (type === 'init') {
      range = window.marketHistory.initialRange(activeQuery.to, interval, activeQuery.maxHistoryYears)
    } else {
      const upper = Math.min(Number(timestamp) || activeQuery.loadedFrom, activeQuery.loadedFrom)
      range = window.marketHistory.olderRange(upper, interval, activeQuery.floor, activeQuery.allowBeyondFloor)
    }
    if (!range) {
      callback([], { forward: false, backward: false })
      return
    }
    $('error').textContent = ''
    $('source').textContent = '缓存：加载中'
    try {
      const query = new URLSearchParams({
        interval,
        from: new Date(range.from).toISOString(),
        to: new Date(range.to).toISOString(),
        session: activeQuery.session,
        adjustment: activeQuery.adjustment
      })
      const data = await getJSON(`/v1/bars/${encodeURIComponent(ticker)}?${query}`)
      if (!window.marketHistory.isCurrentQuery(activeQuery, generation, ticker, interval)) {
        callback([], { forward: false, backward: false })
        return
      }
      const bars = (Array.isArray(data.bars) ? data.bars : []).map(normalizeBar).sort((a, b) => a.timestamp - b.timestamp)
      activeQuery.loadedFrom = Math.min(activeQuery.loadedFrom, range.from)
      lastBars = window.marketHistory.mergeBars(lastBars, bars)
      const hasOlder = window.marketHistory.canRequestOlder(range.from, activeQuery.floor, false, bars.length)
      callback(bars, { forward: hasOlder, backward: false })
      $('source').textContent = `缓存：${data.source}`
      $('count').textContent = `Bars：${lastBars.length}`
      $('updated').textContent = `更新：${new Date().toLocaleTimeString()}`
      if (data.warning) $('error').textContent = `部分历史数据加载失败，已展示可用数据：${data.warning}`
      if (type === 'init') {
        setChartEmptyState(bars.length ? '' : '暂无 K 线数据')
      }
    } catch (error) {
      callback([], { forward: false, backward: false })
      if (!window.marketHistory.isCurrentQuery(activeQuery, generation, ticker, interval)) return
      $('error').textContent = error.message
      $('source').textContent = '缓存：加载失败'
      if (type === 'init') {
        lastBars = []
        $('count').textContent = 'Bars：0'
        $('updated').textContent = '更新：—'
        setChartEmptyState('K 线加载失败，请重试')
      }
    }
  },
  subscribeBar({ symbol, period, callback }) {
    if (/^(I|F):/.test(symbol.ticker)) {
      stopLive()
      const detail = symbol.ticker.startsWith('I:') ? '指数当前仅提供历史或延迟 K 线，尚未接入实时 WebSocket' : 'Massive 期货当前仅提供历史或延迟 K 线，尚未接入实时 WebSocket'
      setWSState('disabled', { symbol: symbol.ticker, detail })
      return
    }
    startLive(symbol.ticker, period, callback)
  },
  unsubscribeBar() {
    stopLive()
  }
})

function resetFormulaWorker(reason) {
  if (formulaWorker) formulaWorker.terminate()
  formulaWorker = new Worker('/formula-worker.js')
  formulaWorker.onmessage = event => {
    const pending = workerRequests.get(event.data.id)
    if (!pending) return
    clearTimeout(pending.timer)
    workerRequests.delete(event.data.id)
    event.data.ok ? pending.resolve(event.data.result) : pending.reject(new Error(event.data.error))
  }
  formulaWorker.onerror = () => resetFormulaWorker('公式计算 Worker 异常，已自动重启')
  if (reason) $('indicator-error').textContent = reason
}

function runFormula(type, indicator, bars = []) {
  if (!formulaWorker) resetFormulaWorker()
  const id = ++workerSequence
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => {
      workerRequests.delete(id)
      resetFormulaWorker('公式计算超过 10 秒，已终止')
      reject(new Error('公式计算超过 10 秒，已终止'))
    }, 10000)
    workerRequests.set(id, { resolve, reject, timer })
    formulaWorker.postMessage({ id, type, formula: indicator.formula, parameters: indicator.parameters || [], bars })
  })
}

const FORMULA_COLORS = { COLORBLUE: BLUE, COLORYELLOW: YELLOW, COLORRED: RED, COLORGREEN: GREEN, COLORWHITE: '#edf7f2', COLORCYAN: CYAN, COLORMAGENTA: PURPLE }
function formulaColor(value, fallback = '#65d6aa') {
  if (!value) return fallback
  return FORMULA_COLORS[String(value).toUpperCase()] || value
}
function escapeHTML(value) { return String(value).replace(/[&<>"']/g, char => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]) }

function formulaManifest(formula) {
  const outputs = []
  const drawings = []
  for (const raw of formula.split(';')) {
    const statement = raw.replace(/\{[\s\S]*?\}/g, '').trim()
    const output = /^([\p{L}_][\p{L}\p{N}_]*)\s*:(?!=)([\s\S]*)$/u.exec(statement)
    if (output) {
      const suffix = output[2].split(',').slice(1).map(value => value.trim().toUpperCase())
      const barMode = suffix.includes('VOLSTICK') ? 'volume' : suffix.includes('COLORSTICK') ? 'signed' : ''
      outputs.push({ name: output[1], color: formulaColor(suffix.find(value => value.startsWith('COLOR') && value !== 'COLORSTICK')), barMode, hidden: suffix.includes('NODRAW') })
    }
    const drawing = /^(DRAWTEXT|DRAWICON|DRAWNUMBER|STICKLINE|DRAWLINE|POLYLINE|DRAWBAND|DRAWKLINE)\s*\(/i.exec(statement)
    if (drawing) drawings.push({ function: drawing[1].toUpperCase(), color: formulaColor((statement.match(/,\s*(COLOR(?:[0-9A-F]{6}|[A-Z]+))/i) || [])[1]) })
  }
  return { outputs, drawings }
}

function formulaFigures(manifest) {
  const figures = manifest.outputs.filter(output => !output.hidden).map(output => output.barMode ? {
    key: `o_${output.name}`, title: `${output.name}: `, type: 'bar', baseValue: 0,
    styles: ({ data }) => {
      const row = data?.current || {}
      const color = output.barMode === 'volume'
        ? (Number(row.__close) >= Number(row.__open) ? RED : GREEN)
        : (Number(row[`o_${output.name}`]) >= 0 ? RED : GREEN)
      return { style: 'fill', color, borderColor: color }
    }
  } : lineFigure(`o_${output.name}`, `${output.name}: `, output.color))
  manifest.drawings.forEach((drawing, index) => {
    if (drawing.function === 'STICKLINE') {
      figures.push(lineFigure(`d${index}_top`, '', 'rgba(0,0,0,0)'), lineFigure(`d${index}_bottom`, '', 'rgba(0,0,0,0)'), stickFigure(`d${index}_mid`, `d${index}_top`, `d${index}_bottom`, drawing.color))
    } else {
      figures.push({
        key: `d${index}_point`, type: 'text',
        attrs: ({ data }) => ({ text: data.current?.[`d${index}_text`] || (drawing.function === 'DRAWICON' ? '●' : '•') }),
        styles: () => ({ color: drawing.color, size: 15, weight: 'bold' })
      })
    }
  })
  return figures
}

async function formulaRows(indicator, data) {
  if (!data.length) return []
  let result
  try {
    result = await runFormula('evaluate', indicator, data)
  } catch (error) {
    $('indicator-state').textContent = `${indicator.name} 计算失败`
    $('indicator-error').textContent = `${indicator.name}: ${error.message}`
    return data.map(() => ({}))
  }
  const rows = data.map(bar => ({ __open: Number(bar.open), __close: Number(bar.close) }))
  for (const output of result.outputs || []) output.data.forEach((value, index) => { rows[index][`o_${output.name}`] = value })
  for (const event of result.drawings || []) {
    const index = event.barIndex
    if (!rows[index]) continue
    const key = `d${event.statementIndex}`
    if (event.function === 'STICKLINE') {
      rows[index][`${key}_top`] = event.values.price1
      rows[index][`${key}_bottom`] = event.values.price2
      rows[index][`${key}_mid`] = (event.values.price1 + event.values.price2) / 2
    } else {
      rows[index][`${key}_point`] = event.values.price ?? event.values.price1
      rows[index][`${key}_text`] = event.text || (event.values.value === undefined ? '' : String(event.values.value))
    }
  }
  return rows
}

async function applyFormulaIndicators() {
  for (const active of activeFormulaCharts) {
    if (chart.getIndicators({ name: active.name, paneId: active.paneId }).length) chart.removeIndicator({ name: active.name, paneId: active.paneId })
  }
  activeFormulaCharts = []
  const enabled = localIndicators.filter(indicator => indicator.enabled).slice(0, 18)
  for (const indicator of enabled) {
    const name = `TDX_${indicator.id}_${indicator.revision}`.replace(/[^A-Za-z0-9_]/g, '_')
    const manifest = formulaManifest(indicator.formula)
    if (!registeredFormulaNames.has(name)) {
      window.klinecharts.registerIndicator({ name, shortName: indicator.name, series: indicator.pane === 'main' ? 'price' : 'normal', precision: 4, figures: formulaFigures(manifest), calc: data => formulaRows(indicator, data) })
      registeredFormulaNames.add(name)
    }
    const paneId = indicator.pane === 'main' ? 'candle_pane' : `tdx_${indicator.id}`
    chart.createIndicator({ name, paneId }, indicator.pane === 'main')
    if (indicator.pane !== 'main') chart.setPaneOptions({ id: paneId, height: 190, minHeight: 100 })
    activeFormulaCharts.push({ name, paneId })
  }
  $('indicator-state').textContent = `${enabled.length} 个已启用 · 仅保存在本机`
}

function renderParameterRows(parameters) {
  $('indicator-parameters').innerHTML = ''
  for (const parameter of parameters || []) {
    const row = document.createElement('div')
    row.className = 'parameter-row'
    row.innerHTML = `<label>参数<input data-field="name" value="${escapeHTML(parameter.name)}"></label><label>当前值<input data-field="value" type="number" value="${Number(parameter.value)}"></label><label>最小值<input data-field="min" type="number" value="${Number(parameter.min)}"></label><label>最大值<input data-field="max" type="number" value="${Number(parameter.max)}"></label><label>步长<input data-field="step" type="number" value="${Number(parameter.step)}"></label>`
    $('indicator-parameters').appendChild(row)
  }
}

function editorParameters() {
  return [...document.querySelectorAll('.parameter-row')].map(row => {
    const get = field => row.querySelector(`[data-field="${field}"]`).value
    const value = Number(get('value'))
    return { name: get('name').trim().toUpperCase(), value, default: value, min: Number(get('min')), max: Number(get('max')), step: Number(get('step')) }
  })
}

function editorIndicator() {
  return { name: $('indicator-name').value.trim(), pane: $('indicator-pane').value, formula: $('indicator-formula').value, parameters: editorParameters(), warnings: [...$('indicator-warnings').querySelectorAll('span')].map(item => item.textContent), enabled: $('indicator-enabled').checked, sort_order: selectedIndicator?.sort_order || 100, revision: Number($('indicator-revision').value || 0) }
}

function selectIndicator(indicator) {
  selectedIndicator = indicator
  $('indicator-id').value = indicator?.id || ''
  $('indicator-revision').value = indicator?.revision || 0
  $('indicator-name').value = indicator?.name || '新指标'
  $('indicator-pane').value = indicator?.pane || 'main'
  $('indicator-formula').value = indicator?.formula || 'MA5:MA(CLOSE,5),COLORWHITE;'
  $('indicator-enabled').checked = indicator?.enabled ?? false
  $('indicator-name').disabled = indicator?.kind === 'template'
  $('indicator-pane').disabled = indicator?.kind === 'template'
  $('indicator-formula').disabled = indicator?.kind === 'template'
  $('delete-indicator').disabled = !indicator || indicator.kind === 'template'
  $('copy-indicator').disabled = !indicator
  renderParameterRows(indicator?.parameters || [])
  $('indicator-warnings').innerHTML = (indicator?.warnings || []).map(value => `<span>${escapeHTML(value)}</span>`).join('<br>')
  renderIndicatorList()
}

function renderIndicatorList() {
  $('indicator-list').innerHTML = ''
  for (const indicator of localIndicators) {
    const row = document.createElement('button')
    row.type = 'button'
    row.className = `indicator-item${selectedIndicator?.id === indicator.id ? ' active' : ''}`
    row.innerHTML = `<input type="checkbox" ${indicator.enabled ? 'checked' : ''} aria-label="启用"><span>${escapeHTML(indicator.name)}</span><em>${indicator.kind === 'template' ? '内置' : '个人'} · ${indicator.pane === 'main' ? '主图' : '副图'}</em>`
    row.querySelector('input').addEventListener('click', async event => {
      event.stopPropagation()
      try {
        const updated = await getJSON(`/v1/me/indicators/${indicator.id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...indicator, enabled: event.target.checked }) })
        localIndicators = localIndicators.map(item => item.id === updated.id ? updated : item)
        if (selectedIndicator?.id === updated.id) selectIndicator(updated)
        await applyFormulaIndicators(); renderIndicatorList()
      } catch (error) { $('indicator-error').textContent = error.message; event.target.checked = indicator.enabled }
    })
    row.addEventListener('click', () => selectIndicator(indicator))
    $('indicator-list').appendChild(row)
  }
}

async function loadIndicators() {
  try {
    const response = await getJSON('/v1/me/indicators')
    localIndicators = response.indicators || []
  } catch (error) {
    localIndicators = []
    $('indicator-state').textContent = `本地指标读取失败：${error.message}`
  }
  renderIndicatorList()
  if (localIndicators.length) selectIndicator(localIndicators[0])
  await applyFormulaIndicators()
}

async function analyzeEditor() {
  $('indicator-error').textContent = ''
  const draft = editorIndicator()
  let result = await runFormula('compile', draft)
  if (result.missing?.length) {
    const existing = new Map(draft.parameters.map(parameter => [parameter.name, parameter]))
    for (const name of result.missing) existing.set(name, { name, value: 1, default: 1, min: 0, max: 500, step: 1 })
    renderParameterRows([...existing.values()])
    result = await runFormula('compile', editorIndicator())
  }
  $('indicator-warnings').innerHTML = (result.warnings || []).map(value => `<span>${escapeHTML(value)}</span>`).join('<br>')
  if (!lastBars.length) throw new Error('请先加载一只股票的 K 线，才能预览并保存公式')
  await runFormula('evaluate', editorIndicator(), lastBars)
  $('indicator-error').textContent = `预览通过：${lastBars.length} 根 K 线`
  return result
}

$('manage-indicators').addEventListener('click', () => { $('indicator-manager').hidden = !$('indicator-manager').hidden })
$('y-axis-zoom').addEventListener('change', event => {
  setYAxisZoomEnabled(event.target.checked)
  if (!event.target.checked && $('symbol').value.trim()) $('query').requestSubmit()
})
$('reset-futu-view').addEventListener('click', async () => {
  const button = $('reset-futu-view')
  button.disabled = true
  $('indicator-error').textContent = ''
  try {
    const response = await getJSON('/v1/me/indicators/reset-display', { method: 'POST' })
    const selectedID = selectedIndicator?.id
    localIndicators = response.indicators || []
    selectIndicator(localIndicators.find(indicator => indicator.id === selectedID) || localIndicators[0] || null)
    await applyFormulaIndicators()
    chart.setBarSpace(10)
    setYAxisZoomEnabled(false)
    if ($('symbol').value.trim()) $('query').requestSubmit()
    $('indicator-state').textContent = '已恢复 Futu 默认 · 0 个指标'
  } catch (error) {
    $('indicator-error').textContent = error.message
  } finally {
    button.disabled = false
  }
})
$('close-indicators').addEventListener('click', () => { $('indicator-manager').hidden = true })
$('new-indicator').addEventListener('click', () => selectIndicator(null))
$('analyze-indicator').addEventListener('click', async () => { try { await analyzeEditor() } catch (error) { $('indicator-error').textContent = error.message } })
$('indicator-editor').addEventListener('submit', async event => {
  event.preventDefault()
  try {
    const analysis = await analyzeEditor()
    const mutation = { ...editorIndicator(), warnings: analysis.warnings || [] }
    const id = $('indicator-id').value
    const saved = await getJSON(id ? `/v1/me/indicators/${id}` : '/v1/me/indicators', { method: id ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(mutation) })
    localIndicators = id ? localIndicators.map(item => item.id === saved.id ? saved : item) : [...localIndicators, saved]
    selectIndicator(saved); await applyFormulaIndicators()
    $('indicator-error').textContent = '已保存到本机数据缓存，不会上传到 go-server'
  } catch (error) { $('indicator-error').textContent = error.message }
})
$('copy-indicator').addEventListener('click', async () => {
  if (!selectedIndicator) return
  try {
    const copy = await getJSON(`/v1/me/indicators/${selectedIndicator.id}/copy`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({}) })
    localIndicators.push(copy); selectIndicator(copy); await applyFormulaIndicators()
  } catch (error) { $('indicator-error').textContent = error.message }
})
$('delete-indicator').addEventListener('click', async () => {
  if (!selectedIndicator || selectedIndicator.kind === 'template' || !confirm(`删除指标“${selectedIndicator.name}”？`)) return
  try {
    await getJSON(`/v1/me/indicators/${selectedIndicator.id}?revision=${selectedIndicator.revision}`, { method: 'DELETE' })
    localIndicators = localIndicators.filter(item => item.id !== selectedIndicator.id); selectIndicator(localIndicators[0] || null); await applyFormulaIndicators()
  } catch (error) { $('indicator-error').textContent = error.message }
})
$('save-watchlist').addEventListener('click', async () => {
  try {
    const symbols = $('watchlist-symbols').value.split(',').map(value => value.trim()).filter(Boolean)
    await getJSON('/v1/me/watchlist', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ symbols })
    })
    $('watchlist-state').textContent = '已保存'
    await refreshAccount()
  } catch (error) {
    $('watchlist-state').textContent = error.message
  }
})

$('market').addEventListener('change', () => {
  const placeholders = { us: '输入代码或名称，例如 NVDA / I:VIX / I:IXIC', hk: '输入代码或名称，例如 700.HK / I:HSI', cn: '输入代码或名称，例如 600519.SH / 贵州茅台' }
  $('symbol').value = ''
  $('symbol').placeholder = placeholders[$('market').value]
  renderSymbolOptions()
})

for (const button of document.querySelectorAll('#period-buttons button[data-interval]')) {
  button.addEventListener('click', () => {
    $('interval').value = button.dataset.interval
    for (const item of document.querySelectorAll('#period-buttons button[data-interval]')) {
      item.setAttribute('aria-pressed', String(item === button))
    }
    if ($('symbol').value.trim()) $('query').requestSubmit()
  })
}

$('query').addEventListener('submit', event => {
  event.preventDefault()
  $('error').textContent = ''
  try {
    const symbol = normalizeSelectedMarketSymbol($('symbol').value)
    if (!symbol) throw new Error('请输入代码')
    if (!marketHistoryEnabled(symbolMarket(symbol))) throw new Error('服务端未启用该市场的历史行情 Provider，请检查对应历史渠道配置后重启 go-server')
    $('symbol').value = symbol
    renderSecurityIdentity(symbol)
    const interval = $('interval').value
    const defaults = marketDefaults(symbol)
    const to = Date.now()
    const maxHistoryYears = window.marketHistory.historyYearsFor(symbol, providerStatus)
    const initial = window.marketHistory.initialRange(to, interval, maxHistoryYears)
    activeQuery = {
      symbol,
      to,
      floor: initial.floor,
      loadedFrom: to,
      allowBeyondFloor: false,
      maxHistoryYears,
      interval,
      session: defaults.session,
      adjustment: defaults.adjustment,
      generation: ++queryGeneration
    }
    lastBars = []
    $('source').textContent = `市场：${defaults.market} · ${defaults.timezone} · 加载中`
    $('count').textContent = 'Bars：0'
    $('updated').textContent = '更新：—'
    setChartEmptyState('正在加载 K 线')
    chart.setTimezone(defaults.timezone)
    const crypto = symbol.endsWith('.BINANCE')
    const nextSymbol = { ticker: symbol, pricePrecision: crypto ? 8 : 4, volumePrecision: crypto ? 8 : 0 }
    const nextPeriod = intervalToPeriod(interval)
    const chartChanges = window.marketHistory.chartQueryChanges(chart.getSymbol(), chart.getPeriod(), nextSymbol, nextPeriod)
    if (chartChanges.symbol) chart.setSymbol(nextSymbol)
    if (chartChanges.period) chart.setPeriod(nextPeriod)
    if (!chartChanges.symbol && !chartChanges.period) chart.resetData()
  } catch (error) {
    $('error').textContent = error.message
  }
})

resetFormulaWorker()
loadIndicators().catch(error => { $('indicator-state').textContent = error.message })
setInterval(refreshAccount, 10000)
setInterval(renderWSStatus, 1000)

async function bootstrap() {
  await Promise.all([refreshAccount(), loadSymbolOptions()])
  $('query').requestSubmit()
}

bootstrap()
