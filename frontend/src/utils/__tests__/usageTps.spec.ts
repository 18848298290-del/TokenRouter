import { describe, expect, it } from 'vitest'
import { calculateUsageTps, formatUsageTps } from '../usageTps'

describe('usage TPS display', () => {
  it('uses the server estimate consistently for display and export', () => {
    expect(calculateUsageTps({ tps: 42.3 })).toBe(42.3)
    expect(formatUsageTps({ tps: 42.3 })).toBe('42.3 tok/s')
    expect(formatUsageTps({ tps: 0 })).toBe('0.0 tok/s')
  })
  it('leaves absent or invalid performance unrecorded', () => {
    for (const tps of [undefined, null, Number.NaN, Infinity, -1]) {
      expect(calculateUsageTps({ tps })).toBeNull()
      expect(formatUsageTps({ tps })).toBe('-')
    }
  })
})
