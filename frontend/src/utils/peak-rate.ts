/**
 * peak-rate.ts builds peak-rate window labels from settings and an optional server
 * timezone offset. Display eligibility requires an enabled flag and both clock strings.
 * Labels retain those strings as supplied and annotate the server timezone when callers
 * provide it.
 */

/**
 * PeakRateFields carries the configured window and multiplier for peak-rate labels.
 */
export interface PeakRateFields {
  /**
   * peak_rate_enabled, peak_start, and peak_end jointly enable window display
   * through truthiness. The formatter shows supplied clock strings after that
   * check and performs no calendar conversion.
   */
  peak_rate_enabled?: boolean // window display enabled
  peak_start?: string // configured start clock
  peak_end?: string // configured end clock
  /**
   * peak_rate_multiplier defaults to one when nullish in formatPeakRateWindow; a
   * configured zero remains visible as the displayed multiplier.
   */
  peak_rate_multiplier?: number // peak price multiplier
}

/**
 * hasPeakRate reports an enabled peak-rate setting with both start and end text
 * present. It checks truthiness rather than clock syntax or multiplier validity,
 * leaving those concerns to configuration validation.
 */
export function hasPeakRate(fields?: PeakRateFields | null): boolean {
  return Boolean(fields?.peak_rate_enabled && fields.peak_start && fields.peak_end)
}

/**
 * serverTimezoneLabel prefixes a supplied UTC offset with UTC for display. Nullish and
 * empty offsets produce empty text, allowing labels from settings without an offset to
 * omit the timezone annotation.
 */
export function serverTimezoneLabel(utcOffset?: string | null): string {
  return utcOffset ? `UTC${utcOffset}` : ''
}

/**
 * currentServerTimezoneLabel reads the server UTC offset from the browser's injected
 * application settings. Server-side execution or missing settings returns empty text
 * through serverTimezoneLabel, so callers can omit that annotation.
 */
export function currentServerTimezoneLabel(): string {
  const settings = typeof window !== 'undefined' ? window.__APP_CONFIG__ : undefined
  return serverTimezoneLabel(settings?.server_utc_offset)
}

/**
 * formatPeakRateWindow renders an enabled peak window and its multiplier, optionally
 * followed by a timezone label. Incomplete windows return empty text; a nullish
 * multiplier becomes one, while a supplied zero remains zero.
 */
export function formatPeakRateWindow(
  fields: PeakRateFields | null | undefined,
  tzLabel?: string,
): string {
  if (!hasPeakRate(fields) || !fields) return ''
  const base = `${fields.peak_start}-${fields.peak_end} ×${fields.peak_rate_multiplier ?? 1}`
  return tzLabel ? `${base} (${tzLabel})` : base
}
