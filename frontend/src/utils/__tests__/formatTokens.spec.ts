import { describe, expect, it } from 'vitest'
import { formatTokens, formatTokensK } from '../format'

/**
 * formatTokens.spec.ts tests the shared two-decimal token formatter and its one-
 * decimal counterpart. Counts exercise compact thresholds, absent aggregates,
 * unsuffixed small values, and the different treatment of billion-sized inputs
 * by the two functions.
 */

describe('formatTokens', () => {
  /**
   * Token counts exercise exact and fractional K, M, and B units. All compact results
   * use two fractional digits, preserving chart and distribution formatting precision.
   */
  it('K/M/B 档位两位小数', () => {
    expect(formatTokens(1000)).toBe('1.00K')
    expect(formatTokens(1500)).toBe('1.50K')
    expect(formatTokens(1_000_000)).toBe('1.00M')
    expect(formatTokens(3_250_000)).toBe('3.25M')
    expect(formatTokens(1_000_000_000)).toBe('1.00B')
    expect(formatTokens(1_200_000_000)).toBe('1.20B')
  })

  /**
   * Zero and a positive count below one thousand enter the shared formatter. Both
   * remain unsuffixed numeric strings in the active locale for small-count display.
   */
  it('小于 1000 用千分位原样展示', () => {
    expect(formatTokens(0)).toBe('0')
    expect(formatTokens(950)).toBe('950')
  })

  /**
   * Null and undefined represent absent token aggregates. Both render as zero so chart
   * labels and distribution cells use a consistent value for missing counts.
   */
  it('null/undefined 归一为 0', () => {
    expect(formatTokens(null)).toBe('0')
    expect(formatTokens(undefined)).toBe('0')
  })

  /**
   * The one-decimal token helper receives thousand, million, billion, and small values.
   * Its outputs keep its own precision and million ceiling distinct from the
   * two-decimal formatter.
   */
  it('与 formatTokensK 的精度差异是有意的,不要互相替换', () => {
    expect(formatTokensK(1500)).toBe('1.5K')
    expect(formatTokensK(1_500_000)).toBe('1.5M')
    expect(formatTokensK(1_500_000_000)).toBe('1500.0M')
    expect(formatTokensK(950)).toBe('950')
  })
})
