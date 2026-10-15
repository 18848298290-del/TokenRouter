import { describe, expect, it } from 'vitest'
import { formatRequestTimingForExport, formatTimingMs, timingBreakdown, timingValue } from './requestTiming'

// requestTiming.test.ts covers the shared timing rules behind the usage tooltip,
// request lookup and export. It checks that zero stays measurable, historical
// SSE offsets still fill the first-event row, durations appear only when
// recorded and sum to the slot offset, and synchronous requests lose their
// stream-only stages.
describe('request timing breakdown', () => {
  // The generic first event wins over the SSE offset, including a measured zero.
  it('prefers generic event offsets and falls back to historical SSE only when absent', () => {
    expect(timingValue({}, 'upstream_first_event_ms')).toBeNull()
    expect(timingValue({ upstream_first_sse_data_ms: 1500 }, 'upstream_first_event_ms')).toBe(1500)
    expect(timingValue({ upstream_first_event_ms: 0, upstream_first_sse_data_ms: 1500 }, 'upstream_first_event_ms')).toBe(0)
    expect(timingValue({ user_slot_wait_ms: 0 }, 'user_slot_wait_ms')).toBe(0)
  })

  // Durations, retry wait and stream applicability decide which rows exist.
  it('selects duration and event rows from the recorded fields', () => {
    expect(timingBreakdown({ provider_slot_acquired_ms: 118235 }).durations).toEqual([])
    expect(timingBreakdown({ user_slot_wait_ms: 0 }).durations.map((row) => row.key)).toEqual([
      'request_body_read_ms', 'preprocess_ms', 'user_slot_wait_ms', 'provider_slot_wait_ms',
    ])
    expect(timingBreakdown({ user_slot_wait_ms: 0, retry_provider_slot_wait_ms: 0 }).retryWait).toBeNull()
    expect(timingBreakdown({ user_slot_wait_ms: 0, retry_provider_slot_wait_ms: 9 }).retryWait?.value).toBe(9)
    expect(timingBreakdown({ user_slot_wait_ms: 0, retry_provider_slot_wait_ms: 9 }).durations).toHaveLength(4)
    expect(timingBreakdown({}, false).events).toHaveLength(5)
    expect(timingBreakdown({ timing_stream: 1 }, false).events).toHaveLength(8)
    expect(timingBreakdown({}).events).toHaveLength(8)
    expect(timingBreakdown({}).admitted).toBeNull()
    expect(timingBreakdown({ timing_admitted: 0 }).admitted).toBe(false)
  })

  // The four durations add up to the slot offset; a gap leaves the total unknown.
  it('sums complete admission durations to the slot offset', () => {
    const timing = { request_body_read_ms: 12, preprocess_ms: 4, user_slot_wait_ms: 0, provider_slot_wait_ms: 104, provider_slot_acquired_ms: 120 }
    expect(timingBreakdown(timing).admissionTotal).toBe(timing.provider_slot_acquired_ms)
    expect(timingBreakdown({ user_slot_wait_ms: 0 }).admissionTotal).toBeNull()
    expect(timingBreakdown({}).admissionTotal).toBeNull()
  })

  // Formatting keeps zero and missing apart and switches units at the usual bounds.
  it('formats milliseconds, seconds and minutes', () => {
    expect(formatTimingMs(null)).toBe('-')
    expect(formatTimingMs(0)).toBe('0ms')
    expect(formatTimingMs(1500)).toBe('1.50s')
    expect(formatTimingMs(118235)).toBe('1m 58s')
  })

  // Export keeps raw field names so spreadsheets can tell durations from offsets.
  it('exports available raw metrics without inventing durations', () => {
    expect(formatRequestTimingForExport({ user_slot_wait_ms: 0, provider_slot_acquired_ms: 118235 })).toBe('user_slot_wait_ms=0ms; provider_slot_acquired_ms=118235ms')
    expect(formatRequestTimingForExport()).toBe('')
  })
})
