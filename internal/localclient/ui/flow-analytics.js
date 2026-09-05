(function (root, factory) {
  const api = factory()
  if (typeof module === 'object' && module.exports) module.exports = api
  else root.flowAnalyticsUtils = api
})(typeof self !== 'undefined' ? self : this, function () {
  function finite(value) {
    const number = Number(value)
    return Number.isFinite(number) ? number : null
  }

  function normalizeTimestamp(value) {
    const timestamp = Date.parse(value)
    return Number.isFinite(timestamp) ? timestamp : Number(value) || 0
  }

  function normalizeFlowPoint(point = {}) {
    return {
      timestamp: normalizeTimestamp(point.timestamp),
      volume: finite(point.volume),
      turnover: finite(point.turnover),
      inflow: finite(point.inflow),
      outflow: finite(point.outflow),
      netFlow: finite(point.net_flow),
      flowRatio: finite(point.flow_ratio)
    }
  }

  function normalizeVolumePoint(point = {}) {
    return {
      timestamp: normalizeTimestamp(point.timestamp),
      volume: finite(point.volume),
      turnover: finite(point.turnover),
      baselineVolume: finite(point.baseline_volume),
      baselineTurnover: finite(point.baseline_turnover),
      volumeRatio: finite(point.volume_ratio),
      turnoverRatio: finite(point.turnover_ratio),
      baselineComplete: Boolean(point.baseline_complete)
    }
  }

  function mergeIntoBars(bars, volumePoints = [], flowPoints = []) {
    const volume = new Map(volumePoints.map(point => {
      const normalized = normalizeVolumePoint(point)
      return [normalized.timestamp, normalized]
    }))
    const flow = new Map(flowPoints.map(point => {
      const normalized = normalizeFlowPoint(point)
      return [normalized.timestamp, normalized]
    }))
    return bars.map(bar => Object.assign(bar, volume.get(bar.timestamp) || {}, flow.get(bar.timestamp) || {}))
  }

  function normalizeSector(sector = {}) {
    return {
      code: String(sector.sic_code || ''),
      name: String(sector.sic_description || ''),
      members: Number(sector.members || 0),
      evaluated: Number(sector.evaluated || 0),
      turnover: finite(sector.turnover),
      netFlow: finite(sector.net_flow),
      flowRatio: finite(sector.flow_ratio)
    }
  }

  function sortSectors(sectors, mode = 'amount') {
    const key = mode === 'ratio' ? 'flowRatio' : 'netFlow'
    return sectors.map(normalizeSector).sort((left, right) => (right[key] ?? -Infinity) - (left[key] ?? -Infinity) || left.code.localeCompare(right.code))
  }

  return { normalizeFlowPoint, normalizeVolumePoint, mergeIntoBars, normalizeSector, sortSectors }
})
