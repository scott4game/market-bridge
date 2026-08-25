/**
 * REF - Reference
 * Returns the value N periods ago
 *
 * @param data - Input data array
 * @param period - Number of periods to look back
 * @returns Array with referenced values (NaN for insufficient history)
 */
export function REF(data: number[], period: number): number[] {
  const result: number[] = new Array(data.length);

  for (let i = 0; i < data.length; i++) {
    if (i < period) {
      result[i] = NaN;
    } else {
      result[i] = data[i - period];
    }
  }

  return result;
}

/**
 * REFV - Reference value N periods ago.
 * TDX-compatible alias of REF without future-function marking.
 */
export function REFV(data: number[], period: number): number[] {
  return REF(data, period);
}

/**
 * REFX - Future reference by N periods.
 */
export function REFX(data: number[], period: number): number[] {
  const offset = Math.floor(period);
  const result: number[] = new Array(data.length);

  for (let i = 0; i < data.length; i++) {
    const futureIndex = i + offset;
    result[i] = futureIndex >= data.length ? NaN : data[futureIndex];
  }

  return result;
}

/**
 * REFXV - Future reference by N periods.
 * TDX-compatible alias of REFX without future-function marking.
 */
export function REFXV(data: number[], period: number): number[] {
  return REFX(data, period);
}

/**
 * HHV - Highest High Value
 * Returns the highest value over N periods
 *
 * @param data - Input data array
 * @param period - Number of periods to look back
 * @returns Array with highest values (NaN for insufficient data)
 */
export function HHV(data: number[], period: number): number[] {
  const result: number[] = new Array(data.length);

  for (let i = 0; i < data.length; i++) {
    if (i < period - 1) {
      result[i] = NaN;
      continue;
    }

    let max = data[i];
    for (let j = 1; j < period; j++) {
      max = Math.max(max, data[i - j]);
    }
    result[i] = max;
  }

  return result;
}

/**
 * HHVBARS - Bars since the highest value within the rolling window.
 */
export function HHVBARS(data: number[], period: number): number[] {
  const n = Math.floor(period);
  const result: number[] = new Array(data.length);

  for (let i = 0; i < data.length; i++) {
    if (i < n - 1) {
      result[i] = NaN;
      continue;
    }

    let maxValue = data[i];
    let bars = 0;
    for (let j = 1; j < n; j++) {
      if (data[i - j] > maxValue) {
        maxValue = data[i - j];
        bars = j;
      }
    }
    result[i] = bars;
  }

  return result;
}

/**
 * LLV - Lowest Low Value
 * Returns the lowest value over N periods
 *
 * @param data - Input data array
 * @param period - Number of periods to look back
 * @returns Array with lowest values (NaN for insufficient data)
 */
export function LLV(data: number[], period: number): number[] {
  const result: number[] = new Array(data.length);

  for (let i = 0; i < data.length; i++) {
    if (i < period - 1) {
      result[i] = NaN;
      continue;
    }

    let min = data[i];
    for (let j = 1; j < period; j++) {
      min = Math.min(min, data[i - j]);
    }
    result[i] = min;
  }

  return result;
}

/**
 * LLVBARS - Bars since the lowest value within the rolling window.
 */
export function LLVBARS(data: number[], period: number): number[] {
  const n = Math.floor(period);
  const result: number[] = new Array(data.length);

  for (let i = 0; i < data.length; i++) {
    if (i < n - 1) {
      result[i] = NaN;
      continue;
    }

    let minValue = data[i];
    let bars = 0;
    for (let j = 1; j < n; j++) {
      if (data[i - j] < minValue) {
        minValue = data[i - j];
        bars = j;
      }
    }
    result[i] = bars;
  }

  return result;
}

function dynamicPeriod(periods: number[], index: number): number {
  const value = periods.length === 1 ? periods[0] : periods[index];
  if (!Number.isFinite(value)) {
    return 0;
  }
  return Math.max(0, Math.floor(value));
}

/** TDX dynamic-period REF/REFV. */
export function DYNAMIC_REF(data: number[], periods: number[]): number[] {
  return data.map((_, index) => {
    const period = dynamicPeriod(periods, index);
    return index < period ? NaN : data[index - period];
  });
}

/** TDX dynamic-period REFX/REFXV. */
export function DYNAMIC_REFX(data: number[], periods: number[]): number[] {
  return data.map((_, index) => {
    const target = index + dynamicPeriod(periods, index);
    return target >= data.length ? NaN : data[target];
  });
}

