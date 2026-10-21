import type { UserSubscription } from '@/api/subscriptions.types'

/**
 * subscriptionQuota.ts derives subscription expiry labels and exhaustion state from
 * supplied dates and quota windows. Duration helpers use elapsed milliseconds with
 * separate rounding rules: detailed parts discard incomplete minutes, while compact
 * expiry labels round remaining minutes or days upward. Invalid dates and elapsed
 * targets return null where no remaining duration exists.
 *
 * Calendar labels compare local calendar components represented as UTC midnights, so a
 * daylight-saving transition does not turn tomorrow into a fractional-day comparison.
 * Quota exhaustion selects the highest configured positive finite window in monthly,
 * weekly, daily order. Usage in a shorter window cannot override the selected longer
 * window's exhaustion result.
 */

/**
 * ONE_DAY_MS represents a fixed twenty-four-hour duration for subscription length and
 * remaining-time calculations, distinct from local calendar-day comparison.
 */
const ONE_DAY_MS = 24 * 60 * 60 * 1000

/**
 * ExpirationDateRelation identifies expiry relative to now and its local calendar date.
 */
export type ExpirationDateRelation = 'expired' | 'today' | 'tomorrow' | 'later'

/**
 * RemainingExpiryDuration represents rounded days or an hours-and-minutes expiry label.
 */
export type RemainingExpiryDuration =
  | {
      /**
       * unit and days describe intervals of at least twenty-four hours. Partial
       * remaining days round upward so the compact expiry label includes the
       * unfinished day.
       */
      unit: 'days' // rounded day display
      days: number // rounded-up remaining days
    }
  | {
      /**
       * unit, hours, and minutes describe intervals below twenty-four hours. The
       * whole remaining interval rounds upward to minutes before splitting into
       * hour and minute components.
       */
      unit: 'hoursMinutes' // subday minute display
      hours: number // remaining hour component
      minutes: number // remaining minute component
    }

/**
 * RemainingDurationParts divides completed remaining minutes into days, hours, and
 * minutes.
 */
export interface RemainingDurationParts {
  /**
   * days, hours, and minutes divide the remaining interval after incomplete
   * minutes are discarded. Hours and minutes are remainders within their enclosing
   * day and hour.
   */
  days: number // complete remaining days
  hours: number // remaining hour component
  minutes: number // remaining minute component
}

/**
 * isOneTimeDailyQuota reports whether valid subscription endpoints span at most
 * twenty-four hours. Missing or invalid endpoints return false; the comparison also
 * accepts reversed endpoints because no positive-duration check is applied.
 */
export function isOneTimeDailyQuota(
  subscription: Pick<UserSubscription, 'starts_at' | 'expires_at'>,
): boolean {
  if (!subscription.starts_at || !subscription.expires_at) return false

  const startsAt = new Date(subscription.starts_at).getTime()
  const expiresAt = new Date(subscription.expires_at).getTime()

  if (!Number.isFinite(startsAt) || !Number.isFinite(expiresAt)) return false

  return expiresAt <= startsAt + ONE_DAY_MS
}

/**
 * getRemainingDurationParts divides a positive remaining interval into complete days,
 * hours, and minutes. Invalid dates and elapsed targets return null; the optional
 * reference date defaults to the current time.
 */
export function getRemainingDurationParts(
  targetAt: Date | string,
  now: Date = new Date(),
): RemainingDurationParts | null {
  const targetTime = targetAt instanceof Date ? targetAt.getTime() : new Date(targetAt).getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null

  const diffMs = targetTime - nowTime
  if (diffMs <= 0) return null

  /**
   * Discard partial minutes before decomposing the elapsed duration into parts.
   */
  const totalMinutes = Math.floor(diffMs / (1000 * 60))
  const days = Math.floor(totalMinutes / (24 * 60))
  const hours = Math.floor((totalMinutes % (24 * 60)) / 60)
  const minutes = totalMinutes % 60

  return { days, hours, minutes }
}

