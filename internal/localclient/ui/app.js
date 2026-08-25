const $ = id => document.getElementById(id)
const NX_STORAGE_KEY = 'market-bridge:nx-indicator'
const MX_STORAGE_KEY = 'market-bridge:mx-macd-indicator'
const MX_INDICATOR_NAME = 'MX_MACD'
const MX_PANE_ID = 'mx_macd_pane'
const BLUE = '#4f8cff'
const YELLOW = '#f2c94c'
const ORANGE = '#ff8d1e'
const CYAN = '#0caee6'
const PURPLE = '#e970dc'
const RED = '#ff4d5a'
const GREEN = '#2ac99a'
let socket = null
let activeQuery = null
let lastBars = []
let cloudIndicators = []
let selectedIndicator = null
let activeFormulaCharts = []
const registeredFormulaNames = new Set()
let workerSequence = 0
let formulaWorker = null
let indicatorUserID = localStorage.getItem('market-bridge:formula-indicators:user') || 'unknown'
const workerRequests = new Map()
const INDICATOR_MIGRATION_KEY = 'market-bridge:formula-indicators:migrated-v1'

function setStatus(text, state = '') {
  $('status').textContent = text
  $('status').className = state
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
    $('massive-status').textContent = massive.state || 'unknown'
    $('massive-plan').textContent = massive.plan || '—'
    const longbridge = providers.longbridge || {}
    $('longbridge-status').textContent = longbridge.state || 'unknown'
    $('longbridge-detail').textContent = `订阅池 ${longbridge.subscribed_symbols ?? 0} · 重连 ${longbridge.reconnects ?? 0}`
    const binance = providers.binance || {}
    $('binance-status').textContent = binance.state || 'unknown'
    $('binance-detail').textContent = `订阅池 ${binance.subscribed_symbols ?? 0} · 重连 ${binance.reconnects ?? 0}`
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
    $('watchlist-symbols').value = (watchlist.symbols || []).join(',')
    $('watchlist-state').textContent = `允许 ${(watchlist.allowed_symbols || []).join(',')}`
    if (!socket || socket.readyState !== WebSocket.OPEN) setStatus('账号在线', 'ok')
  } catch (error) {
    setStatus('账号状态不可用', 'bad')
  }
}

