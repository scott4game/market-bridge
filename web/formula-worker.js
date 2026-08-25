import { FormulaEngine } from './vendor/formula-ts/src/FormulaEngine'

const MAX_FORMULA_BYTES = 64 * 1024
const MAX_BARS = 250000
const MAX_DRAWING_STATEMENTS = 32
const MAX_DRAWING_EVENTS = 500000

const MARKET_FIELDS = new Set([
  'OPEN', 'O', 'HIGH', 'H', 'LOW', 'L', 'CLOSE', 'C', 'VOL', 'VOLUME', 'V',
  'AMOUNT', 'AMO', 'TIMESTAMP', 'DATE', 'TIME', 'YEAR', 'MONTH', 'DAY', 'HOUR',
  'MINUTE', 'WEEKDAY', 'PERIOD', 'BARSCOUNT', 'CURRBARSCOUNT', 'TOTALBARSCOUNT',
  'ISLASTBAR', 'BARSTATUS', 'DRAWNULL', 'ADVANCE', 'DECLINE', 'TRADABLESHARES'
])

const KNOWN_FUNCTIONS = new Set(`
MA EMA SUM MAX MIN ABS SQRT POW EXP LN LOG MOD CEILING FLOOR INTPART FRACPART ROUND ROUND2 SIGN SIN COS TAN ASIN ACOS ATAN
REF REFV REFX REFXV BACKSET ZIG PEAK PEAKBARS TROUGH TROUGHBARS HHV LLV HHVBARS LLVBARS IF IFF IFN CROSS LONGCROSS EVERY EXIST BARSLAST BARSLASTCOUNT COUNT FILTER NOT
STD STDP STDDEV VAR VARP DEVSQ FORCAST SLOPE COVAR RELATE BETA MEDIAN AVEDEV SMA WMA DMA CONST RSI UPNDAY DOWNNDAY NDAY LAST EXISTR RANGE BETWEEN
WINNER LWINNER COST VALUEWHEN TOPRANGE LOWRANGE MACD_DIF MACD_DEA MACD_MACD KDJ_K KDJ_D KDJ_J SAR CCI DMI_PDI DMI_MDI DMI_ADX DMI_ADXR ADX ADXR TRIX OBV BIAS ROC MTM WR PSY
OPEN HIGH LOW CLOSE VOL AMOUNT ADVANCE DECLINE DATE TIME YEAR MONTH DAY HOUR MINUTE WEEKDAY PERIOD BARSCOUNT CURRBARSCOUNT TOTALBARSCOUNT ISLASTBAR BARSTATUS BARSSINCE SUMBARS
DRAWTEXT DRAWICON DRAWNUMBER STICKLINE DRAWLINE POLYLINE DRAWBAND DRAWKLINE
`.trim().split(/\s+/))

const DRAWING_FUNCTIONS = new Set(['DRAWTEXT', 'DRAWICON', 'DRAWNUMBER', 'STICKLINE', 'DRAWLINE', 'POLYLINE', 'DRAWBAND', 'DRAWKLINE'])
const FUTURE_FUNCTIONS = new Set(['REFX', 'REFXV', 'BACKSET', 'ZIG', 'PEAK', 'PEAKBARS', 'TROUGH', 'TROUGHBARS'])
const EXTERNAL_DATA_FUNCTIONS = new Set(['FINANCE', 'DYNAINFO', 'EXTERNVALUE', 'EXTDATA_USER', 'GPJYVALUE', 'BLOCKSETNUM', 'HORCALC'])
const RESERVED_WORDS = new Set(['AND', 'OR', 'NOT', 'IF', 'TRUE', 'FALSE'])

function splitStatements(source) {
  const statements = []
  let start = 0
  let quote = ''
  let comment = false
  for (let index = 0; index < source.length; index++) {
    const char = source[index]
    if (comment) {
      if (char === '}') comment = false
      continue
    }
    if (quote) {
      if (char === '\\') index++
      else if (char === quote) quote = ''
      continue
    }
    if (char === '{') comment = true
    else if (char === "'" || char === '"') quote = char
    else if (char === ';') {
      const statement = source.slice(start, index).trim()
      if (statement) statements.push(statement)
      start = index + 1
    }
  }
  const trailing = source.slice(start).trim()
  if (trailing) statements.push(trailing)
  return statements
}

