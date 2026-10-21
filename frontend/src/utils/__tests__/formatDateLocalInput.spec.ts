import { describe, expect, it, vi } from 'vitest'

import { formatDateLocalInput, formatDateTimeLocalInput } from '../format'

/**
 * formatDateLocalInput.spec.ts tests local date and datetime form rendering with
 * controlled calendar getters. The assertions separate local fields from UTC
 * text, check date validity, and retain zero-padded month, day, hour, and minute
 * output.
 */

describe('formatDateLocalInput', () => {
  /**
   * A Date has local calendar getters stubbed to a different day from its UTC
   * timestamp. The date-input string follows those local fields and pads month and day.
   */
  it('formats the calendar date in local time', () => {
    const localDate = new Date('2026-07-12T16:30:00Z')
    vi.spyOn(localDate, 'getFullYear').mockReturnValue(2026)
    vi.spyOn(localDate, 'getMonth').mockReturnValue(6)
    vi.spyOn(localDate, 'getDate').mockReturnValue(13)

    expect(formatDateLocalInput(localDate)).toBe('2026-07-13')
  })

  /**
   * An invalid Date cannot supply a usable calendar day. The formatter returns empty
   * text so the date control receives its empty-value representation for that input.
   */
  it('returns an empty string for an invalid date', () => {
    expect(formatDateLocalInput(new Date('invalid'))).toBe('')
  })
})

describe('formatDateTimeLocalInput', () => {
  /**
   * Local date and time getters are controlled for a Unix-seconds input. The
   * datetime-local string uses those fields through minutes, with zero padding and the
   * required T separator.
   */
  it('formats the date and time with local fields', () => {
    vi.spyOn(Date.prototype, 'getFullYear').mockReturnValue(2026)
    vi.spyOn(Date.prototype, 'getMonth').mockReturnValue(6)
    vi.spyOn(Date.prototype, 'getDate').mockReturnValue(13)
    vi.spyOn(Date.prototype, 'getHours').mockReturnValue(0)
    vi.spyOn(Date.prototype, 'getMinutes').mockReturnValue(30)

    expect(formatDateTimeLocalInput(1_783_893_000)).toBe('2026-07-13T00:30')
  })
})
