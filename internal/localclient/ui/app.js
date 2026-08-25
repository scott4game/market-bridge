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

function setStatus(text, state = '') {
  $('status').textContent = text
  $('status').className = state
}

async function getJSON(path, options) {
  const response = await fetch(path, options)
  const data = await response.json()
  if (!response.ok) throw new Error(data.error || response.statusText)
  return data
}

async function refreshAccount() {
  try {
    const [me, usage, providers, watchlist] = await Promise.all([
      getJSON('/v1/me'),
      getJSON('/v1/me/usage'),
      getJSON('/v1/providers/status'),
      getJSON('/v1/me/watchlist')
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
    volume: Number(bar.volume || 0)
  }
  if (bar.turnover !== undefined && bar.turnover !== null) normalized.turnover = Number(bar.turnover)
  return normalized
}

function intervalToPeriod(interval) {
  const match = /^(\d+)(m|h|d)$/.exec(interval)
  if (!match) return { type: 'day', span: 1 }
  const types = { m: 'minute', h: 'hour', d: 'day' }
  return { type: types[match[2]], span: Number(match[1]) }
}

function periodToInterval(period) {
  const suffixes = { minute: 'm', hour: 'h', day: 'd' }
  return `${period.span}${suffixes[period.type] || 'd'}`
}

function localDateTimeValue(date) {
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000).toISOString().slice(0, 16)
}

if (!window.klinecharts) {
  $('error').textContent = 'KLineChart 静态资源加载失败，请重新拉取 go-client 镜像。'
  throw new Error('klinecharts is unavailable')
}

registerNX()
registerMX()
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
        session: 'regular',
        adjustment: 'split_adjusted'
      })
      const data = await getJSON(`/v1/bars/${encodeURIComponent(symbol.ticker)}?${query}`)
      const bars = (Array.isArray(data.bars) ? data.bars : []).map(normalizeBar).sort((a, b) => a.timestamp - b.timestamp)
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

const nxConfig = readNXConfig()
writeNXInputs(nxConfig)
applyNX()
const mxConfig = readMXConfig()
writeMXInputs(mxConfig)
applyMX()

const now = new Date()
const past = new Date(now.getTime() - 180 * 24 * 3600 * 1000)
$('from').value = localDateTimeValue(past)
$('to').value = localDateTimeValue(now)

$('apply-nx').addEventListener('click', applyNX)
$('nx-enabled').addEventListener('change', applyNX)
$('apply-mx').addEventListener('click', applyMX)
$('mx-enabled').addEventListener('change', applyMX)
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
    activeQuery = { from: from.toISOString(), to: to.toISOString(), interval }
    chart.setPeriod(intervalToPeriod(interval))
    chart.setSymbol({ ticker: symbol, pricePrecision: 4, volumePrecision: 0 })
    chart.resetData()
  } catch (error) {
    $('error').textContent = error.message
  }
})

refreshAccount()
setInterval(refreshAccount, 10000)
$('query').requestSubmit()
