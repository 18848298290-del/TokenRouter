import { describe, expect, it } from 'vitest'
import { buildOpsErrorTimeParams } from '../opsErrorParams'

// opsErrorParams.spec.ts checks time-filter construction shared by Ops error
// queries through direct helper calls. Complete custom selections use explicit
// bounds, while predefined ranges and incomplete custom selections retain
// supported backend time-range parameters.

describe('buildOpsErrorTimeParams', () => {
  // A custom range provides both timestamp bounds to the shared helper. The
  // result must contain those exact start and end values as explicit query
  // parameters.
  it('uses explicit timestamps for a complete custom range', () => {
    expect(
      buildOpsErrorTimeParams('custom', '2026-08-01T00:00:00Z', '2026-08-02T00:00:00Z'),
    ).toEqual({
      start_time: '2026-08-01T00:00:00Z',
      end_time: '2026-08-02T00:00:00Z',
    })
  })

  // Predefined and incomplete custom ranges are passed to the shared helper.
  // Presets must pass through, while each missing-bound combination must select
  // the supported one-hour range.
  it('preserves predefined ranges and falls back for incomplete custom ranges', () => {
    expect(buildOpsErrorTimeParams('24h')).toEqual({ time_range: '24h' })
    expect(buildOpsErrorTimeParams('custom', null, null)).toEqual({ time_range: '1h' })
    expect(buildOpsErrorTimeParams('custom', '2026-08-01T00:00:00Z', null)).toEqual({
      time_range: '1h',
    })
    expect(buildOpsErrorTimeParams('custom', null, '2026-08-02T00:00:00Z')).toEqual({
      time_range: '1h',
    })
  })
})
