import { describe, expect, it } from 'vitest'
import { formatCompactNumber } from '../format'

/**
 * formatCompactNumber.spec.ts tests compact numeric suffix selection and
 * missing-value output. Boundary fixtures retain one decimal at K, M, and B
 * scales, while an option suppresses billions and nullish counts render as zero.
 */

describe('formatCompactNumber', () => {
  /**
   * Counts cross decimal thousand, million, and billion thresholds. Suffix selection
   * occurs before one-decimal rounding, so a value just below one million still renders
   * with K.
   */
  it('formats boundary values with K/M/B', () => {
    expect(formatCompactNumber(0)).toBe('0')
    expect(formatCompactNumber(999)).toBe('999')
    expect(formatCompactNumber(1000)).toBe('1.0K')
    expect(formatCompactNumber(999999)).toBe('1000.0K')
    expect(formatCompactNumber(1000000)).toBe('1.0M')
    expect(formatCompactNumber(1000000000)).toBe('1.0B')
  })

  /**
   * The caller disables the billion suffix for a billion-sized request count. The
   * formatted result uses millions with one decimal, preserving the requested display
   * unit.
   */
  it('supports disabling billion unit (requests style)', () => {
    expect(formatCompactNumber(1000000000, { allowBillions: false })).toBe('1000.0M')
  })

  /**
   * Both null and undefined are supplied as missing counts. Each displays numeric zero,
   * keeping the shared compact formatter usable for rows with absent aggregates.
   */
  it('returns 0 for nullish input', () => {
    expect(formatCompactNumber(null)).toBe('0')
    expect(formatCompactNumber(undefined)).toBe('0')
  })
})
