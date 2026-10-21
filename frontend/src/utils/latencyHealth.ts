/**
 * latencyHealth.ts assigns request latency severity and theme classes for usage tables.
 * First-token and total durations use separate millisecond thresholds, and each
 * threshold is inclusive. Text, solid bars, and gradient endpoints share the same four
 * severity names.
 */

/**
 * LatencySeverity names the four ordered visual categories for observed latency.
 */
export type LatencySeverity = 'good' | 'warn' | 'slow' | 'critical'

/**
 * FIRST_TOKEN_THRESHOLDS_MS and DURATION_THRESHOLDS_MS supply inclusive millisecond
 * thresholds, with longer intervals permitted for total request duration.
 */
export const FIRST_TOKEN_THRESHOLDS_MS = {
  warn: 10_000,
  slow: 30_000,
  critical: 60_000,
} as const

export const DURATION_THRESHOLDS_MS = {
  warn: 60_000,
  slow: 180_000,
  critical: 300_000,
} as const

/**
 * Thresholds supplies inclusive millisecond cutoffs for the three elevated severities.
 */
interface Thresholds {
  warn: number // warning threshold milliseconds
  slow: number // slow threshold milliseconds
  critical: number // critical threshold milliseconds
}

/**
 * classify selects the highest threshold reached by a duration in milliseconds. Callers
 * supply ordered thresholds; values below every threshold, including negative values
 * and NaN, receive the good category.
 */
const classify = (ms: number, thresholds: Thresholds): LatencySeverity => {
  /**
   * Check descending severity so an exceeded critical cutoff wins over lower cutoffs.
   */
  if (ms >= thresholds.critical) return 'critical'
  if (ms >= thresholds.slow) return 'slow'
  if (ms >= thresholds.warn) return 'warn'
  return 'good'
}

/**
 * firstTokenSeverity classifies first-token latency using the shared millisecond
 * thresholds.
 */
export const firstTokenSeverity = (ms: number): LatencySeverity =>
  classify(ms, FIRST_TOKEN_THRESHOLDS_MS)

/**
 * durationSeverity classifies total request duration using the shared millisecond
 * thresholds.
 */
export const durationSeverity = (ms: number): LatencySeverity =>
  classify(ms, DURATION_THRESHOLDS_MS)

/**
 * LATENCY_TEXT_CLASSES assigns severity text colors for both light and dark themes
 * wherever numeric latency values appear.
 */
export const LATENCY_TEXT_CLASSES: Record<LatencySeverity, string> = {
  good: 'text-emerald-600 dark:text-emerald-400',
  warn: 'text-amber-600 dark:text-amber-400',
  slow: 'text-orange-600 dark:text-orange-400',
  critical: 'text-red-600 dark:text-red-400',
}

/**
 * LATENCY_BAR_CLASSES assigns solid severity backgrounds when a latency bar displays a
 * single severity rather than two endpoints.
 */
export const LATENCY_BAR_CLASSES: Record<LatencySeverity, string> = {
  good: 'bg-emerald-500',
  warn: 'bg-amber-400',
  slow: 'bg-orange-500',
  critical: 'bg-red-500',
}

/**
 * LATENCY_BAR_FROM_CLASSES supplies first-token gradient starts that combine with
 * duration colors from LATENCY_BAR_TO_CLASSES on the same latency bar.
 */
export const LATENCY_BAR_FROM_CLASSES: Record<LatencySeverity, string> = {
  good: 'from-emerald-500',
  warn: 'from-amber-400',
  slow: 'from-orange-500',
  critical: 'from-red-500',
}

/**
 * LATENCY_BAR_TO_CLASSES supplies total-duration gradient ends that combine with
 * first-token colors to represent both latency observations together.
 */
export const LATENCY_BAR_TO_CLASSES: Record<LatencySeverity, string> = {
  good: 'to-emerald-500',
  warn: 'to-amber-400',
  slow: 'to-orange-500',
  critical: 'to-red-500',
}
