import { describe, expect, it } from 'vitest'
import { formatCompactTokenCount, formatCompactTokenRange } from '../formatters'

/**
 * formatCompactTokenRange.spec.ts tests lowercase compact token counts and range
 * labels. Examples cover fractional scaled counts, integer rounding, open-ended
 * bounds, and bounded ranges so the shared formatter retains its display units
 * and endpoint punctuation.
 */

describe('formatCompactTokenCount', () => {
  /**
   * Counts below one thousand include zero and the last integer before the first
   * threshold. Their rendered strings contain integer values without a compact suffix.
   */
  it('小数值不带后缀', () => {
    expect(formatCompactTokenCount(0)).toBe('0')
    expect(formatCompactTokenCount(999)).toBe('999')
  })

  /**
   * Thousand-sized counts exercise both fractional and three-digit scaled values. The
   * formatter retains one decimal for 1.5k and removes fractional digits from 272k.
   */
  it('千级用 k 后缀，百位以上不带小数', () => {
    expect(formatCompactTokenCount(1500)).toBe('1.5k')
    expect(formatCompactTokenCount(272000)).toBe('272k')
  })

  /**
   * Million-sized counts include both an exact unit and a fractional unit. Lowercase m
   * labels retain the meaningful half-million fraction while omitting unnecessary
   * decimal zeros.
   */
  it('百万级用 m 后缀', () => {
    expect(formatCompactTokenCount(1_000_000)).toBe('1m')
    expect(formatCompactTokenCount(1_500_000)).toBe('1.5m')
  })
})

describe('formatCompactTokenRange', () => {
  /**
   * Upper bounds are separately omitted and supplied as null. Both produce the same
   * plus-ended label, keeping an open-ended range distinct from a numeric upper bound.
   */
  it('无上限区间用 + 后缀', () => {
    expect(formatCompactTokenRange(272000, null)).toBe('272k+')
    expect(formatCompactTokenRange(272000)).toBe('272k+')
  })

  /**
   * Two bounded ranges use zero and nonzero lower limits. Each renders its compact
   * endpoints in supplied order with a hyphen separating the lower and upper labels.
   */
  it('闭区间用连字符', () => {
    expect(formatCompactTokenRange(0, 272000)).toBe('0-272k')
    expect(formatCompactTokenRange(128000, 272000)).toBe('128k-272k')
  })
})
