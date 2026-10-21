import { getLocale } from '@/i18n'

/**
 * formatters.ts formats cache counts, billing multipliers, and token-range labels.
 * Cache totals use the application locale, while compact ranges use the browser's
 * numeric locale and lowercase units. Multiplier formatting retains small fractional
 * rates that fixed two-decimal output would obscure.
 */

/**
 * formatCacheTokens renders cache token counts with uppercase K or M and one fractional
 * digit. Counts below one thousand use the application locale, including its numeric
 * grouping and decimal conventions.
 */
export function formatCacheTokens(tokens: number): string {
  /**
   * Use decimal token multiples before rounding cache counts for display.
   */
  if (tokens >= 1000000) return `${(tokens / 1000000).toFixed(1)}M`
  if (tokens >= 1000) return `${(tokens / 1000).toFixed(1)}K`
  return tokens.toLocaleString(getLocale())
}

/**
 * formatMultiplier renders a multiplier with up to four fractional digits and at least
 * two for values at or above 0.0001. Smaller values use two significant digits through
 * toPrecision, including negative inputs.
 */
export function formatMultiplier(val: number): string {
  /**
   * Small multipliers retain significant figures that four fixed decimals could round
   * away.
   */
  if (val < 0.0001) return val.toPrecision(2)
  /**
   * Trim trailing zeros after retaining two decimal places for ordinary multipliers.
   */
  return val.toFixed(4).replace(/(\.\d{2}\d*?)0+$/, '$1')
}

/**
 * formatCompactTokenCount formats token counts using lowercase k and m suffixes in the
 * browser locale. Scaled values below one hundred keep at most one decimal; larger
 * scaled values and unscaled counts round to integers.
 */
export function formatCompactTokenCount(value: number): string {
  /**
   * compact renders a scaled token count with browser-locale number formatting. Counts
   * of at least one hundred round to integers; smaller counts retain at most one
   * fractional digit.
   */
  const compact = (divided: number): string =>
    new Intl.NumberFormat(undefined, {
      // Larger scaled labels omit fractions to keep range endpoints compact.
      maximumFractionDigits: divided >= 100 ? 0 : 1,
    }).format(divided)
  /**
   * Scale counts before choosing fractional precision for compact range labels.
   */
  if (value >= 1_000_000) {
    return `${compact(value / 1_000_000)}m`
  }
  if (value >= 1_000) {
    return `${compact(value / 1_000)}k`
  }
  return new Intl.NumberFormat(undefined, { maximumFractionDigits: 0 }).format(value)
}

/**
 * formatCompactTokenRange renders token bounds with lowercase compact units. Nullish
 * upper bounds produce a plus suffix; numeric bounds, including zero, produce a
 * hyphen-separated lower-to-upper interval using the supplied order.
 */
export function formatCompactTokenRange(minTokens: number, maxTokens?: number | null): string {
  if (typeof maxTokens !== 'number') {
    return `${formatCompactTokenCount(minTokens)}+`
  }
  return `${formatCompactTokenCount(minTokens)}-${formatCompactTokenCount(maxTokens)}`
}