function ema(data, field, period) {
  const alpha = 2 / (period + 1)
  let previous = null
  return data.map(item => {
    const value = Number(item[field])
    previous = previous === null ? value : value * alpha + previous * (1 - alpha)
    return previous
  })
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

function registerNX() {
  window.klinecharts.registerIndicator({
    name: 'NX',
    shortName: 'NX 牛熊分界线',
    series: 'price',
    calcParams: [24, 23, 89, 90],
    figures: [
      lineFigure('blueUpper', 'A: ', BLUE),
      lineFigure('blueLower', 'B: ', BLUE),
      stickFigure('blueBreak', 'blueUpper', 'blueLower', BLUE),
      lineFigure('yellowUpper', 'A1: ', YELLOW),
      lineFigure('yellowLower', 'B1: ', YELLOW),
      stickFigure('yellowBreak', 'yellowUpper', 'yellowLower', YELLOW)
    ],
    calc: (data, indicator) => {
      const params = indicator.calcParams.map(value => Math.max(1, Number(value) || 1))
      const blueUpper = ema(data, 'high', params[0])
      const blueLower = ema(data, 'low', params[1])
      const yellowUpper = ema(data, 'high', params[2])
      const yellowLower = ema(data, 'low', params[3])
      return data.map((bar, index) => {
        const close = Number(bar.close)
        const blueOutside = close > blueUpper[index] || close < blueLower[index]
        const yellowOutside = close > yellowUpper[index] || close < yellowLower[index]
        return {
          blueUpper: blueUpper[index],
          blueLower: blueLower[index],
          blueBreak: blueOutside ? (blueUpper[index] + blueLower[index]) / 2 : null,
          yellowUpper: yellowUpper[index],
          yellowLower: yellowLower[index],
          yellowBreak: yellowOutside ? (yellowUpper[index] + yellowLower[index]) / 2 : null
        }
      })
    }
  })
}

function emaNumbers(values, period) {
  const alpha = 2 / (period + 1)
  let previous = null
  return values.map(raw => {
    const value = Number(raw)
    previous = previous === null ? value : value * alpha + previous * (1 - alpha)
    return previous
  })
}

function dynamicRef(values, index, distance) {
  if (!Number.isInteger(distance) || distance < 0 || index - distance < 0) return null
  const value = values[index - distance]
  return value === undefined ? null : value
}

function finiteValues(...values) {
  return values.every(Number.isFinite)
}

function calculateMX(data, params) {
  const closes = data.map(bar => Number(bar.close))
  const shortEMA = emaNumbers(closes, params[0])
  const longEMA = emaNumbers(closes, params[1])
  const diff = closes.map((_, index) => shortEMA[index] - longEMA[index])
  const dea = emaNumbers(diff, params[2])
  const macd = diff.map((value, index) => (value - dea[index]) * 2)
  const length = data.length
  const n1 = Array(length).fill(null)
  const m1 = Array(length).fill(null)
  const cc1 = Array(length).fill(null)
  const cc2 = Array(length).fill(null)
  const cc3 = Array(length).fill(null)
  const ch1 = Array(length).fill(null)
  const ch2 = Array(length).fill(null)
  const ch3 = Array(length).fill(null)
  const difl1 = Array(length).fill(null)
  const difl2 = Array(length).fill(null)
  const difl3 = Array(length).fill(null)
  const difh1 = Array(length).fill(null)
  const difh2 = Array(length).fill(null)
  const difh3 = Array(length).fill(null)
  const ccc = Array(length).fill(false)
  const jjj = Array(length).fill(false)
  const dbbl = Array(length).fill(false)
  const dbjg = Array(length).fill(false)
  let lastNegativeCross = null
  let lastPositiveCross = null

  for (let index = 0; index < length; index++) {
    const previousMACD = index > 0 ? macd[index - 1] : null
    const negativeCross = index > 0 && previousMACD >= 0 && macd[index] < 0
    const positiveCross = index > 0 && previousMACD <= 0 && macd[index] > 0
    if (negativeCross) lastNegativeCross = index
    if (positiveCross) lastPositiveCross = index
    if (lastNegativeCross !== null) {
      n1[index] = index - lastNegativeCross
      cc1[index] = negativeCross ? closes[index] : Math.min(cc1[index - 1], closes[index])
      difl1[index] = negativeCross ? diff[index] : Math.min(difl1[index - 1], diff[index])
    }
    if (lastPositiveCross !== null) {
      m1[index] = index - lastPositiveCross
      ch1[index] = positiveCross ? closes[index] : Math.max(ch1[index - 1], closes[index])
      difh1[index] = positiveCross ? diff[index] : Math.max(difh1[index - 1], diff[index])
    }

    const previousMACDNegative = index > 0 && previousMACD < 0 && diff[index] < 0
    const negativeRefDistance = m1[index] === null ? null : m1[index] + 1
    cc2[index] = dynamicRef(cc1, index, negativeRefDistance)
    cc3[index] = dynamicRef(cc2, index, negativeRefDistance)
    difl2[index] = dynamicRef(difl1, index, negativeRefDistance)
    difl3[index] = dynamicRef(difl2, index, negativeRefDistance)
    const aaa = finiteValues(cc1[index], cc2[index], difl1[index], difl2[index]) &&
      cc1[index] < cc2[index] && difl1[index] > difl2[index] && previousMACDNegative
    const bbb = finiteValues(cc1[index], cc3[index], difl1[index], difl2[index], difl3[index]) &&
      cc1[index] < cc3[index] && difl1[index] < difl2[index] && difl1[index] > difl3[index] && previousMACDNegative
    ccc[index] = (aaa || bbb) && diff[index] < 0
    jjj[index] = index > 0 && ccc[index - 1] && Math.abs(diff[index - 1]) >= Math.abs(diff[index]) * 1.01

    const previousMACDPositive = index > 0 && previousMACD > 0 && diff[index] > 0
    const positiveRefDistance = n1[index] === null ? null : n1[index] + 1
    ch2[index] = dynamicRef(ch1, index, positiveRefDistance)
    ch3[index] = dynamicRef(ch2, index, positiveRefDistance)
    difh2[index] = dynamicRef(difh1, index, positiveRefDistance)
    difh3[index] = dynamicRef(difh2, index, positiveRefDistance)
    const zjdbl = finiteValues(ch1[index], ch2[index], difh1[index], difh2[index]) &&
      ch1[index] > ch2[index] && difh1[index] < difh2[index] && previousMACDPositive
    const gxdbl = finiteValues(ch1[index], ch3[index], difh1[index], difh2[index], difh3[index]) &&
      ch1[index] > ch3[index] && difh1[index] > difh2[index] && difh1[index] < difh3[index] && previousMACDPositive
    dbbl[index] = (zjdbl || gxdbl) && diff[index] > 0
    dbjg[index] = index > 0 && dbbl[index - 1] && diff[index - 1] >= diff[index] * 1.01
  }

  return data.map((_, index) => ({
    diff: diff[index],
    dea: dea[index],
    macd: macd[index],
    buy: index > 0 && !jjj[index - 1] && jjj[index] ? diff[index] / 0.81 : null,
    sell: index > 0 && !dbjg[index - 1] && dbjg[index] ? diff[index] * 1.31 : null
  }))
}

function signalFigure(key, text, color) {
  return {
    key,
    type: 'text',
    attrs: () => ({ text }),
    styles: () => ({ color, size: 16, weight: 'bold' })
  }
}

function registerMX() {
  window.klinecharts.registerIndicator({
    name: MX_INDICATOR_NAME,
    shortName: 'MX MACD 背离',
    precision: 4,
    calcParams: [12, 26, 9],
    figures: [
      lineFigure('diff', 'DIFF: ', ORANGE),
      lineFigure('dea', 'DEA: ', CYAN),
      {
        key: 'macd',
        title: 'MACD: ',
        type: 'bar',
        baseValue: 0,
        styles: () => ({ style: 'fill', color: PURPLE, borderColor: PURPLE })
      },
      signalFigure('buy', 'B', RED),
      signalFigure('sell', 'S', GREEN)
    ],
    calc: (data, indicator) => {
      const params = indicator.calcParams.map(value => Math.min(500, Math.max(1, Number(value) || 1)))
      return calculateMX(data, params)
    }
  })
}

function readNXConfig() {
  const defaults = { enabled: true, params: [24, 23, 89, 90] }
  try {
    const saved = JSON.parse(localStorage.getItem(NX_STORAGE_KEY))
    if (!saved || !Array.isArray(saved.params) || saved.params.length !== 4) return defaults
    return {
      enabled: saved.enabled !== false,
      params: saved.params.map(value => Math.min(500, Math.max(1, Number(value) || 1)))
    }
  } catch (_) {
    return defaults
  }
}

function writeNXInputs(config) {
  $('nx-enabled').checked = config.enabled
  ;['nx-blue-high', 'nx-blue-low', 'nx-yellow-high', 'nx-yellow-low'].forEach((id, index) => {
    $(id).value = config.params[index]
  })
}

function configFromNXInputs() {
  const params = ['nx-blue-high', 'nx-blue-low', 'nx-yellow-high', 'nx-yellow-low']
    .map(id => Math.min(500, Math.max(1, Number($(id).value) || 1)))
  return { enabled: $('nx-enabled').checked, params }
}

function readMXConfig() {
  const defaults = { enabled: true, params: [12, 26, 9] }
  try {
    const saved = JSON.parse(localStorage.getItem(MX_STORAGE_KEY))
    if (!saved || !Array.isArray(saved.params) || saved.params.length !== 3) return defaults
    return {
      enabled: saved.enabled !== false,
      params: saved.params.map(value => Math.min(500, Math.max(1, Number(value) || 1)))
    }
  } catch (_) {
    return defaults
  }
}

function writeMXInputs(config) {
  $('mx-enabled').checked = config.enabled
  ;['mx-short', 'mx-long', 'mx-signal'].forEach((id, index) => {
    $(id).value = config.params[index]
  })
}

function configFromMXInputs() {
  const params = ['mx-short', 'mx-long', 'mx-signal']
    .map(id => Math.min(500, Math.max(1, Number($(id).value) || 1)))
  return { enabled: $('mx-enabled').checked, params }
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

function localDateTimeValue(date) {
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}

function marketDefaults(symbol) {
  const upper = symbol.toUpperCase()
  if (upper.endsWith('.BINANCE')) return { session: 'continuous', adjustment: 'raw', market: 'Binance Spot' }
  if (upper.endsWith('.HK')) return { session: 'regular', adjustment: 'forward_adjusted', market: '港股' }
  if (upper.endsWith('.SH') || upper.endsWith('.SZ')) return { session: 'regular', adjustment: 'forward_adjusted', market: 'A 股' }
  return { session: 'regular', adjustment: 'split_adjusted', market: '美股' }
}

if (!window.klinecharts) {
  $('error').textContent = 'KLineChart 静态资源加载失败，请重新拉取 go-client 镜像。'
  throw new Error('klinecharts is unavailable')
}

const chart = window.klinecharts.init('chart', {
  locale: 'zh-CN',
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  zoomAnchor: 'cursor',
  styles: {
    grid: { horizontal: { color: '#17352c' }, vertical: { color: '#17352c' } },
    candle: {
      bar: {
        upColor: '#2ac99a', downColor: '#f06f78', noChangeColor: '#91aa9f',
        upBorderColor: '#2ac99a', downBorderColor: '#f06f78', noChangeBorderColor: '#91aa9f',
        upWickColor: '#2ac99a', downWickColor: '#f06f78', noChangeWickColor: '#91aa9f'
      }
    },
    xAxis: { axisLine: { color: '#315649' }, tickLine: { color: '#315649' }, tickText: { color: '#91aa9f' } },
    yAxis: { axisLine: { color: '#315649' }, tickLine: { color: '#315649' }, tickText: { color: '#91aa9f' } },
    separator: { color: '#315649' },
    crosshair: {
      horizontal: { line: { color: '#65d6aa' }, text: { backgroundColor: '#183b30' } },
      vertical: { line: { color: '#65d6aa' }, text: { backgroundColor: '#183b30' } }
    }
  }
})

if (!chart) throw new Error('failed to initialize klinecharts')
chart.setZoomEnabled(true)
chart.setScrollEnabled(true)
chart.setZoomAnchor('cursor')
chart.setOffsetRightDistance(64)
chart.overrideXAxis({ scrollZoomEnabled: true })
chart.overrideYAxis({ paneId: 'candle_pane', scrollZoomEnabled: true })

function stopLive() {
  if (socket) {
    socket.onclose = null
    socket.close()
    socket = null
  }
}

function startLive(symbol, period, callback) {
  stopLive()
  if (periodToInterval(period) !== '1m') return
  const scheme = location.protocol === 'https:' ? 'wss' : 'ws'
  socket = new WebSocket(`${scheme}://${location.host}/v1/live/ws`)
  socket.onopen = () => {
    socket.send(JSON.stringify({ action: 'subscribe', symbols: [symbol], events: ['bar'] }))
    setStatus('实时已连接', 'ok')
  }
  socket.onmessage = event => {
    const payload = JSON.parse(event.data)
    if (payload.type === 'bar' && payload.bar) callback(normalizeBar(payload.bar))
  }
  socket.onerror = () => setStatus('实时连接异常', 'bad')
  socket.onclose = () => {
    socket = null
  }
}

chart.setDataLoader({
  async getBars({ type, symbol, period, callback }) {
    if (type !== 'init' || !activeQuery) {
      callback([], false)
      return
    }
    $('error').textContent = ''
    $('source').textContent = '缓存：加载中'
    try {
      const query = new URLSearchParams({
        interval: periodToInterval(period),
        from: activeQuery.from,
        to: activeQuery.to,
        session: activeQuery.session,
        adjustment: activeQuery.adjustment
      })
      const data = await getJSON(`/v1/bars/${encodeURIComponent(symbol.ticker)}?${query}`)
      const bars = (Array.isArray(data.bars) ? data.bars : []).map(normalizeBar).sort((a, b) => a.timestamp - b.timestamp)
      lastBars = bars
      callback(bars, false)
      $('source').textContent = `缓存：${data.source}`
      $('count').textContent = `Bars：${bars.length}`
      $('updated').textContent = `更新：${new Date().toLocaleTimeString()}`
      if (bars.length) setTimeout(() => chart.scrollToRealTime(), 0)
    } catch (error) {
      callback([], false)
      $('error').textContent = error.message
      $('source').textContent = '缓存：加载失败'
    }
  },
  subscribeBar({ symbol, period, callback }) {
    startLive(symbol.ticker, period, callback)
  },
  unsubscribeBar() {
    stopLive()
  }
})

function applyNX() {
  const config = configFromNXInputs()
  writeNXInputs(config)
  localStorage.setItem(NX_STORAGE_KEY, JSON.stringify(config))
  const existing = chart.getIndicators({ name: 'NX', paneId: 'candle_pane' })
  if (existing.length) {
    chart.overrideIndicator({ name: 'NX', paneId: 'candle_pane', calcParams: config.params, visible: config.enabled })
  } else if (config.enabled) {
    chart.createIndicator({ name: 'NX', paneId: 'candle_pane', calcParams: config.params }, true)
  }
  $('nx-state').textContent = config.enabled
    ? `蓝 ${config.params[0]}/${config.params[1]} · 黄 ${config.params[2]}/${config.params[3]}`
    : 'NX 已隐藏'
}

function applyMX() {
  const config = configFromMXInputs()
  writeMXInputs(config)
  localStorage.setItem(MX_STORAGE_KEY, JSON.stringify(config))
  const existing = chart.getIndicators({ name: MX_INDICATOR_NAME, paneId: MX_PANE_ID })
  if (!config.enabled) {
    if (existing.length) chart.removeIndicator({ name: MX_INDICATOR_NAME, paneId: MX_PANE_ID })
  } else if (existing.length) {
    chart.overrideIndicator({ name: MX_INDICATOR_NAME, paneId: MX_PANE_ID, calcParams: config.params })
  } else {
    chart.createIndicator({ name: MX_INDICATOR_NAME, paneId: MX_PANE_ID, calcParams: config.params })
    chart.setPaneOptions({ id: MX_PANE_ID, height: 190, minHeight: 110 })
  }
  $('mx-state').textContent = config.enabled
    ? `S/P/M ${config.params.join('/')} · B 买点 · S 卖点`
    : 'MX MACD 已隐藏'
}

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
      outputs.push({ name: output[1], color: formulaColor(suffix.find(value => value.startsWith('COLOR') && value !== 'COLORSTICK')), bar: suffix.includes('COLORSTICK') || suffix.includes('VOLSTICK'), hidden: suffix.includes('NODRAW') })
    }
    const drawing = /^(DRAWTEXT|DRAWICON|DRAWNUMBER|STICKLINE|DRAWLINE|POLYLINE|DRAWBAND|DRAWKLINE)\s*\(/i.exec(statement)
    if (drawing) drawings.push({ function: drawing[1].toUpperCase(), color: formulaColor((statement.match(/,\s*(COLOR(?:[0-9A-F]{6}|[A-Z]+))/i) || [])[1]) })
  }
  return { outputs, drawings }
}

