const assert = require('node:assert/strict')
const live = require('../internal/localclient/ui/live-market.js')

const pascal = live.normalizeTrade({ Price: '101.25', Volume: 40, Timestamp: 1770000000, TradeType: 'F', Direction: 2, TradeSession: 1 })
assert.deepEqual(pascal, { price: '101.25', volume: 40, timestamp: 1770000000000, tradeType: 'F', direction: 2, tradeSession: 1 })

const snake = live.normalizeTrade({ price: '99.5', volume: 8, timestamp: '2026-02-02T02:40:01Z', trade_type: '', direction: 1, trade_session: 0 })
assert.equal(snake.timestamp, Date.parse('2026-02-02T02:40:01Z'))
assert.equal(snake.direction, 1)

const depth = live.normalizeDepth({
  Ask: Array.from({ length: 12 }, (_, index) => ({ Position: index + 1, Price: String(112 - index), Volume: 10, OrderNum: index + 2 })),
  Bid: [{ position: 2, price: '99', volume: 30, order_num: 3 }, { position: 1, price: '100', volume: 20, order_num: 2 }]
})
assert.equal(depth.asks.length, 10)
assert.deepEqual(depth.asks.map(row => row.position), [10, 9, 8, 7, 6, 5, 4, 3, 2, 1])
assert.deepEqual(depth.bids.map(row => row.position), [1, 2])
assert.equal(depth.bids[0].orderNum, 2)

const quote = live.normalizeQuote({ last_done: '103', prev_close: '100', change: '3', change_percent: '3', trade_session: 'post', source: 'longbridge' })
assert.deepEqual(quote, { lastDone: 103, prevClose: 100, change: 3, changePercent: 3, tradeSession: 'post', source: 'longbridge' })
const incompleteQuote = live.normalizeQuote({ last_done: '103', trade_session: 'regular' })
assert.equal(incompleteQuote.lastDone, 103)
assert.equal(incompleteQuote.changePercent, null)

const initial = [
  { price: '10', volume: 1, timestamp: 100, trade_type: '', direction: 0, trade_session: 0 },
  { price: '10', volume: 1, timestamp: 100, trade_type: '', direction: 0, trade_session: 0 }
]
const buffered = [
  { Price: '10', Volume: 1, Timestamp: 100, TradeType: '', Direction: 0, TradeSession: 0 },
  { Price: '11', Volume: 2, Timestamp: 101, TradeType: 'F', Direction: 2, TradeSession: 0 }
]
const merged = live.mergeInitialAndBuffered(initial, buffered, 100)
assert.equal(merged.length, 3)
assert.equal(merged[0].price, '11')
assert.equal(merged.filter(row => row.price === '10').length, 2)

console.log('live market tests passed')
