const assert = require('node:assert/strict')

let response
global.self = { postMessage: message => { response = message } }
require('../internal/localclient/ui/formula-worker.js')

function invoke(type, formula, parameters, bars = []) {
  response = null
  self.onmessage({ data: { id: 1, type, formula, parameters, bars } })
  assert(response, 'worker did not respond')
  if (!response.ok) throw new Error(response.error)
  return response.result
}

const bars = Array.from({ length: 240 }, (_, index) => ({
  timestamp: Date.UTC(2025, 0, index + 1),
  open: 100 + index / 20,
  high: 102 + index / 20,
  low: 98 + index / 20,
  close: 100 + index / 20 + Math.sin(index / 8) * 9,
  volume: 1000 + index
}))

const nx = `A:EMA(HIGH,BLUE_HIGH),COLORBLUE;
B:EMA(LOW,BLUE_LOW),COLORBLUE;
STICKLINE(C>A,A,B,0.1,1),COLORBLUE;
STICKLINE(C<B,A,B,0.1,1),COLORBLUE;`
const nxResult = invoke('evaluate', nx, [
  { name: 'BLUE_HIGH', value: 24 },
  { name: 'BLUE_LOW', value: 23 }
], bars)
assert.deepEqual(nxResult.outputs.map(output => output.name), ['A', 'B'])
assert(nxResult.drawings.length > 0)

const mx = `DIFF:EMA(CLOSE,S)-EMA(CLOSE,P),COLORFF8D1E;
DEA:EMA(DIFF,M),COLOR0CAEE6;
MACD:(DIFF-DEA)*2,COLORSTICK,COLORE970DC;
N1:=BARSLAST(REF(MACD,1)>=0 AND MACD<0);
M1:=BARSLAST(REF(MACD,1)<=0 AND MACD>0);
CC1:=LLV(CLOSE,N1+1); CC2:=REF(CC1,M1+1);
DIFL1:=LLV(DIFF,N1+1); DIFL2:=REF(DIFL1,M1+1);
AAA:=CC1<CC2 AND DIFL1>DIFL2 AND REF(MACD,1)<0 AND DIFF<0;
DRAWTEXT(AAA,DIFF/0.81,'B'),COLORRED,LINETHICK3;`
const mxResult = invoke('evaluate', mx, [
  { name: 'S', value: 12 }, { name: 'P', value: 26 }, { name: 'M', value: 9 }
], bars)
assert.deepEqual(mxResult.outputs.map(output => output.name), ['DIFF', 'DEA', 'MACD'])

const analysis = invoke('compile', 'X:REFX(CLOSE,N);', [])
assert.deepEqual(analysis.missing, ['N'])
assert.match(analysis.warnings[0], /未来函数/)

const future = invoke('evaluate', 'X:BACKSET(CLOSE>105,3);', [], bars)
assert.match(future.warnings[0], /BACKSET/)
assert.equal(future.outputs[0].data.length, bars.length)

const zig = invoke('evaluate', 'Z:ZIG(3,5); P:PEAK(3,5,1);', [], bars)
assert.match(zig.warnings[0], /ZIG/)
assert.equal(zig.outputs[0].data.length, bars.length)

response = null
self.onmessage({ data: { id: 2, type: 'compile', formula: 'X:FINANCE(1);', parameters: [] } })
assert.equal(response.ok, false)
assert.match(response.error, /外部数据/)