function formulaFigures(manifest) {
  const figures = manifest.outputs.filter(output => !output.hidden).map(output => output.bar ? {
    key: `o_${output.name}`, title: `${output.name}: `, type: 'bar', baseValue: 0,
    styles: () => ({ style: 'fill', color: output.color, borderColor: output.color })
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
  const rows = data.map(() => ({}))
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
  const enabled = cloudIndicators.filter(indicator => indicator.enabled).slice(0, 12)
  for (const indicator of enabled) {
    const name = `TDX_${indicator.id}_${indicator.revision}`.replace(/[^A-Za-z0-9_]/g, '_')
    const manifest = formulaManifest(indicator.formula)
    if (!registeredFormulaNames.has(name)) {
      window.klinecharts.registerIndicator({ name, shortName: indicator.name, series: indicator.pane === 'main' ? 'price' : 'normal', precision: 4, figures: formulaFigures(manifest), calc: data => formulaRows(indicator, data) })
      registeredFormulaNames.add(name)
    }
    const paneId = indicator.pane === 'main' ? 'candle_pane' : `tdx_${indicator.id}`
    chart.createIndicator({ name }, indicator.pane === 'main', { id: paneId, height: 190, minHeight: 100 })
    activeFormulaCharts.push({ name, paneId })
  }
  $('indicator-state').textContent = `${enabled.length} 个已启用 · 云端个人配置`
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
  for (const indicator of cloudIndicators) {
    const row = document.createElement('button')
    row.type = 'button'
    row.className = `indicator-item${selectedIndicator?.id === indicator.id ? ' active' : ''}`
    row.innerHTML = `<input type="checkbox" ${indicator.enabled ? 'checked' : ''} aria-label="启用"><span>${escapeHTML(indicator.name)}</span><em>${indicator.kind === 'template' ? '内置' : '个人'} · ${indicator.pane === 'main' ? '主图' : '副图'}</em>`
    row.querySelector('input').addEventListener('click', async event => {
      event.stopPropagation()
      try {
        const updated = await getJSON(`/v1/me/indicators/${indicator.id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...indicator, enabled: event.target.checked }) })
        cloudIndicators = cloudIndicators.map(item => item.id === updated.id ? updated : item)
        if (selectedIndicator?.id === updated.id) selectIndicator(updated)
        await applyFormulaIndicators(); renderIndicatorList(); cacheIndicators()
      } catch (error) { $('indicator-error').textContent = error.message; event.target.checked = indicator.enabled }
    })
    row.addEventListener('click', () => selectIndicator(indicator))
    $('indicator-list').appendChild(row)
  }
}

function indicatorCacheKey() { return `market-bridge:formula-indicators:${indicatorUserID}` }
function cacheIndicators() { localStorage.setItem(indicatorCacheKey(), JSON.stringify(cloudIndicators)) }

async function migrateLegacyIndicatorSettings() {
  const migrationKey = `${INDICATOR_MIGRATION_KEY}:${indicatorUserID}`
  if (localStorage.getItem(migrationKey)) return
  const migrations = [[NX_STORAGE_KEY, 'nx-v1'], [MX_STORAGE_KEY, 'mx-macd-v1']]
  for (const [storageKey, templateKey] of migrations) {
    let legacy
    try { legacy = JSON.parse(localStorage.getItem(storageKey)) } catch (_) { legacy = null }
    const template = cloudIndicators.find(item => item.template_key === templateKey)
    if (!template || !legacy || !Array.isArray(legacy.params) || legacy.params.length !== template.parameters.length) continue
    const mutation = { ...template, enabled: legacy.enabled !== false, parameters: template.parameters.map((parameter, index) => ({ ...parameter, value: Number(legacy.params[index]) })) }
    const updated = await getJSON(`/v1/me/indicators/${template.id}`, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(mutation) })
    cloudIndicators = cloudIndicators.map(item => item.id === updated.id ? updated : item)
  }
  localStorage.setItem(migrationKey, '1')
}

async function loadIndicators() {
  try {
    const me = await getJSON('/v1/me')
    indicatorUserID = me.id || me.name || 'unknown'
    localStorage.setItem('market-bridge:formula-indicators:user', indicatorUserID)
    const response = await getJSON('/v1/me/indicators')
    cloudIndicators = response.indicators || []
    await migrateLegacyIndicatorSettings()
    cacheIndicators()
  } catch (error) {
    try { cloudIndicators = JSON.parse(localStorage.getItem(indicatorCacheKey())) || [] } catch (_) { cloudIndicators = [] }
    $('indicator-state').textContent = cloudIndicators.length ? '云端不可用，使用上次缓存' : '指标配置不可用'
  }
  renderIndicatorList()
  if (cloudIndicators.length) selectIndicator(cloudIndicators[0])
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

const now = new Date()
const past = new Date(now.getTime() - 180 * 24 * 3600 * 1000)
$('from').value = localDateTimeValue(past)
$('to').value = localDateTimeValue(now)

$('manage-indicators').addEventListener('click', () => { $('indicator-manager').hidden = !$('indicator-manager').hidden })
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
    cloudIndicators = id ? cloudIndicators.map(item => item.id === saved.id ? saved : item) : [...cloudIndicators, saved]
    cacheIndicators(); selectIndicator(saved); await applyFormulaIndicators()
    $('indicator-error').textContent = '已保存到当前账号'
  } catch (error) { $('indicator-error').textContent = error.message }
})
$('copy-indicator').addEventListener('click', async () => {
  if (!selectedIndicator) return
  try {
    const copy = await getJSON(`/v1/me/indicators/${selectedIndicator.id}/copy`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({}) })
    cloudIndicators.push(copy); cacheIndicators(); selectIndicator(copy); await applyFormulaIndicators()
  } catch (error) { $('indicator-error').textContent = error.message }
})
$('delete-indicator').addEventListener('click', async () => {
  if (!selectedIndicator || selectedIndicator.kind === 'template' || !confirm(`删除指标“${selectedIndicator.name}”？`)) return
  try {
    await getJSON(`/v1/me/indicators/${selectedIndicator.id}?revision=${selectedIndicator.revision}`, { method: 'DELETE' })
    cloudIndicators = cloudIndicators.filter(item => item.id !== selectedIndicator.id); cacheIndicators(); selectIndicator(cloudIndicators[0] || null); await applyFormulaIndicators()
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

$('query').addEventListener('submit', event => {
  event.preventDefault()
  $('error').textContent = ''
  try {
    const symbol = $('symbol').value.trim().toUpperCase()
    if (!symbol) throw new Error('请输入股票代码')
    const from = new Date($('from').value)
    const to = new Date($('to').value)
    if (!Number.isFinite(from.getTime()) || !Number.isFinite(to.getTime()) || from >= to) throw new Error('请选择有效的开始和结束时间')
    const interval = $('interval').value
    const defaults = marketDefaults(symbol)
    activeQuery = { from: from.toISOString(), to: to.toISOString(), interval, session: defaults.session, adjustment: defaults.adjustment }
    $('source').textContent = `市场：${defaults.market} · 加载中`
    chart.setPeriod(intervalToPeriod(interval))
	const crypto = symbol.endsWith('.BINANCE')
	chart.setSymbol({ ticker: symbol, pricePrecision: crypto ? 8 : 4, volumePrecision: crypto ? 8 : 0 })
    chart.resetData()
  } catch (error) {
    $('error').textContent = error.message
  }
})

resetFormulaWorker()
loadIndicators().catch(error => { $('indicator-state').textContent = error.message })
refreshAccount()
setInterval(refreshAccount, 10000)
$('query').requestSubmit()
