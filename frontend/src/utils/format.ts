import { i18n, getLocale } from '@/i18n'

/**
 * format.ts formats dates, elapsed time, numeric counts, and reasoning effort for
 * shared frontend displays. Locale-aware functions read the application locale unless
 * their signature accepts an override. Compact token functions have distinct precision
 * and suffix rules, so consumers choose the representation appropriate to their table,
 * chart, or export.
 *
 * Local form values carry calendar components without a timezone. Parsing validates
 * those components before constructing a local Date, checks calendar overflow
 * afterward, and returns whole Unix seconds. Reasoning-effort mapping separately
 * compares normalized values and their display labels so missing historical values
 * remain readable alongside requested and forwarded settings.
 *
 * The formatting approach references the CRS project's format.js implementation.
 */

/**
 * formatRelativeTime translates elapsed time using the largest completed day, hour, or
 * minute. Missing, invalid, and future dates use the never label; elapsed times below
 * one minute use justNow.
 */
export function formatRelativeTime(date: string | Date | null | undefined): string {
  if (!date) return i18n.global.t('common.time.never')

  const now = new Date()
  const past = new Date(date)
  const diffMs = now.getTime() - past.getTime()

  /**
   * Future timestamps and failed date parsing have no elapsed interval, so the
   * relative-time display uses the same never label.
   */
  if (diffMs < 0 || isNaN(diffMs)) return i18n.global.t('common.time.never')

  /**
   * Discard partial seconds before selecting completed elapsed-time units.
   */
  const diffSecs = Math.floor(diffMs / 1000)
  /**
   * Floor elapsed units so partially completed minutes stay uncounted.
   */
  const diffMins = Math.floor(diffSecs / 60)
  const diffHours = Math.floor(diffMins / 60)
  const diffDays = Math.floor(diffHours / 24)

  if (diffDays > 0) return i18n.global.t('common.time.daysAgo', { n: diffDays })
  if (diffHours > 0) return i18n.global.t('common.time.hoursAgo', { n: diffHours })
  if (diffMins > 0) return i18n.global.t('common.time.minutesAgo', { n: diffMins })
  return i18n.global.t('common.time.justNow')
}

/**
 * formatNumber formats a number in the active application locale with at most one
 * fractional digit. Nullish values become zero, and absolute values of at least ten
 * thousand use locale-specific compact notation.
 */
export function formatNumber(num: number | null | undefined): string {
  if (num === null || num === undefined) return '0'

  const locale = getLocale()
  const absNum = Math.abs(num)

  /**
   * Compact notation follows locale conventions, including Chinese ten-thousand units;
   * the threshold keeps smaller values in standard numeric notation.
   */
  const formatter = new Intl.NumberFormat(locale, {
    notation: absNum >= 10000 ? 'compact' : 'standard',
    maximumFractionDigits: 1,
  })

  return formatter.format(num)
}

/**
 * formatBytes formats positive byte counts using powers of 1024 and the listed
 * binary-size labels. Zero returns 0 B; negative decimal precision becomes zero, while
 * other precision values pass to toFixed.
 */
export function formatBytes(bytes: number, decimals: number = 2): string {
  if (bytes === 0) return '0 B'

  /**
   * Use binary byte multiples while retaining familiar KB-style labels.
   */
  const k = 1024
  const dm = decimals < 0 ? 0 : decimals
  const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB', 'ZB', 'YB']

  /**
   * Select the applicable binary magnitude before rounding its displayed value.
   */
  const i = Math.floor(Math.log(bytes) / Math.log(k))

  return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i]
}

/**
 * formatDate formats a valid date in the supplied or active application locale. Missing
 * and invalid dates return an empty string; omitted options include year through
 * seconds with a 24-hour clock.
 */
export function formatDate(
  date: string | Date | null | undefined,
  options: Intl.DateTimeFormatOptions = {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  },
  localeOverride?: string,
): string {
  if (!date) return ''

  const d = new Date(date)
  if (isNaN(d.getTime())) return ''

  const locale = localeOverride ?? getLocale()
  return new Intl.DateTimeFormat(locale, options).format(d)
}

