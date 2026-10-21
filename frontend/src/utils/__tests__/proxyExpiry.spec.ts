import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { daysUntil, proxyExpiryBadgeClass, proxyExpiryLabelKey } from '../proxyExpiry'

/**
 * proxyExpiry.spec.ts tests proxy expiry labels and badge urgency against a
 * fixed clock. Whole-day offsets exercise future and overdue dates, explicit
 * expired status takes precedence, and three-day and seven-day boundaries remain
 * inclusive.
 */

/**
 * NOW fixes the reference instant for expiry tests, while isoInDays derives future and
 * past timestamps from the same clock.
 */
const NOW = new Date('2026-06-02T00:00:00Z')
/**
 * isoInDays returns an ISO timestamp offset from the fixed test clock by whole
 * twenty-four-hour days. Positive and negative offsets supply deterministic future and
 * overdue proxy fixtures.
 */
const isoInDays = (n: number): string =>
  // Fixed elapsed-day offsets keep fixture instants independent of calendar transitions.
  new Date(NOW.getTime() + n * 86400000).toISOString()

/**
 * Install the fake clock before each case so Date.now and derived expiry
 * comparisons share the same fixed reference instant.
 */
beforeEach(() => {
  vi.useFakeTimers()
  vi.setSystemTime(NOW)
})
/**
 * Restore real timers after each case so this fixed expiry-test clock cannot
 * affect other tests that read the current time.
 */
afterEach(() => {
  vi.useRealTimers()
})

describe('daysUntil', () => {
  /**
   * The clock is fixed and target timestamps differ by whole positive or negative days.
   * The remaining-day helper returns the corresponding signed day counts exactly.
   */
  it('返回距今整天数', () => {
    expect(daysUntil(isoInDays(10))).toBe(10)
    expect(daysUntil(isoInDays(-3))).toBe(-3)
  })
})

describe('proxyExpiryBadgeClass', () => {
  /**
   * A proxy has expired status despite a future date thirty days away. Status takes
   * precedence and selects the danger badge regardless of that future timestamp.
   */
  it('status=expired → danger', () => {
    expect(proxyExpiryBadgeClass(isoInDays(30), 'expired')).toBe('badge badge-danger')
  })
  /**
   * Active proxies have two or exactly three days remaining. Both receive the danger
   * badge, protecting inclusion of the three-day threshold in the highest urgency
   * category.
   */
  it('≤3 天 → danger（含边界 3）', () => {
    expect(proxyExpiryBadgeClass(isoInDays(2), 'active')).toBe('badge badge-danger')
    expect(proxyExpiryBadgeClass(isoInDays(3), 'active')).toBe('badge badge-danger')
  })
  /**
   * Active proxies have five or exactly seven days remaining. Both receive the warning
   * badge, protecting inclusion of the seven-day boundary in the warning interval.
   */
  it('4–7 天 → warning（含边界 7）', () => {
    expect(proxyExpiryBadgeClass(isoInDays(5), 'active')).toBe('badge badge-warning')
    expect(proxyExpiryBadgeClass(isoInDays(7), 'active')).toBe('badge badge-warning')
  })
  /**
   * An active proxy has thirty days remaining under the fixed clock. Its badge uses
   * neutral gray classes because it lies beyond both expiry warning thresholds.
   */
  it('>7 天 → gray', () => {
    expect(proxyExpiryBadgeClass(isoInDays(30), 'active')).toBe('text-gray-500')
  })
})

describe('proxyExpiryLabelKey', () => {
  /**
   * Expired status accompanies an otherwise future date. The label selects the explicit
   * expired translation key and omits remaining-day parameters for that terminal
   * status.
   */
  it('status=expired → expired key', () => {
    expect(proxyExpiryLabelKey(isoInDays(30), 'expired')).toEqual({ key: 'admin.proxies.expired' })
  })
  /**
   * An active proxy expired three whole days before the fixed clock. The label chooses
   * overdueDays and supplies the positive magnitude of the overdue interval.
   */
  it('已逾期(d<0) → overdueDays', () => {
    expect(proxyExpiryLabelKey(isoInDays(-3), 'active')).toEqual({
      key: 'admin.proxies.overdueDays',
      params: { days: 3 },
    })
  })
  /**
   * An active proxy expires within the seven-day warning interval. The label chooses
   * expiringInDays and carries the remaining count for the caller to translate.
   */
  it('≤7 天 → expiringInDays', () => {
    expect(proxyExpiryLabelKey(isoInDays(5), 'active')).toEqual({
      key: 'admin.proxies.expiringInDays',
      params: { days: 5 },
    })
  })
  /**
   * An active proxy expires beyond the warning interval. The label chooses
   * remainingDays and carries the full positive day count for ordinary expiry display.
   */
  it('>7 天 → remainingDays', () => {
    expect(proxyExpiryLabelKey(isoInDays(30), 'active')).toEqual({
      key: 'admin.proxies.remainingDays',
      params: { days: 30 },
    })
  })
})
