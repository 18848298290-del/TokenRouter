import { describe, expect, it } from 'vitest'

import {
  getExpirationDateRelation,
  getRemainingExpiryDuration,
  highestQuotaExhausted,
} from '../subscriptionQuota'

/**
 * subscriptionQuota.spec.ts tests subscription expiry labels and highest-window
 * quota exhaustion. Date fixtures cover local calendar relationships, invalid
 * inputs, exact expiry, and upward rounding, while quota fixtures exercise
 * monthly-to-daily precedence and nonfinite or absent limits.
 */

describe('subscription expiry timing', () => {
  /**
   * The reference time is near local midnight and targets fall today or the next
   * calendar day. Classification follows local dates even when elapsed hours are small.
   */
  it('uses local calendar dates for today and tomorrow', () => {
    const now = new Date(2026, 2, 7, 23, 30)

    expect(getExpirationDateRelation(new Date(2026, 2, 7, 23, 45), now)).toBe('today')
    expect(getExpirationDateRelation(new Date(2026, 2, 8, 3, 30), now)).toBe('tomorrow')
  })

  /**
   * Expiry targets equal or precede the reference instant. Calendar classification
   * reports expired and remaining-duration calculation returns null for both elapsed
   * and exact-boundary cases.
   */
  it('treats the exact expiry instant and elapsed expiries as expired', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(now, now)).toBe('expired')
    expect(getRemainingExpiryDuration(now, now)).toBeNull()
    expect(getExpirationDateRelation(new Date(2026, 6, 30, 8, 59), now)).toBe('expired')
    expect(getRemainingExpiryDuration(new Date(2026, 6, 30, 8, 59), now)).toBeNull()
  })

  /**
   * Either the target or reference Date is invalid. Both expiry helpers return null,
   * keeping malformed timestamps from producing a calendar label or remaining-time
   * object.
   */
  it('rejects invalid target and current dates', () => {
    const invalid = new Date('invalid')
    const valid = new Date(2026, 6, 30, 9, 0)

    expect(getExpirationDateRelation(invalid, valid)).toBeNull()
    expect(getExpirationDateRelation(valid, invalid)).toBeNull()
    expect(getRemainingExpiryDuration(invalid, valid)).toBeNull()
    expect(getRemainingExpiryDuration(valid, invalid)).toBeNull()
  })

  /**
   * Expiry lies below twenty-four hours and includes fractional-minute edges. Remaining
   * duration uses hours and rounded-up minutes, keeping even a one-millisecond future
   * target visible.
   */
  it('returns rounded-up hours and minutes for an expiry under 24 hours away', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getRemainingExpiryDuration(new Date(2026, 6, 31, 8, 30), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 23,
      minutes: 30,
    })
    expect(getRemainingExpiryDuration(new Date(now.getTime() + 1), now)).toEqual({
      unit: 'hoursMinutes',
      hours: 0,
      minutes: 1,
    })
    expect(
      getRemainingExpiryDuration(new Date(now.getTime() + 23 * 60 * 60 * 1000 + 1), now),
    ).toEqual({
      unit: 'hoursMinutes',
      hours: 23,
      minutes: 1,
    })
  })

  /**
   * Targets equal twenty-four hours and exceed it by one millisecond. Compact expiry
   * output uses one day at the boundary and two rounded-up days just afterward.
   */
  it('preserves rounded-up day display from 24 hours onward', () => {
    const now = new Date(2026, 6, 30, 9, 0)

    expect(getRemainingExpiryDuration(new Date(now.getTime() + 24 * 60 * 60 * 1000), now)).toEqual({
      unit: 'days',
      days: 1,
    })
    expect(
      getRemainingExpiryDuration(new Date(now.getTime() + 24 * 60 * 60 * 1000 + 1), now),
    ).toEqual({
      unit: 'days',
      days: 2,
    })
  })
})

describe('highest subscription quota exhaustion', () => {
  /**
   * Monthly quota is configured alongside exhausted or available shorter windows.
   * Monthly usage determines the result, including exhaustion exactly when it reaches
   * the monthly limit.
   */
  it('checks monthly quota before lower windows', () => {
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: 100,
        monthly_usage_usd: 99,
        weekly_limit_usd: 10,
        weekly_usage_usd: 10,
        daily_limit_usd: 1,
        daily_usage_usd: 1,
      }),
    ).toBe(false)
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: 100,
        monthly_usage_usd: 100,
        weekly_limit_usd: 10,
        weekly_usage_usd: 0,
        daily_limit_usd: 1,
        daily_usage_usd: 0,
      }),
    ).toBe(true)
  })

  /**
   * Higher quota windows are absent while weekly or daily limits remain configured.
   * Exhaustion selects the first available lower window and compares its own usage to
   * its limit.
   */
  it('falls back to weekly and daily quotas when higher windows are absent', () => {
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: null,
        monthly_usage_usd: 100,
        weekly_limit_usd: 10,
        weekly_usage_usd: 10,
        daily_limit_usd: 1,
        daily_usage_usd: 0,
      }),
    ).toBe(true)
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: null,
        monthly_usage_usd: 100,
        weekly_limit_usd: null,
        weekly_usage_usd: 10,
        daily_limit_usd: 1,
        daily_usage_usd: 1,
      }),
    ).toBe(true)
  })

  /**
   * Shorter windows are exhausted, or all candidate limits are zero, missing, or
   * infinite. The helper leaves overall exhaustion false until a finite positive
   * highest window is exhausted.
   */
  it('does not mark unlimited or lower-only exhaustion as revocable', () => {
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: null,
        monthly_usage_usd: 0,
        weekly_limit_usd: 10,
        weekly_usage_usd: 9,
        daily_limit_usd: 1,
        daily_usage_usd: 1,
      }),
    ).toBe(false)
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: 0,
        monthly_usage_usd: 10,
        weekly_limit_usd: 0,
        weekly_usage_usd: 10,
        daily_limit_usd: null,
        daily_usage_usd: 1,
      }),
    ).toBe(false)
    expect(
      highestQuotaExhausted({
        monthly_limit_usd: Number.POSITIVE_INFINITY,
        monthly_usage_usd: Number.POSITIVE_INFINITY,
        weekly_limit_usd: null,
        weekly_usage_usd: 0,
        daily_limit_usd: null,
        daily_usage_usd: 0,
      }),
    ).toBe(false)
  })
})