/**
 * formatDateOnly renders the local calendar date in the active application locale.
 * It delegates validation to formatDate and returns empty text for missing or
 * invalid date inputs.
 */
export function formatDateOnly(date: string | Date | null | undefined): string {
  return formatDate(date, {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  })
}

/**
 * formatDateTime delegates date and time rendering to formatDate with the caller's
 * options and locale override. Omitting options uses formatDate's full date-and-time
 * defaults, and invalid or missing dates return empty text.
 */
export function formatDateTime(
  date: string | Date | null | undefined,
  options?: Intl.DateTimeFormatOptions,
  localeOverride?: string,
): string {
  return formatDate(date, options, localeOverride)
}

/**
 * formatDateTimeToMinute formats a local date and 24-hour time through minutes using
 * the supplied or application locale. Missing and invalid dates return empty text
 * through formatDate's shared validation.
 */
export function formatDateTimeToMinute(
  date: string | Date | null | undefined,
  localeOverride?: string,
): string {
  return formatDate(
    date,
    {
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      hour12: false,
    },
    localeOverride,
  )
}

/**
 * formatDateLocalInput converts a Date into local year-month-day text. Invalid
 * dates return empty text, and month and day use two digits without converting the
 * calendar date to UTC.
 */
export function formatDateLocalInput(date: Date): string {
  if (isNaN(date.getTime())) return ''
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}

/**
 * formatDateTimeLocalInput converts Unix seconds to a local datetime-local value
 * through minutes. Null, zero, and invalid timestamps return empty text; seconds are
 * omitted from the displayed form value.
 */
export function formatDateTimeLocalInput(timestampSeconds: number | null): string {
  if (!timestampSeconds) return ''
  /**
   * Convert persisted Unix seconds into Date milliseconds before reading local calendar
   * fields.
   */
  const date = new Date(timestampSeconds * 1000)
  if (isNaN(date.getTime())) return ''
  const year = date.getFullYear()
  const month = String(date.getMonth() + 1).padStart(2, '0')
  const day = String(date.getDate()).padStart(2, '0')
  const hours = String(date.getHours()).padStart(2, '0')
  const minutes = String(date.getMinutes()).padStart(2, '0')
  return `${year}-${month}-${day}T${hours}:${minutes}`
}

/**
 * parseDateTimeLocalInput parses timezone-free local calendar components into whole
 * Unix seconds. Empty, malformed, out-of-range, and calendar-overflow values return
 * null; optional seconds and fractions are accepted, with fractional-second text
 * truncated to millisecond precision.
 *
 * Date construction uses local setters so browser timezone and daylight-saving
 * disambiguation apply. The post-construction check compares year, month, and day to
 * reject normalized calendar overflow, while time-of-day normalization follows Date
 * behavior. Returned seconds are floored, so subsecond input is discarded at the
 * API-facing result.
 */
export function parseDateTimeLocalInput(value: string): number | null {
  if (!value) return null

  /**
   * The form supplies timezone-free local components; matching their shape prevents
   * timezone-bearing strings from being reinterpreted as absolute instants.
   */
  const match = /^(\d{4,})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d+))?)?$/.exec(value)
  if (!match) return null

  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hours = Number(match[4])
  const minutes = Number(match[5])
  const seconds = match[6] ? Number(match[6]) : 0
  /**
   * Retain millisecond precision and discard additional fractional-second digits.
   */
  const milliseconds = match[7] ? Number(match[7].slice(0, 3).padEnd(3, '0')) : 0

  if (
    !Number.isSafeInteger(year) ||
    year < 1 ||
    month < 1 ||
    month > 12 ||
    day < 1 ||
    day > 31 ||
    hours > 23 ||
    minutes > 59 ||
    seconds > 59
  ) {
    return null
  }

  const date = new Date(0)
  date.setFullYear(year, month - 1, day)
  date.setHours(hours, minutes, seconds, milliseconds)

  /**
   * Date setters normalize invalid calendar days, so compare the resulting calendar
   * components before accepting a timestamp for form submission.
   */
  if (date.getFullYear() !== year || date.getMonth() !== month - 1 || date.getDate() !== day) {
    return null
  }

  const timestamp = date.getTime()
  /**
   * Convert milliseconds to whole Unix seconds, dropping subsecond precision.
   */
  return Number.isFinite(timestamp) ? Math.floor(timestamp / 1000) : null
}

