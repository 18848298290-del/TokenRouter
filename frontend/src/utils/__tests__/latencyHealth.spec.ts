import { describe, expect, it } from 'vitest'

import { durationSeverity, firstTokenSeverity } from '../latencyHealth'

/**
 * latencyHealth.spec.ts tests first-token and total-duration severity cutoffs in
 * milliseconds. Each configured boundary is checked together with its immediate
 * predecessor, keeping inclusive threshold behavior and the distinct duration
 * scales visible in the assertions.
 */

describe('latencyHealth', () => {
  /**
   * First-token durations sit immediately below and exactly at each configured
   * threshold. Inclusive comparisons advance severity at ten, thirty, and sixty seconds
   * while zero remains good.
   */
  it('classifies first-token latency at 10s/30s/60s boundaries', () => {
    expect(firstTokenSeverity(0)).toBe('good')
    expect(firstTokenSeverity(9_999)).toBe('good')
    expect(firstTokenSeverity(10_000)).toBe('warn')
    expect(firstTokenSeverity(29_999)).toBe('warn')
    expect(firstTokenSeverity(30_000)).toBe('slow')
    expect(firstTokenSeverity(59_999)).toBe('slow')
    expect(firstTokenSeverity(60_000)).toBe('critical')
  })

  /**
   * Total durations sit immediately below and exactly at their longer thresholds.
   * Severity advances at one, three, and five minutes while shorter observations retain
   * the preceding category.
   */
  it('classifies total duration at 1min/3min/5min boundaries', () => {
    expect(durationSeverity(0)).toBe('good')
    expect(durationSeverity(59_999)).toBe('good')
    expect(durationSeverity(60_000)).toBe('warn')
    expect(durationSeverity(179_999)).toBe('warn')
    expect(durationSeverity(180_000)).toBe('slow')
    expect(durationSeverity(299_999)).toBe('slow')
    expect(durationSeverity(300_000)).toBe('critical')
  })
})
