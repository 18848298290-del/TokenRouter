import { describe, expect, it } from 'vitest'

import { formatDateTimeToMinute } from '../format'

/**
 * formatDateTimeToMinute.spec.ts tests minute-precision date rendering with an
 * explicit locale. A local timestamp with nonzero seconds confirms those seconds
 * are omitted, and an invalid Date confirms the formatter returns empty display
 * text.
 */

describe('formatDateTimeToMinute', () => {
  /**
   * A local Date includes nonzero seconds and uses the en-GB locale override. The
   * rendered date and time omit seconds while retaining the requested locale ordering.
   */
  it('formats local date and time without seconds', () => {
    const value = new Date(2026, 6, 19, 20, 30, 45)

    expect(formatDateTimeToMinute(value, 'en-GB')).toBe('19/07/2026, 20:30')
  })

  /**
   * An invalid Date is formatted with an explicit locale override. Validation returns
   * empty text before locale rendering, preserving the missing form of an unusable
   * date.
   */
  it('returns an empty string for an invalid date', () => {
    expect(formatDateTimeToMinute(new Date('invalid'), 'en-GB')).toBe('')
  })
})