/**
 * getBrowserTimeZone reads the browser's resolved IANA timezone identifier. An empty
 * resolved timezone or an exception from Intl returns UTC, providing callers a nonempty
 * label for local-time context.
 */
export function getBrowserTimeZone(): string {
  try {
    /**
     * Some Intl environments return an empty timezone identifier; UTC provides a usable
     * label when browser timezone information is unavailable.
     */
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    /**
     * Intl access can fail in the current environment; return UTC so timezone labeling
     * remains available to the date controls.
     */
    return 'UTC'
  }
}

/**
 * formatReasoningEffort turns known reasoning settings into usage labels after trimming
 * and normalizing separators. Empty, none, and minimal values display a dash; unknown
 * nonempty values retain their spelling with the first letter uppercased.
 */
export function formatReasoningEffort(effort: string | null | undefined): string {
  const raw = (effort ?? '').toString().trim()
  if (!raw) return '-'

  const normalized = raw.toLowerCase().replace(/[-_\s]/g, '')
  switch (normalized) {
    case 'low':
      return 'Low'
    case 'medium':
      return 'Medium'
    case 'high':
      return 'High'
    case 'xhigh':
    case 'extrahigh':
      return 'XHigh'
    case 'max':
      return 'Max'
    case 'none':
    case 'minimal':
      return '-'
    default:
      /**
       * Unknown effort values remain visible for diagnostics; uppercase the first
       * letter while keeping the remaining provider spelling intact.
       */
      return raw.length > 1 ? raw[0].toUpperCase() + raw.slice(1) : raw.toUpperCase()
  }
}

/**
 * reasoningEffortValuesEqual compares nonempty reasoning settings after trimming,
 * lowercasing, and removing spaces, hyphens, and underscores. Two empty values return
 * false, so absent settings are not treated as a recorded match.
 */
export function reasoningEffortValuesEqual(
  left: string | null | undefined,
  right: string | null | undefined,
): boolean {
  /**
   * normalize removes case and separator differences from a reasoning setting. Nullish
   * values become empty text, allowing the outer comparison to reject missing values
   * before comparing normalized spellings.
   */
  const normalize = (value: string | null | undefined) =>
    (value ?? '')
      .toString()
      .trim()
      .toLowerCase()
      .replace(/[-_\s]/g, '')
  const a = normalize(left)
  const b = normalize(right)
  return a !== '' && a === b
}

/**
 * formatReasoningEffortMapping renders requested and forwarded settings as one label or
 * an arrow-separated pair. Each side first receives formatReasoningEffort's
 * missing-value and compatibility handling; a missing side displays the other side's
 * label.
 *
 * Comparison uses normalized raw values so separator variants such as x-high and xhigh
 * collapse to one forwarded label. When both formatted labels are absent the result is
 * a dash. Two remaining different values retain their requested-to-forwarded order for
 * usage exports.
 */
export function formatReasoningEffortMapping(
  requested: string | null | undefined,
  forwarded: string | null | undefined,
): string {
  const requestedLabel = formatReasoningEffort(requested)
  const forwardedLabel = formatReasoningEffort(forwarded)
  if (requestedLabel === '-' && forwardedLabel === '-') return '-'
  if (requestedLabel === '-' || reasoningEffortValuesEqual(requested, forwarded)) {
    return forwardedLabel === '-' ? requestedLabel : forwardedLabel
  }
  if (forwardedLabel === '-') return requestedLabel
  return `${requestedLabel} → ${forwardedLabel}`
}

/**
 * formatTime renders local hours, minutes, and seconds in the application locale using
 * a 24-hour clock. It delegates date validation to formatDate, which returns empty text
 * for missing or invalid dates.
 */
