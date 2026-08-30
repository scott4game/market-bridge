(function (root, factory) {
  const api = factory()
  if (typeof module === 'object' && module.exports) module.exports = api
  if (root) root.marketHistory = api
})(typeof window !== 'undefined' ? window : globalThis, function () {
  const MAX_BARS_PER_REQUEST = 5000
  const MAX_HISTORY_REQUEST_MS = 366 * 24 * 60 * 60 * 1000
  const INTERVAL_MS = {
    '1m': 60 * 1000,
    '3m': 3 * 60 * 1000,
    '5m': 5 * 60 * 1000,
    '10m': 10 * 60 * 1000,
    '15m': 15 * 60 * 1000,
    '30m': 30 * 60 * 1000,
    '1h': 60 * 60 * 1000,
    '2h': 2 * 60 * 60 * 1000,
    '3h': 3 * 60 * 60 * 1000,
    '4h': 4 * 60 * 60 * 1000,
    '1d': 24 * 60 * 60 * 1000,
    '1w': 7 * 24 * 60 * 60 * 1000,
    '1mo': 31 * 24 * 60 * 60 * 1000,
    '1y': 366 * 24 * 60 * 60 * 1000
  }

  function historyFloor(timestamp, years) {
    const date = new Date(timestamp)
    date.setUTCFullYear(date.getUTCFullYear() - Math.max(1, Number(years) || 5))
    return date.getTime()
  }

  function twoYearFloor(timestamp) { return historyFloor(timestamp, 2) }

  function chunkSpan(interval) {
    return Math.min((INTERVAL_MS[interval] || INTERVAL_MS['1d']) * MAX_BARS_PER_REQUEST, MAX_HISTORY_REQUEST_MS)
  }

  function automaticHistoryInterval(interval) {
    return ['1h', '2h', '3h', '4h', '1d', '1w', '1mo', '1y'].includes(interval)
  }

  function initialRange(to, interval, maxYears = 5) {
    const floor = historyFloor(to, maxYears)
    if (automaticHistoryInterval(interval)) return { from: floor, to, floor }
    return { from: Math.max(floor, to - chunkSpan(interval)), to, floor }
  }

  function olderRange(upper, interval, floor, allowBeyondFloor) {
    if (!allowBeyondFloor && upper <= floor) return null
    let from = upper - chunkSpan(interval)
    if (!allowBeyondFloor) from = Math.max(from, floor)
    if (from >= upper) return null
    return { from, to: upper }
  }

  function canRequestOlder(rangeFrom, floor, allowBeyondFloor, receivedBars) {
    return receivedBars > 0 && (allowBeyondFloor || rangeFrom > floor)
  }

  function mergeBars(current, incoming) {
    const byTimestamp = new Map()
    for (const bar of current || []) byTimestamp.set(bar.timestamp, bar)
    for (const bar of incoming || []) byTimestamp.set(bar.timestamp, bar)
    return [...byTimestamp.values()].sort((a, b) => a.timestamp - b.timestamp)
  }

  function isCurrentQuery(query, generation, symbol, interval) {
    return Boolean(
      query &&
      query.generation === generation &&
      query.symbol === symbol &&
      query.interval === interval
    )
  }

  function chartQueryChanges(currentSymbol, currentPeriod, nextSymbol, nextPeriod) {
    const currentTicker = String(currentSymbol?.ticker || '')
    const nextTicker = String(nextSymbol?.ticker || '')
    const samePeriod = String(currentPeriod?.type || '') === String(nextPeriod?.type || '') &&
      Number(currentPeriod?.span) === Number(nextPeriod?.span)
    return {
      symbol: currentTicker !== nextTicker,
      period: !samePeriod
    }
  }

  function historyYearsFor(symbol, providers) {
    const upper = String(symbol || '').toUpperCase()
    const routes = providers?.history_policy?.routes || {}
    let route = 'us'
    if (upper.endsWith('.BINANCE')) route = 'binance'
    else if (upper.endsWith('.HK')) route = 'hk'
    else if (upper.endsWith('.SH') || upper.endsWith('.SZ')) route = 'ashare'
    else if (upper.startsWith('I:')) route = 'index'
    const years = Number(routes[route]?.max_years)
    return Number.isInteger(years) && years > 0 ? years : 5
  }

  return {
    MAX_BARS_PER_REQUEST,
    MAX_HISTORY_REQUEST_MS,
    historyFloor,
    twoYearFloor,
    automaticHistoryInterval,
    chunkSpan,
    initialRange,
    olderRange,
    canRequestOlder,
    mergeBars,
    isCurrentQuery,
    chartQueryChanges,
    historyYearsFor
  }
})
