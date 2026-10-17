import type { UsageLog } from '@/types'

// usageTps.ts formats the throughput derived from persisted upstream observations.
// The server owns the content interval and completeness checks. Pages and exports
// read the same value at one decimal place, independent of the TTFT mode.

// calculateUsageTps returns the server estimate when it is finite and nonnegative.
// Missing or invalid values remain absent rather than using another latency metric.
export function calculateUsageTps(row: Partial<Pick<UsageLog, 'tps'>>): number | null {
  return typeof row.tps === 'number' && Number.isFinite(row.tps) && row.tps >= 0 ? row.tps : null
}

// formatUsageTps renders the shared estimate with its token-per-second unit.
export function formatUsageTps(row: Partial<Pick<UsageLog, 'tps'>>): string {
  const value = calculateUsageTps(row)
  return value === null ? '-' : `${value.toFixed(1)} tok/s`
}
