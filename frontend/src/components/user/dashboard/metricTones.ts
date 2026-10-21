// metricTones.ts assigns icon and tile colors to dashboard metric categories. Metric cards
// and the heading statistics share these values, and complete class strings let the Tailwind
// scanner retain every light and dark color variant.

// METRIC_TONES supplies complete Tailwind class strings for matching metric icons and tiles
// across dashboard cards and live statistics.

export const METRIC_TONES = {
  // Requests and RPM use the brand color shared by the request line.
  requests: { icon: 'text-primary-600 dark:text-primary-500', tile: 'bg-primary-500/10' },
  // Token volume and TPM.
  tokens: { icon: 'text-blue-500 dark:text-blue-400', tile: 'bg-blue-500/10' },
  // Period and daily spending.
  cost: { icon: 'text-amber-500 dark:text-amber-400', tile: 'bg-amber-500/10' },
  // Cache-hit rate matches the violet chart line.
  cacheHitRate: { icon: 'text-violet-500 dark:text-violet-400', tile: 'bg-violet-500/10' },
  // Average request latency.
  latency: { icon: 'text-rose-500 dark:text-rose-400', tile: 'bg-rose-500/10' },
} as const

// MetricTone identifies a metric with matching dashboard icon and tile colors.
export type MetricTone = keyof typeof METRIC_TONES
