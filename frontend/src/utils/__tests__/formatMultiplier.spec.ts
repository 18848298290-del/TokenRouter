import { describe, expect, it } from 'vitest'
import { formatMultiplier } from '../formatters'

/**
 * formatMultiplier.spec.ts tests the precision used for displayed billing
 * multipliers. Ordinary values retain two decimal places, fractional rates
 * retain meaningful extra digits, and values below the fixed-decimal threshold
 * switch to significant-digit output.
 */

describe('formatMultiplier', () => {
  /**
   * Multipliers contain meaningful third and fourth fractional digits. Formatting keeps
   * those digits so small billing rates remain distinguishable after the shared
   * multiplier display conversion.
   */
  it('keeps significant decimals instead of rounding to 2 places', () => {
    expect(formatMultiplier(0.035)).toBe('0.035')
    expect(formatMultiplier(0.125)).toBe('0.125')
    expect(formatMultiplier(0.015)).toBe('0.015')
    expect(formatMultiplier(0.0625)).toBe('0.0625')
  })

  /**
   * Whole and single-decimal multipliers are formatted for display. Every result
   * retains at least two fractional digits, keeping ordinary multiplier labels
   * consistent across these inputs.
   */
  it('pads to at least 2 decimals for round values', () => {
    expect(formatMultiplier(0.3)).toBe('0.30')
    expect(formatMultiplier(1)).toBe('1.00')
    expect(formatMultiplier(1.5)).toBe('1.50')
    expect(formatMultiplier(2)).toBe('2.00')
  })

  /**
   * Multipliers reach the fixed-precision lower range including exactly 0.0001. Their
   * significant digits remain visible and unnecessary trailing zeros are omitted from
   * the result.
   */
  it('handles small values down to 4 decimals', () => {
    expect(formatMultiplier(0.001)).toBe('0.001')
    expect(formatMultiplier(0.0001)).toBe('0.0001')
  })

  /**
   * A multiplier falls below the fixed four-decimal threshold. Its output retains two
   * significant digits so the small positive rate does not round to zero.
   */
  it('falls back to 2 significant digits below 0.0001', () => {
    expect(formatMultiplier(0.00005)).toBe('0.000050')
  })
})
