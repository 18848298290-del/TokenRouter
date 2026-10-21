import { describe, expect, it } from 'vitest'
import { calculateUsageTps, formatUsageTps } from '../usageTps'

/**
 * usageTps.spec.ts tests recorded throughput access and tokens-per-second
 * display formatting. A measured zero remains numeric and visible, while absent,
 * negative, and nonfinite values stay unavailable through both the numeric and
 * formatted interfaces.
 */

describe('usage TPS display', () => {
  /**
   * A usage row carries a finite throughput estimate and another carries measured zero.
   * Numeric access retains both values and formatting applies the shared
   * tokens-per-second suffix.
   */
  it('uses the server estimate consistently for display and export', () => {
    expect(calculateUsageTps({ tps: 42.3 })).toBe(42.3)
    expect(formatUsageTps({ tps: 42.3 })).toBe('42.3 tok/s')
    expect(formatUsageTps({ tps: 0 })).toBe('0.0 tok/s')
  })
  /**
   * Throughput values are missing, nonfinite, or negative. Numeric access returns null
   * and display returns a dash for every invalid observation in the supplied fixture
   * set.
   */
  it('leaves absent or invalid performance unrecorded', () => {
    for (const tps of [undefined, null, Number.NaN, Infinity, -1]) {
      expect(calculateUsageTps({ tps })).toBeNull()
      expect(formatUsageTps({ tps })).toBe('-')
    }
  })
})
