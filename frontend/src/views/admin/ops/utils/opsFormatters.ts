import type { OpsSeverity } from '@/api/admin/ops'
import { formatBytes } from '@/utils/format'

// opsFormatters.ts formats numeric summaries, timestamps, and severity styling
// for Ops views. Missing measurements remain visibly absent where appropriate,
// and API memory values are converted from binary megabyte units before the
// shared byte formatter chooses a display unit.

// getSeverityClass returns the severity badge palette used by Ops tables.
// Unrecognized or empty severity values select the P3 palette, allowing callers
// to display a neutral badge for unsupported codes.
export function getSeverityClass(severity: OpsSeverity): string {
  const classes: Record<string, string> = {
    P0: 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-400',
    P1: 'bg-orange-100 text-orange-800 dark:bg-orange-900/30 dark:text-orange-400',
    P2: 'bg-yellow-100 text-yellow-800 dark:bg-yellow-900/30 dark:text-yellow-400',
    P3: 'bg-blue-100 text-blue-800 dark:bg-blue-900/30 dark:text-blue-400',
  }
  return classes[String(severity || '')] || classes.P3
}

// formatDateTime renders a timestamp as local month-day and time through
// seconds. Invalid dates return an empty string so callers can decide how an
// unavailable timestamp appears.
export function formatDateTime(dateStr: string): string {
  const d = new Date(dateStr)
  if (Number.isNaN(d.getTime())) return ''
  return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}:${String(d.getSeconds()).padStart(2, '0')}`
}

// sumNumbers adds finite numeric observations and treats missing or invalid
// entries as zero. It accepts nullable series from API results and returns zero
// for an empty series.
export function sumNumbers(values: Array<number | null | undefined>): number {
  return values.reduce<number>((acc, v) => {
    // Treat nonfinite series entries as missing observations so a single invalid
    // sample does not contaminate the aggregate sum.
    const n = typeof v === 'number' && Number.isFinite(v) ? v : 0
    return acc + n
  }, 0)
}

// parseTimeRangeMinutes converts minute or hour suffixes into positive minutes.
// Numeric prefixes are parsed with parseInt; blank, unsupported, or nonpositive
// values return sixty minutes for callers that need a relative window.
export function parseTimeRangeMinutes(range: string): number {
  const trimmed = (range || '').trim()
  if (!trimmed) return 60
  if (trimmed.endsWith('m')) {
    const v = Number.parseInt(trimmed.slice(0, -1), 10)
    return Number.isFinite(v) && v > 0 ? v : 60
  }
  // Hour suffixes convert to minutes for shared window arithmetic.
  if (trimmed.endsWith('h')) {
    const v = Number.parseInt(trimmed.slice(0, -1), 10)
    return Number.isFinite(v) && v > 0 ? v * 60 : 60
  }
  return 60
}

// formatHistoryLabel produces local-time chart labels for a requested history
// window. Missing or invalid dates return an empty label; windows of at least
// one day include month and day.
export function formatHistoryLabel(date: string | undefined, timeRange: string): string {
  if (!date) return ''
  const d = new Date(date)
  if (Number.isNaN(d.getTime())) return ''
  const minutes = parseTimeRangeMinutes(timeRange)
  // Long windows include the date to distinguish repeated clock labels.
  if (minutes >= 24 * 60) {
    return `${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
  }
  return `${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

// formatMemorySizeMB formats API capacities measured in units of 1024 squared
// bytes. Missing, nonfinite, or negative inputs return a dash, while zero
// retains an explicit MB label.
export function formatMemorySizeMB(value: number | null | undefined): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '-'
  if (value === 0) return '0 MB'

  // Restore bytes from API binary-megabyte capacity measurements.
  return formatBytes(value * 1024 * 1024, 1)
}
