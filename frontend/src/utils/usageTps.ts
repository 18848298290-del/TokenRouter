import type { UsageLog } from '@/api/usage.types'

/**
 * usageTps.ts formats the throughput observation recorded on a usage row. Finite
 * nonnegative values, including zero, remain available to consumers, while absent or
 * invalid values become a dash. The display rounds that recorded tokens-per-second
 * value to one fractional digit.
 */

/**
 * calculateUsageTps returns the recorded tokens-per-second value when it is finite and
 * nonnegative. Missing, negative, and nonnumeric observations return null, keeping an
 * unavailable measurement distinct from a measured zero.
 */
export function calculateUsageTps(row: Partial<Pick<UsageLog, 'tps'>>): number | null {
  return typeof row.tps === 'number' && Number.isFinite(row.tps) && row.tps >= 0 ? row.tps : null
}

/**
 * formatUsageTps renders the accepted recorded throughput with one fractional digit and
 * the tok/s unit. Missing or invalid values produce a dash through calculateUsageTps's
 * shared validation rules.
 */
export function formatUsageTps(row: Partial<Pick<UsageLog, 'tps'>>): string {
  const value = calculateUsageTps(row)
  /**
   * Round the recorded observation to one decimal for the shared throughput display.
   */
  return value === null ? '-' : `${value.toFixed(1)} tok/s`
}
