// latencyBuckets.ts validates and serializes the five duration boundaries used
// by the Ops histogram. URL values use decimal milliseconds in ascending order;
// invalid lists return null so the dashboard can try stored preferences and
// defaults.

// DEFAULT_LATENCY_BUCKET_BOUNDARIES supplies the five millisecond cutoffs used
// when no valid preference exists.
export const DEFAULT_LATENCY_BUCKET_BOUNDARIES = [100, 200, 500, 1000, 2000] as const
// MAX_LATENCY_BUCKET_BOUNDARY_MS caps each histogram boundary at one day
// expressed in milliseconds.
export const MAX_LATENCY_BUCKET_BOUNDARY_MS = 86_400_000
// LATENCY_BUCKET_STORAGE_KEY identifies the browser preference read and written
// by the Ops dashboard.
export const LATENCY_BUCKET_STORAGE_KEY = 'ops-latency-bucket-boundaries'

// defaultLatencyBucketBoundaries returns a mutable copy of the standard
// histogram boundaries. Each caller receives its own array so editing a dialog
// draft cannot modify the shared defaults.
export function defaultLatencyBucketBoundaries(): number[] {
  return [...DEFAULT_LATENCY_BUCKET_BOUNDARIES]
}

// normalizeLatencyBucketBoundaries converts exactly five values into ascending
// integer milliseconds. It returns null for nonpositive, oversized, fractional,
// or unordered boundaries, and returns a new array on success.
export function normalizeLatencyBucketBoundaries(values: readonly unknown[]): number[] | null {
  if (values.length !== DEFAULT_LATENCY_BUCKET_BOUNDARIES.length) return null

  const normalized = values.map((value) => Number(value))
  for (let index = 0; index < normalized.length; index += 1) {
    const boundary = normalized[index]
    if (!Number.isInteger(boundary) || boundary <= 0 || boundary > MAX_LATENCY_BUCKET_BOUNDARY_MS) {
      return null
    }
    if (index > 0 && boundary <= normalized[index - 1]) {
      return null
    }
  }
  return normalized
}

// parseLatencyBucketBoundaries decodes a comma-separated decimal boundary list
// from storage or the URL. Blank input, nondecimal tokens, and lists rejected by
// normalizeLatencyBucketBoundaries return null for caller-selected defaults.
export function parseLatencyBucketBoundaries(raw: string): number[] | null {
  if (!raw.trim()) return null
  const parts = raw.split(',').map((value) => value.trim())
  if (parts.some((value) => !/^\d+$/.test(value))) return null
  return normalizeLatencyBucketBoundaries(parts)
}

// serializeLatencyBucketBoundaries joins supplied boundaries for storage and
// query parameters. It performs no validation, so callers validate or supply the
// standard defaults before publishing the resulting comma-separated value.
export function serializeLatencyBucketBoundaries(boundaries: readonly number[]): string {
  return boundaries.join(',')
}

// areLatencyBucketBoundariesEqual compares boundary count and each ordered
// numeric value.
export function areLatencyBucketBoundariesEqual(
  left: readonly number[],
  right: readonly number[],
): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index])
}

// areDefaultLatencyBucketBoundaries reports whether boundaries exactly match the
// standard sequence.
export function areDefaultLatencyBucketBoundaries(boundaries: readonly number[]): boolean {
  return areLatencyBucketBoundariesEqual(boundaries, DEFAULT_LATENCY_BUCKET_BOUNDARIES)
}
