import { describe, expect, it } from 'vitest'

import { formatDateTimeLocalInput, getBrowserTimeZone, parseDateTimeLocalInput } from '../format'

/**
 * formatDateTimeLocalInput.spec.ts tests local datetime parsing and minute-level
 * round trips. Fixtures cover optional seconds, fractional precision, malformed
 * fields, calendar overflow, timezone-bearing input rejection, and the nonempty
 * timezone label returned by the running environment.
 */

describe('formatDateTimeLocalInput', () => {
  /**
   * A local timestamp is created at exact minute precision. Formatting and reparsing it
   * returns the same Unix-seconds value, protecting the form value round trip.
   */
  it('round-trips a minute-precision local timestamp', () => {
    // Convert the local fixture to whole persisted Unix seconds.
    const timestamp = Math.floor(new Date(2026, 7, 30, 7, 24).getTime() / 1000)

    expect(formatDateTimeLocalInput(timestamp)).toBe('2026-08-30T07:24')
    expect(parseDateTimeLocalInput('2026-08-30T07:24')).toBe(timestamp)
  })
})

describe('parseDateTimeLocalInput', () => {
  /**
   * Local components include seconds, while alternate strings append a timezone offset
   * or Z. The parser accepts the local value and rejects both timezone-bearing forms.
   */
  it('uses local date components and rejects timezone-bearing values', () => {
    // Match the parser's whole-second result from the local Date fixture.
    const expected = Math.floor(new Date(2026, 7, 30, 7, 24, 10).getTime() / 1000)

    expect(parseDateTimeLocalInput('2026-08-30T07:24:10')).toBe(expected)
    expect(parseDateTimeLocalInput('2026-08-30T07:24:10+08:00')).toBeNull()
    expect(parseDateTimeLocalInput('2026-08-30T07:24:10Z')).toBeNull()
  })

  /**
   * Empty text, malformed separators, invalid calendar days, and overflowing clock
   * fields enter the parser. Every supplied invalid value returns null rather than a
   * normalized timestamp.
   */
  it('rejects empty, malformed, and overflowing calendar values', () => {
    expect(parseDateTimeLocalInput('')).toBeNull()
    expect(parseDateTimeLocalInput('2026-08-30 07:24')).toBeNull()
    expect(parseDateTimeLocalInput('2026-02-30T07:24')).toBeNull()
    expect(parseDateTimeLocalInput('2026-08-30T24:00')).toBeNull()
    expect(parseDateTimeLocalInput('2026-08-30T07:60')).toBeNull()
  })

  /**
   * The local form value includes optional seconds and a fractional second. Parsing
   * produces the floored Unix-seconds timestamp derived from those local components.
   */
  it('accepts optional seconds and fractional seconds', () => {
    // Match the parser's whole-second result from the local Date fixture.
    const expected = Math.floor(new Date(2026, 7, 30, 7, 24, 10, 120).getTime() / 1000)

    expect(parseDateTimeLocalInput('2026-08-30T07:24:10.12')).toBe(expected)
  })
})

describe('getBrowserTimeZone', () => {
  /**
   * The running environment resolves its browser timezone through Intl. The helper
   * returns nonempty text, covering the ordinary identifier-or-UTC result without
   * assuming a particular host timezone.
   */
  it('returns a timezone identifier or the UTC fallback', () => {
    expect(getBrowserTimeZone()).toBeTruthy()
  })
})