export function formatTime(date: string | Date | null | undefined): string {
  return formatDate(date, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hour12: false,
  })
}

/**
 * formatNumberLocaleString formats a number using the active application locale.
 */
export function formatNumberLocaleString(num: number): string {
  return num.toLocaleString(getLocale())
}

/**
 * formatTokensK renders positive thousand and million token counts with one fractional
 * digit and uppercase suffixes. Smaller values are rounded to an integer; billion-sized
 * inputs continue to use the million unit.
 */
export function formatTokensK(tokens: number): string {
  /**
   * Scale decimal token counts before rounding to one fractional digit.
   */
  if (tokens >= 1_000_000) return `${(tokens / 1_000_000).toFixed(1)}M`
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(1)}K`
  return Math.round(tokens).toString()
}

/**
 * formatTokens renders token counts with two fractional digits at K, M, and B
 * thresholds. Nullish values become zero, and values below one thousand use ordinary
 * application-locale number formatting.
 */
export function formatTokens(value: number | null | undefined): string {
  if (value === null || value === undefined) return '0'
  /**
   * Apply decimal suffix thresholds before rounding the displayed token count.
   */
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(2)}B`
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  if (value >= 1_000) return `${(value / 1_000).toFixed(2)}K`
  return value.toLocaleString(getLocale())
}

/**
 * formatCompactNumber renders signed counts using K, M, or B with one fractional digit.
 * Nullish inputs become zero; allowBillions defaults to true, and disabling it keeps
 * billion-sized values in millions.
 */
export function formatCompactNumber(
  num: number | null | undefined,
  options?: {
    /**
     * allowBillions enables the B suffix by default; false keeps billion-sized
     * values expressed in millions.
     */
    allowBillions?: boolean
  },
): string {
  if (num === null || num === undefined) return '0'

  const abs = Math.abs(num)
  const allowBillions = options?.allowBillions !== false

  /**
   * Choose suffixes from absolute magnitude so negative counts retain their sign.
   */
  if (allowBillions && abs >= 1_000_000_000) return `${(num / 1_000_000_000).toFixed(1)}B`
  if (abs >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M`
  if (abs >= 1_000) return `${(num / 1_000).toFixed(1)}K`
  return num.toString()
}

/**
 * formatCountdown translates the remaining interval to a target date using completed
 * minutes. Missing, invalid, and elapsed targets return null; future targets below one
 * minute display zero minutes until expiration.
 */
export function formatCountdown(targetDate: string | Date | null | undefined): string | null {
  if (!targetDate) return null

  const now = new Date()
  const target = new Date(targetDate)
  const diffMs = target.getTime() - now.getTime()

  /**
   * Elapsed and invalid targets cannot produce a future countdown; return null so
   * consumers can omit the remaining-time label.
   */
  if (diffMs <= 0 || isNaN(diffMs)) return null

  /**
   * Discard partial minutes before selecting the largest remaining countdown
   * units.
   */
  const diffMins = Math.floor(diffMs / (1000 * 60))
  const diffHours = Math.floor(diffMins / 60)
  const diffDays = Math.floor(diffHours / 24)

  const remainingHours = diffHours % 24
  const remainingMins = diffMins % 60

  if (diffDays > 0) {
    return i18n.global.t('common.time.countdown.daysHours', { d: diffDays, h: remainingHours })
  }
  if (diffHours > 0) {
    return i18n.global.t('common.time.countdown.hoursMinutes', { h: diffHours, m: remainingMins })
  }

  return i18n.global.t('common.time.countdown.minutes', { m: diffMins })
}

/**
 * formatCountdownWithSuffix adds the translated countdown suffix to formatCountdown's
 * result. Missing, invalid, and elapsed targets return null, allowing callers to omit
 * countdown text when no future interval exists.
 */
export function formatCountdownWithSuffix(
  targetDate: string | Date | null | undefined,
): string | null {
  const countdown = formatCountdown(targetDate)
  if (!countdown) return null
  return i18n.global.t('common.time.countdown.withSuffix', { time: countdown })
}
