import { describe, expect, it } from 'vitest'

import {
  bucketKeys,
  fillTrendBuckets,
  previousRange,
  resolveCustomRange,
  resolvePresetRange,
  summarizeTrend,
} from '../usageChartData'
import type { TrendDataPoint } from '@/api/usage.types'

// usageChartData.spec.ts checks local dashboard intervals, continuous bucket filling, and
// weighted usage totals using a fixed clock. The fixtures separate recorded input and cache
// tokens so aggregate cache effectiveness can be checked independently from bucket count.

// point creates a zero-usage dated bucket and applies scenario-specific field overrides.
// Tests supply separate input and cache categories when checking the weighted aggregate
// cache-hit percentage.
const point = (date: string, overrides: Partial<TrendDataPoint> = {}): TrendDataPoint => ({
  date,
  requests: 0,
  input_tokens: 0,
  output_tokens: 0,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: 0,
  cost: 0,
  actual_cost: 0,
  ...overrides,
})

describe('usageChartData', () => {
  const now = new Date(2026, 8, 30, 15, 20)

  // A twenty-four-hour shortcut is resolved during a partly completed hour. The test
  // protects hour alignment, inclusion of the current bucket, and exactly twenty-four
  // chronological labels.
  it('24 小时范围按整点对齐并包含当前小时', () => {
    const range = resolvePresetRange('24h', now)
    expect(range.granularity).toBe('hour')
    expect(range.startAt).toEqual(new Date(2026, 8, 29, 16))
    expect(range.endAt).toEqual(new Date(2026, 8, 30, 16))
    const keys = bucketKeys(range, now)
    expect(keys).toHaveLength(24)
    expect(keys[0]).toBe('2026-09-29 16:00')
    expect(keys[23]).toBe('2026-09-30 15:00')
  })

  // A seven-day shortcut includes the current local date. The test protects its complete
  // calendar-day labels and an adjacent comparison interval ending at the current start.
  it('按天范围包含今天，上一周期紧邻且等长', () => {
    const range = resolvePresetRange('7d', now)
    expect(range.granularity).toBe('day')
    expect(bucketKeys(range, now)).toEqual([
      '2026-09-24',
      '2026-09-25',
      '2026-09-26',
      '2026-09-27',
      '2026-09-28',
      '2026-09-29',
      '2026-09-30',
    ])
    const prev = previousRange(range)
    expect(prev.startAt).toEqual(new Date(2026, 8, 17))
    expect(prev.endAt).toEqual(range.startAt)
  })

  // Custom picker input supplies date-only endpoints around the current date. The test
  // protects inclusive end-date conversion, hourly precision for short spans, omitted future
  // buckets, and rejection of reversed ranges.
  it('自定义范围两天以内按小时，纯日期结束值包含当天，未来时段不画', () => {
    const range = resolveCustomRange('2026-09-29', '2026-09-30')
    expect(range?.granularity).toBe('hour')
    expect(range?.endAt).toEqual(new Date(2026, 9, 1))
    expect(bucketKeys(range!, now)).toHaveLength(24 + 16)
    expect(resolveCustomRange('2026-09-01', '2026-09-30')?.granularity).toBe('day')
    expect(resolveCustomRange('2026-09-30', '2026-09-01')).toBeNull()
  })

  // The API omits an unused day between two recorded buckets with unequal token volumes. The
  // test protects zero filling, additive totals, and cache-rate aggregation from input
  // counts.
  it('补齐后端省略的空时段并汇总指标', () => {
    const filled = fillTrendBuckets(
      [
        point('2026-09-28', {
          requests: 2,
          input_tokens: 100,
          cache_read_tokens: 300,
          total_tokens: 420,
          output_tokens: 20,
          actual_cost: 1.5,
        }),
        point('2026-09-30', {
          requests: 1,
          input_tokens: 100,
          total_tokens: 110,
          output_tokens: 10,
          actual_cost: 0.5,
        }),
      ],
      ['2026-09-28', '2026-09-29', '2026-09-30'],
    )
    expect(filled.map((item) => item.requests)).toEqual([2, 0, 1])
    expect(summarizeTrend(filled)).toEqual({ requests: 3, tokens: 530, cost: 2, cacheHitRate: 60 })
    expect(summarizeTrend([]).cacheHitRate).toBeNull()
  })
})
