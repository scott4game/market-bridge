(function (root, factory) {
  const api = factory()
  if (typeof module === 'object' && module.exports) module.exports = api
  else root.liveMarketUtils = api
})(typeof self !== 'undefined' ? self : this, function () {
  function field(value, snake, pascal) {
    if (!value || typeof value !== 'object') return undefined
    return value[snake] !== undefined ? value[snake] : value[pascal]
  }

  function normalizeTimestamp(value) {
    if (typeof value === 'string' && !/^\d+$/.test(value)) return Date.parse(value)
    const numeric = Number(value)
    if (!Number.isFinite(numeric)) return 0
    return numeric < 1e12 ? numeric * 1000 : numeric
  }

  function normalizeTrade(value) {
    return {
      price: String(field(value, 'price', 'Price') ?? ''),
      volume: Number(field(value, 'volume', 'Volume') ?? 0),
      timestamp: normalizeTimestamp(field(value, 'timestamp', 'Timestamp')),
      tradeType: String(value?.tradeType ?? field(value, 'trade_type', 'TradeType') ?? ''),
      direction: Number(field(value, 'direction', 'Direction') ?? 0),
      tradeSession: Number(value?.tradeSession ?? field(value, 'trade_session', 'TradeSession') ?? 0)
    }
  }

  function normalizeTrades(value) {
    const rows = Array.isArray(value) ? value : Array.isArray(value?.trades) ? value.trades : []
    return rows.map(normalizeTrade).filter(row => row.price && row.timestamp > 0)
  }

  function normalizeLevel(value) {
    return {
      position: Number(field(value, 'position', 'Position') ?? 0),
      price: String(field(value, 'price', 'Price') ?? ''),
      volume: Number(field(value, 'volume', 'Volume') ?? 0),
      orderNum: Number(field(value, 'order_num', 'OrderNum') ?? 0)
    }
  }

  function normalizeDepth(value, limit = 10) {
    const asks = (value?.ask || value?.Ask || []).map(normalizeLevel).filter(row => row.price).sort((a, b) => b.position - a.position).slice(-limit)
    const bids = (value?.bid || value?.Bid || []).map(normalizeLevel).filter(row => row.price).sort((a, b) => a.position - b.position).slice(0, limit)
    return { asks, bids }
  }

  function tradeKey(row) {
    return [row.timestamp, row.price, row.volume, row.tradeType, row.direction, row.tradeSession].join('|')
  }

  function mergeInitialAndBuffered(initial, buffered, limit = 100) {
    const snapshot = normalizeTrades(initial)
    const pushed = normalizeTrades(buffered)
    const overlap = new Map()
    for (const row of snapshot) {
      const key = tradeKey(row)
      overlap.set(key, (overlap.get(key) || 0) + 1)
    }
    const additions = []
    for (const row of pushed) {
      const key = tradeKey(row)
      const remaining = overlap.get(key) || 0
      if (remaining > 0) overlap.set(key, remaining - 1)
      else additions.push(row)
    }
    return snapshot.concat(additions).sort((a, b) => b.timestamp - a.timestamp).slice(0, limit)
  }

  return { normalizeTrade, normalizeTrades, normalizeDepth, tradeKey, mergeInitialAndBuffered }
})