function dynamicExtreme(data: number[], periods: number[], highest: boolean, bars: boolean): number[] {
  return data.map((_, index) => {
    const period = dynamicPeriod(periods, index);
    const start = period === 0 ? 0 : index - period + 1;
    if (start < 0) {
      return NaN;
    }
    let selected = data[index];
    let selectedBars = 0;
    for (let cursor = index - 1; cursor >= start; cursor--) {
      if ((highest && data[cursor] > selected) || (!highest && data[cursor] < selected)) {
        selected = data[cursor];
        selectedBars = index - cursor;
      }
    }
    return bars ? selectedBars : selected;
  });
}

export function DYNAMIC_HHV(data: number[], periods: number[]): number[] {
  return dynamicExtreme(data, periods, true, false);
}

export function DYNAMIC_LLV(data: number[], periods: number[]): number[] {
  return dynamicExtreme(data, periods, false, false);
}

export function DYNAMIC_HHVBARS(data: number[], periods: number[]): number[] {
  return dynamicExtreme(data, periods, true, true);
}

export function DYNAMIC_LLVBARS(data: number[], periods: number[]): number[] {
  return dynamicExtreme(data, periods, false, true);
}

/** TDX BACKSET. A true value rewrites the current and preceding N-1 bars. */
export function BACKSET(condition: number[], periods: number[]): number[] {
  const result = new Array(condition.length).fill(0);
  for (let index = 0; index < condition.length; index++) {
    if (!condition[index] || Number.isNaN(condition[index])) continue;
    const period = Math.max(1, dynamicPeriod(periods, index));
    for (let cursor = Math.max(0, index - period + 1); cursor <= index; cursor++) result[cursor] = 1;
  }
  return result;
}

type ZigPoint = { index: number; kind: 'peak' | 'trough' };

function zigPoints(data: number[], percentage: number): ZigPoint[] {
  if (data.length === 0) return [];
  const threshold = Math.max(0, percentage) / 100;
  let high = 0;
  let low = 0;
  let direction = 0;
  const points: ZigPoint[] = [];
  for (let index = 1; index < data.length; index++) {
    if (data[index] >= data[high]) high = index;
    if (data[index] <= data[low]) low = index;
    if (direction === 0) {
      if (data[index] >= data[low] * (1 + threshold)) {
        points.push({ index: low, kind: 'trough' });
        direction = 1;
        high = index;
      } else if (data[index] <= data[high] * (1 - threshold)) {
        points.push({ index: high, kind: 'peak' });
        direction = -1;
        low = index;
      }
    } else if (direction > 0 && data[index] <= data[high] * (1 - threshold)) {
      points.push({ index: high, kind: 'peak' });
      direction = -1;
      low = index;
    } else if (direction < 0 && data[index] >= data[low] * (1 + threshold)) {
      points.push({ index: low, kind: 'trough' });
      direction = 1;
      high = index;
    }
  }
  const finalIndex = direction >= 0 ? high : low;
  const finalKind = direction >= 0 ? 'peak' : 'trough';
  if (points[points.length - 1]?.index !== finalIndex) points.push({ index: finalIndex, kind: finalKind });
  return points;
}

/** TDX ZIG future function, with N expressed as a percentage. */
export function ZIG(data: number[], percentage: number): number[] {
  const points = zigPoints(data, percentage);
  if (points.length === 0) return [];
  const result = new Array(data.length).fill(NaN);
  for (let part = 0; part < points.length - 1; part++) {
    const from = points[part].index;
    const to = points[part + 1].index;
    for (let index = from; index <= to; index++) {
      const ratio = to === from ? 0 : (index - from) / (to - from);
      result[index] = data[from] + (data[to] - data[from]) * ratio;
    }
  }
  const first = points[0].index;
  const last = points[points.length - 1].index;
  for (let index = 0; index < first; index++) result[index] = data[first];
  for (let index = last; index < data.length; index++) result[index] = data[last];
  return result;
}

/** TDX PEAK/TROUGH family derived from ZIG turning points. */
export function ZIG_TURN(data: number[], percentage: number, order: number, kind: 'peak' | 'trough', bars: boolean): number[] {
  const points = zigPoints(data, percentage).filter(point => point.kind === kind);
  const wanted = Math.max(1, Math.floor(order));
  let cursor = 0;
  const eligible: ZigPoint[] = [];
  return data.map((_, index) => {
    while (cursor < points.length && points[cursor].index <= index) eligible.push(points[cursor++]);
    const point = eligible[eligible.length - wanted];
    if (!point) return NaN;
    return bars ? index - point.index : data[point.index];
  });
}
