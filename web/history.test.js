const assert = require('node:assert/strict')
const history = require('../internal/localclient/ui/history.js')

const to = Date.parse('2026-08-26T12:00:00Z')
const floor = Date.parse('2024-08-26T12:00:00Z')

assert.equal(history.twoYearFloor(to), floor)
assert.equal(history.initialRange(to, '1d').to - history.initialRange(to, '1d').from, history.MAX_HISTORY_REQUEST_MS)

const minute = history.initialRange(to, '1m')
assert.equal(minute.to - minute.from, 5000 * 60 * 1000)
assert.ok(minute.from > floor)

assert.deepEqual(history.olderRange(floor + 60_000, '1m', floor, false), { from: floor, to: floor + 60_000 })
assert.equal(history.olderRange(floor, '1m', floor, false), null)
const upgraded = history.olderRange(floor, '1d', floor, true)
assert.ok(upgraded.from < floor)
assert.equal(upgraded.to - upgraded.from, history.MAX_HISTORY_REQUEST_MS)

assert.equal(history.canRequestOlder(floor, floor, false, 1), false)
assert.equal(history.canRequestOlder(floor, floor, true, 1), true)
assert.equal(history.canRequestOlder(floor, floor, true, 0), false)
assert.equal(history.allowHistoryBeyondTwoYears('AAPL', { massive: { state: 'enabled', plan: 'stocks_basic' } }), false)
assert.equal(history.allowHistoryBeyondTwoYears('AAPL', { massive: { state: 'enabled', plan: 'stocks_advanced' } }), true)
assert.equal(history.allowHistoryBeyondTwoYears('700.HK', null), true)

assert.deepEqual(history.mergeBars(
  [{ timestamp: 2, close: 2 }, { timestamp: 3, close: 3 }],
  [{ timestamp: 1, close: 1 }, { timestamp: 2, close: 20 }]
), [
  { timestamp: 1, close: 1 },
  { timestamp: 2, close: 20 },
  { timestamp: 3, close: 3 }
])

const activeQuery = { generation: 7, symbol: 'AAPL', interval: '1d' }
assert.equal(history.isCurrentQuery(activeQuery, 7, 'AAPL', '1d'), true)
assert.equal(history.isCurrentQuery(activeQuery, 6, 'AAPL', '1d'), false)
assert.equal(history.isCurrentQuery(activeQuery, 7, 'MSFT', '1d'), false)
assert.equal(history.isCurrentQuery(activeQuery, 7, 'AAPL', '1h'), false)
assert.equal(history.isCurrentQuery(null, 7, 'AAPL', '1d'), false)

console.log('history policy tests passed')