/**
 * getExpirationDateRelation classifies a valid expiry instant as expired, today,
 * tomorrow, or later. Expiry at or before the supplied reference instant is expired,
 * and invalid inputs return null.
 *
 * Future instants are compared by their local year, month, and day components expressed
 * as UTC midnights. This keeps calendar-day classification independent of the actual
 * elapsed hours across daylight-saving transitions. The optional reference date
 * defaults to now, and the input Date objects remain unchanged.
 */
export function getExpirationDateRelation(
  targetAt: Date | string,
  now: Date = new Date(),
): ExpirationDateRelation | null {
  const target = targetAt instanceof Date ? targetAt : new Date(targetAt)
  const targetTime = target.getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null
  if (targetTime <= nowTime) return 'expired'

  /**
   * Represent local calendar dates as UTC midnights so offset changes cannot alter the
   * number of calendar-day steps between them.
   */
  const targetDay = Date.UTC(target.getFullYear(), target.getMonth(), target.getDate())
  const currentDay = Date.UTC(now.getFullYear(), now.getMonth(), now.getDate())
  /**
   * Convert the calendar-midnight difference to a whole day count.
   */
  const calendarDays = Math.round((targetDay - currentDay) / ONE_DAY_MS)

  if (calendarDays === 0) return 'today'
  if (calendarDays === 1) return 'tomorrow'
  return 'later'
}

/**
 * getRemainingExpiryDuration returns rounded-up days for intervals of at least
 * twenty-four hours and rounded-up minutes otherwise. Invalid dates and elapsed targets
 * return null; the optional reference date defaults to now.
 */
export function getRemainingExpiryDuration(
  targetAt: Date | string,
  now: Date = new Date(),
): RemainingExpiryDuration | null {
  const targetTime = targetAt instanceof Date ? targetAt.getTime() : new Date(targetAt).getTime()
  const nowTime = now.getTime()

  if (!Number.isFinite(targetTime) || !Number.isFinite(nowTime)) return null

  const diffMs = targetTime - nowTime
  if (diffMs <= 0) return null
  if (diffMs >= ONE_DAY_MS) {
    /**
     * Round partial days upward for the compact subscription expiry label.
     */
    return { unit: 'days', days: Math.ceil(diffMs / ONE_DAY_MS) }
  }

  /**
   * Round partial minutes upward so a future expiry keeps a positive countdown.
   */
  const totalMinutes = Math.ceil(diffMs / (60 * 1000))
  return {
    unit: 'hoursMinutes',
    hours: Math.floor(totalMinutes / 60),
    minutes: totalMinutes % 60,
  }
}

/**
 * highestQuotaExhausted checks usage against the highest configured positive finite
 * limit. Windows are considered in monthly, weekly, then daily order, and a missing or
 * nonpositive limit lets the next window participate.
 *
 * Once a window is selected, its usage alone decides exhaustion with an inclusive
 * comparison. This means an exhausted daily allocation does not report overall
 * exhaustion when a positive monthly limit remains available. If no window qualifies,
 * the function returns false; it does not independently validate the selected usage
 * amount.
 */
export function highestQuotaExhausted(
  subscription: Pick<
    UserSubscription,
    | 'monthly_limit_usd'
    | 'monthly_usage_usd'
    | 'weekly_limit_usd'
    | 'weekly_usage_usd'
    | 'daily_limit_usd'
    | 'daily_usage_usd'
  >,
): boolean {
  const windows = [
    { limit: subscription.monthly_limit_usd, used: subscription.monthly_usage_usd },
    { limit: subscription.weekly_limit_usd, used: subscription.weekly_usage_usd },
    { limit: subscription.daily_limit_usd, used: subscription.daily_usage_usd },
  ]
  /**
   * Select the longest configured positive finite quota window first because shorter
   * exhausted windows can reset while that window remains available.
   */
  const highest = windows.find(
    (window) => window.limit != null && Number.isFinite(window.limit) && window.limit > 0,
  )
  return highest != null && highest.limit != null && highest.used >= highest.limit
}
