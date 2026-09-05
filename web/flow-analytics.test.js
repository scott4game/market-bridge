const assert = require('node:assert/strict')
const fs = require('node:fs')
const path = require('node:path')
const flow = require('../internal/localclient/ui/flow-analytics.js')

const merged = flow.mergeIntoBars(
  [{ timestamp: Date.parse('2026-09-03T14:30:00Z'), close: 10 }],
  [{ timestamp: '2026-09-03T14:30:00Z', volume_ratio: '1.250000', turnover_ratio: '0.900000', baseline_complete: true }],
  [{ timestamp: '2026-09-03T14:30:00Z', inflow: '750.000000', outflow: '250.000000', net_flow: '500.000000', flow_ratio: '0.500000' }]
)
assert.equal(merged[0].volumeRatio, 1.25)
assert.equal(merged[0].netFlow, 500)
assert.equal(merged[0].baselineComplete, true)

const sectors = flow.sortSectors([
  { sic_code: '2000', net_flow: '100', flow_ratio: '0.1' },
  { sic_code: '1000', net_flow: '50', flow_ratio: '0.2' }
])
assert.deepEqual(sectors.map(item => item.code), ['2000', '1000'])
assert.deepEqual(flow.sortSectors(sectors.map(item => ({ sic_code: item.code, net_flow: item.netFlow, flow_ratio: item.flowRatio })), 'ratio').map(item => item.code), ['1000', '2000'])

const html = fs.readFileSync(path.join(__dirname, '../internal/localclient/ui/index.html'), 'utf8')
for (const id of ['funds-state', 'fund-volume-ratio', 'sector-flow-rows', 'flow-basket-select', 'volume-energy-pane', 'flow-proxy-pane']) {
  assert.match(html, new RegExp(`id="${id}"`))
}
assert.ok(html.indexOf('/flow-analytics.js') < html.indexOf('/app.js'))

console.log('flow analytics tests passed')
