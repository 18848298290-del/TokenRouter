import { describe, expect, it } from 'vitest'

import { formatMemorySizeMB } from '../opsFormatters'

// opsFormatters.spec.ts checks binary-megabyte capacity formatting used by Ops
// memory displays. Direct calls cover the unit transition, zero capacity, and
// invalid observations so missing measurements remain distinct from actual
// measured values.

describe('formatMemorySizeMB', () => {
  // Capacity observations remain below one binary gigabyte. The formatter must
  // keep their displayed MB unit and numeric values instead of promoting the
  // observations prematurely.
  it('keeps capacities below one GiB in MB', () => {
    expect(formatMemorySizeMB(61)).toBe('61 MB')
    expect(formatMemorySizeMB(1023)).toBe('1023 MB')
  })

  // Capacity observations reach or exceed 1024 binary megabytes. The formatter
  // must display the corresponding GB magnitude, including fractional capacities
  // and larger whole values.
  it('converts capacities at or above one GiB to GB', () => {
    expect(formatMemorySizeMB(1024)).toBe('1 GB')
    expect(formatMemorySizeMB(1536)).toBe('1.5 GB')
    expect(formatMemorySizeMB(32768)).toBe('32 GB')
  })

  // Capacity input includes zero, negative numbers, nonfinite numbers, and
  // missing observations. Zero must retain an MB label while invalid
  // observations display the unavailable marker.
  it('handles zero and invalid values without producing a misleading unit', () => {
    expect(formatMemorySizeMB(0)).toBe('0 MB')
    expect(formatMemorySizeMB(-1)).toBe('-')
    expect(formatMemorySizeMB(Number.NaN)).toBe('-')
    expect(formatMemorySizeMB(Number.POSITIVE_INFINITY)).toBe('-')
    expect(formatMemorySizeMB(null)).toBe('-')
    expect(formatMemorySizeMB(undefined)).toBe('-')
  })
})