function stripCommentsAndStrings(source) {
  return source
    .replace(/\{[\s\S]*?\}/g, ' ')
    .replace(/\/\/[^\n]*/g, ' ')
    .replace(/'(?:\\.|[^'\\])*'|"(?:\\.|[^"\\])*"/g, ' ')
}

function styleFromSuffix(suffix) {
  const style = {}
  for (const raw of suffix.split(',').map(value => value.trim().toUpperCase()).filter(Boolean)) {
    if (/^COLOR[0-9A-F]{6}$/.test(raw)) style.color = `#${raw.slice(5)}`
    else if (raw.startsWith('COLOR')) style.color = raw
    else if (/^LINETHICK[1-9]$/.test(raw)) style.lineWidth = Number(raw.slice(-1))
    else if (raw === 'COLORSTICK' || raw === 'VOLSTICK' || raw === 'STICK') style.drawMethod = raw
    else if (raw === 'NODRAW') style.hidden = true
    else if (raw === 'DOTLINE' || raw === 'DASHLINE') style.lineStyle = raw
  }
  return style
}

function drawingStatement(statement) {
  const cleaned = statement.replace(/^\s*(?:\{[\s\S]*?\}\s*)+/, '')
  const match = /^([\p{L}_][\p{L}\p{N}_]*)\s*\(/u.exec(cleaned)
  if (!match || !DRAWING_FUNCTIONS.has(match[1].toUpperCase())) return null
  let quote = ''
  let depth = 0
  let end = -1
  for (let index = cleaned.indexOf('('); index < cleaned.length; index++) {
    const char = cleaned[index]
    if (quote) {
      if (char === '\\') index++
      else if (char === quote) quote = ''
      continue
    }
    if (char === "'" || char === '"') quote = char
    else if (char === '(') depth++
    else if (char === ')' && --depth === 0) {
      end = index
      break
    }
  }
  if (end < 0) throw new Error(`${match[1]} 缺少右括号`)
  const suffix = cleaned.slice(end + 1).trim().replace(/^,/, '')
  return { function: match[1].toUpperCase(), expression: cleaned.slice(0, end + 1), style: styleFromSuffix(suffix) }
}

function preprocess(source) {
  if (new TextEncoder().encode(source).length > MAX_FORMULA_BYTES) throw new Error('公式超过 64 KiB 限制')
  const core = []
  const drawings = []
  for (const statement of splitStatements(source)) {
    const drawing = drawingStatement(statement)
    if (drawing) drawings.push(drawing)
    else core.push(statement)
  }
  if (drawings.length > MAX_DRAWING_STATEMENTS) throw new Error('绘图语句不能超过 32 条')
  return { core: core.map(value => `${value};`).join('\n'), drawings }
}

function analyzeIdentifiers(source) {
  const clean = stripCommentsAndStrings(source)
  const declared = new Set()
  for (const match of clean.matchAll(/([\p{L}_][\p{L}\p{N}_]*)\s*(?::=|:(?!=))/gu)) declared.add(match[1].toUpperCase())
  const calls = new Set()
  for (const match of clean.matchAll(/([\p{L}_][\p{L}\p{N}_]*)\s*\(/gu)) calls.add(match[1].toUpperCase())
  const external = [...calls].filter(name => EXTERNAL_DATA_FUNCTIONS.has(name))
  if (external.length) throw new Error(`公式需要当前 K 线未提供的外部数据：${external.join(', ')}；不能保存`)
  const unknownFunctions = [...calls].filter(name => !KNOWN_FUNCTIONS.has(name))
  if (unknownFunctions.length) throw new Error(`不支持的函数：${unknownFunctions.join(', ')}`)
  const parameters = new Set()
  for (const match of clean.matchAll(/[\p{L}_][\p{L}\p{N}_]*/gu)) {
    const name = match[0].toUpperCase()
    if (declared.has(name) || calls.has(name) || MARKET_FIELDS.has(name) || RESERVED_WORDS.has(name) || name.startsWith('COLOR') || name.startsWith('LINETHICK') || ['NODRAW', 'DOTLINE', 'DASHLINE', 'VOLSTICK', 'STICK'].includes(name)) continue
    parameters.add(name)
  }
  const future = [...calls].filter(name => FUTURE_FUNCTIONS.has(name))
  return { parameters: [...parameters], warnings: future.length ? [`含未来函数：${future.join(', ')}；历史信号可能重绘`] : [] }
}

function parameterPrefix(parameters) {
  return (parameters || []).map(parameter => {
    const name = String(parameter.name || '').toUpperCase()
    const value = Number(parameter.value)
    if (!/^[\p{L}_][\p{L}\p{N}_]*$/u.test(name) || !Number.isFinite(value)) throw new Error(`参数 ${name || '?'} 无效`)
    return `${name}:=${value};`
  }).join('\n')
}

function compile(source, parameters = []) {
  const prepared = preprocess(source)
  const analysis = analyzeIdentifiers(source)
  const supplied = new Set(parameters.map(parameter => String(parameter.name).toUpperCase()))
  const missing = analysis.parameters.filter(name => !supplied.has(name))
  const prefix = parameterPrefix(parameters)
  const engine = new FormulaEngine()
  engine.parse(`${prefix}\n${prepared.core}`)
  for (const drawing of prepared.drawings) engine.parse(`${prefix}\n${prepared.core}\n${drawing.expression};`)
  return { prepared, ...analysis, missing }
}

function normalizeMarketData(bars) {
  if (!Array.isArray(bars) || bars.length === 0) throw new Error('没有可用于预览的 K 线数据')
  if (bars.length > MAX_BARS) throw new Error('单次计算不能超过 250000 根 K 线')
  return bars.map(bar => ({
    timestamp: Number(bar.timestamp), open: Number(bar.open), high: Number(bar.high), low: Number(bar.low), close: Number(bar.close),
    volume: Number(bar.volume || 0), amount: bar.turnover === undefined || bar.turnover === null ? undefined : Number(bar.turnover)
  }))
}

function sanitizeNumbers(value) {
  if (Array.isArray(value)) return value.map(item => Number.isFinite(item) ? item : sanitizeNumbers(item))
  if (value && typeof value === 'object') {
    const output = {}
    for (const [key, item] of Object.entries(value)) output[key] = sanitizeNumbers(item)
    return output
  }
  return typeof value === 'number' && !Number.isFinite(value) ? null : value
}

function evaluate(source, parameters, bars) {
  const compiled = compile(source, parameters)
  if (compiled.missing.length) throw new Error(`请配置参数：${compiled.missing.join(', ')}`)
  const prefix = parameterPrefix(parameters)
  const data = normalizeMarketData(bars)
  const engine = new FormulaEngine()
  const coreResult = engine.evaluate(`${prefix}\n${compiled.prepared.core}`, data)
  const drawings = []
  compiled.prepared.drawings.forEach((drawing, statementIndex) => {
    const result = engine.evaluate(`${prefix}\n${compiled.prepared.core}\n${drawing.expression};`, data)
    for (const item of result.drawings || []) drawings.push({ ...item, statementIndex, style: drawing.style })
    if (drawings.length > MAX_DRAWING_EVENTS) throw new Error('绘图事件超过 500000 条限制')
  })
  return sanitizeNumbers({ outputs: coreResult.outputs, drawings, warnings: compiled.warnings })
}

self.onmessage = event => {
  const { id, type, formula, parameters, bars } = event.data || {}
  try {
    const result = type === 'evaluate' ? evaluate(formula, parameters, bars) : compile(formula, parameters)
    self.postMessage({ id, ok: true, result })
  } catch (error) {
    self.postMessage({ id, ok: false, error: error?.message || String(error) })
  }
}
