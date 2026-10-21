/**
 * proxyExpiry.ts generates proxy expiry badge classes and translation parameters from
 * expiry dates and status. Remaining days are rounded upward from elapsed milliseconds.
 * Explicit expired status wins over the date, while three-day and seven-day thresholds
 * select warning colors.
 */

/**
 * EXPIRY_WARN_DAYS and EXPIRY_DANGER_DAYS define inclusive visual warning thresholds
 * shared by proxy expiry labels and badge colors.
 */
export const EXPIRY_WARN_DAYS = 7
export const EXPIRY_DANGER_DAYS = 3

/**
 * daysUntil returns the ceiling of the remaining interval measured in twenty-four-hour
 * days. It reads Date.now on every call, and invalid date strings produce NaN for the
 * calling display helper.
 */
export const daysUntil = (iso: string): number =>
  /**
   * Round partial days upward for the shared urgency thresholds.
   */
  Math.ceil((new Date(iso).getTime() - Date.now()) / 86400000)

/**
 * proxyExpiryBadgeClass selects expiry urgency classes, with expired status taking
 * precedence. Missing dates act as infinite remaining time; invalid dates fail both
 * threshold comparisons and receive the neutral class.
 */
export function proxyExpiryBadgeClass(expiresAt: string | null, status?: string): string {
  if (status === 'expired') return 'badge badge-danger'
  const d = expiresAt ? daysUntil(expiresAt) : Infinity
  if (d <= EXPIRY_DANGER_DAYS) return 'badge badge-danger'
  if (d <= EXPIRY_WARN_DAYS) return 'badge badge-warning'
  return 'text-gray-500'
}

/**
 * proxyExpiryLabelKey returns translation metadata for an expiry state, prioritizing
 * explicit expired status. Other inputs use rounded remaining days; absent dates yield
 * Infinity and invalid dates yield NaN in the remainingDays parameters.
 */
export function proxyExpiryLabelKey(
  expiresAt: string | null,
  status?: string,
): {
  key: string // translation lookup key
  /** params carries the day count for relative labels and is omitted for explicit expired status. */
  params?: { days: number /* remaining or overdue days */ }
} {
  if (status === 'expired') return { key: 'admin.proxies.expired' }
  const d = expiresAt ? daysUntil(expiresAt) : Infinity
  if (d < 0) return { key: 'admin.proxies.overdueDays', params: { days: Math.abs(d) } }
  if (d <= EXPIRY_WARN_DAYS) return { key: 'admin.proxies.expiringInDays', params: { days: d } }
  return { key: 'admin.proxies.remainingDays', params: { days: d } }
}
