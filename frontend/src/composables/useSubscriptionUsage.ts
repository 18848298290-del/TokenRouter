import { useI18n } from 'vue-i18n'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import type { UserSubscription } from '@/api/subscriptions.types'
import { formatDateTimeToMinute } from '@/utils/format'

// useSubscriptionUsage.ts formats subscription quota windows and chooses their urgency
// presentation. Only positive configured limits participate in quota comparisons, so
// subscriptions without such windows display as unlimited. The highest usage percentage
// controls ordering and status dots across daily, weekly, and monthly windows.
//
// Amount formatting delegates to the configured balance display, while expiration
// formatting uses localized minute-resolution dates. Progress width is capped at one
// hundred percent, but usage percentages used for sorting retain values above the limit.
// Remaining-day colors round partial days upward, with distinct three-day and seven-day
// warning thresholds.

// SubscriptionUsageWindow pairs a subscription period's usage with its configured limit.
export interface SubscriptionUsageWindow {
  key: 'daily' | 'weekly' | 'monthly' // subscription quota period identifier
  label: string // localized quota period label
  used: number // consumed quota amount USD
  // limit is denominated in USD; null and nonpositive values are excluded when
  // usageWindows selects comparable quota periods.
  limit: number | null // nullable quota limit USD
}

// useSubscriptionUsage returns quota formatting and urgency helpers for subscription
// displays. Invoke it during setup with i18n available; amounts use configured balance
// presentation, and only positive quota windows participate in usage ranking.
export function useSubscriptionUsage() {
  const { t } = useI18n()
  const { formatBalanceAmount } = useBalanceDisplay()

  // usageWindows returns translated daily, weekly, and monthly windows with positive
  // limits. Missing usage counts become zero, while missing, zero, and negative limits
  // exclude that period from quota comparisons.
  function usageWindows(subscription: UserSubscription): SubscriptionUsageWindow[] {
    const windows: SubscriptionUsageWindow[] = [
      {
        key: 'daily',
        label: t('subscriptionProgress.daily'),
        used: subscription.daily_usage_usd || 0,
        limit: subscription.daily_limit_usd,
      },
      {
        key: 'weekly',
        label: t('subscriptionProgress.weekly'),
        used: subscription.weekly_usage_usd || 0,
        limit: subscription.weekly_limit_usd,
      },
      {
        key: 'monthly',
        label: t('subscriptionProgress.monthly'),
        used: subscription.monthly_usage_usd || 0,
        limit: subscription.monthly_limit_usd,
      },
    ]
    return windows.filter((window) => window.limit != null && window.limit > 0)
  }

  // getMaxUsagePercentage returns the largest percentage across configured positive quota
  // windows. Each window compares its own usage and limit, so daily exhaustion can
  // dominate even when monthly usage remains low.
  //
  // A subscription with no qualifying windows returns zero, matching its unlimited
  // presentation. Percentages are not capped here because sorting needs to distinguish
  // subscriptions already beyond their limits; only progress-bar width applies a display
  // cap. Missing usage contributes zero through usageWindows.
  function getMaxUsagePercentage(subscription: UserSubscription): number {
    const windows = usageWindows(subscription)
    if (windows.length === 0) return 0
    // Convert each window's fraction to percentage points for cross-window ranking.
    return Math.max(...windows.map((window) => ((window.used || 0) / (window.limit || 1)) * 100))
  }

  // isUnlimited reports whether the subscription has no positive configured quota limits.
  function isUnlimited(subscription: UserSubscription): boolean {
    return usageWindows(subscription).length === 0
  }

  // sortByUsage returns a new array ordered by descending maximum period usage percentage.
  // It leaves the supplied array order unchanged and compares the highest qualifying
  // window for each subscription.
  function sortByUsage(subscriptions: UserSubscription[]): UserSubscription[] {
    return [...subscriptions].sort((a, b) => getMaxUsagePercentage(b) - getMaxUsagePercentage(a))
  }

  // getProgressDotClass chooses the dot color from the subscription's highest qualifying
  // usage percentage. Unlimited subscriptions use emerald; configured windows select red
  // at ninety percent and orange at seventy.
  function getProgressDotClass(subscription: UserSubscription): string {
    if (isUnlimited(subscription)) return 'bg-emerald-500'
    const percentage = getMaxUsagePercentage(subscription)
    // Ninety percent begins the highest quota warning level.
    if (percentage >= 90) return 'bg-red-500'
    // Seventy percent begins the intermediate quota warning level.
    if (percentage >= 70) return 'bg-orange-500'
    return 'bg-green-500'
  }

  // getProgressBarClass chooses a bar color from one usage and limit pair. Falsy limits
  // use gray; other values use red at ninety percent, orange at seventy, and green below.
  function getProgressBarClass(used: number, limit: number | null): string {
    if (!limit || limit === 0) return 'bg-gray-400'
    // Convert the usage fraction to percentage points for warning comparisons.
    const percentage = (used / limit) * 100
    // Red marks at least ninety percent used; orange marks seventy through below ninety.
    if (percentage >= 90) return 'bg-red-500'
    if (percentage >= 70) return 'bg-orange-500'
    return 'bg-green-500'
  }

  // getProgressWidth returns a CSS percentage capped at one hundred for the usage pair.
  // Falsy limits return zero width, and callers supply nonnegative usage because negative
  // percentages are not clamped.
  function getProgressWidth(used: number, limit: number | null): string {
    if (!limit || limit === 0) return '0%'
    // Cap the visual bar while retaining over-limit usage elsewhere.
    return `${Math.min((used / limit) * 100, 100)}%`
  }

  // formatUsage renders used and limit amounts with two fractional digits. A null limit
  // displays the infinity symbol, while numeric limits use the same configured balance
  // formatting as usage.
  function formatUsage(used: number, limit: number | null): string {
    const usedValue = formatBalanceAmount(used, { fractionDigits: 2 })
    const limitValue = limit == null ? '∞' : formatBalanceAmount(limit, { fractionDigits: 2 })
    return `${usedValue}/${limitValue}`
  }

  // formatExpiration renders a localized subscription expiry label from a timestamp. If
  // the shared date formatter returns an empty result, this helper also returns an empty
  // display string.
  function formatExpiration(expiresAt: string): string {
    const time = formatDateTimeToMinute(expiresAt)
    return time ? t('subscriptionProgress.expiresAt', { time }) : ''
  }

  // getDaysRemainingClass selects urgency text classes from rounded-up days until
  // expiration. Three days or fewer, including expired subscriptions, use red; up to seven
  // days uses orange, and later dates use neutral text.
  function getDaysRemainingClass(expiresAt: string): string {
    // Round partial remaining days upward for expiry warnings.
    const days = Math.ceil((new Date(expiresAt).getTime() - Date.now()) / (1000 * 60 * 60 * 24))
    // Red covers three days or fewer; orange covers the remaining first week.
    if (days <= 3) return 'text-red-600 dark:text-red-400'
    if (days <= 7) return 'text-orange-600 dark:text-orange-400'
    return 'text-gray-500 dark:text-dark-400'
  }

  return {
    usageWindows,
    isUnlimited,
    sortByUsage,
    getProgressDotClass,
    getProgressBarClass,
    getProgressWidth,
    formatUsage,
    formatExpiration,
    getDaysRemainingClass,
  }
}
