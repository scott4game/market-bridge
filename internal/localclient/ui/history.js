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

  function twoYearFloor(timestamp) {
    const date = new Date(timestamp)
    date.setUTCFullYear(date.getUTCFullYear() - 2)
    return date.getTime()
  }

  function chunkSpan(interval) {
    return Math.min((INTERVAL_MS[interval] || INTERVAL_MS['1d']) * MAX_BARS_PER_REQUEST, MAX_HISTORY_REQUEST_MS)
  }

  function initialRange(to, interval) {
    const floor = twoYearFloor(to)
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

  function allowHistoryBeyondTwoYears(symbol, providers) {
    const upper = String(symbol || '').toUpperCase()
	if (upper.startsWith('I:') || upper.endsWith('.HK') || upper.endsWith('.SH') || upper.endsWith('.SZ') || upper.endsWith('.BINANCE')) return true
	const massive = providers && providers.massive
	const plan = String((massive && massive.plan) || '').toLowerCase()
	return Boolean(massive && massive.state === 'enabled' && plan && plan !== 'stocks_basic')
  }

  return {
    MAX_BARS_PER_REQUEST,
    MAX_HISTORY_REQUEST_MS,
    twoYearFloor,
    chunkSpan,
    initialRange,
    olderRange,
    canRequestOlder,
    mergeBars,
    isCurrentQuery,
    allowHistoryBeyondTwoYears
  }
})
