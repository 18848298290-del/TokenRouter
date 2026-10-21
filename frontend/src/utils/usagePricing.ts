/**
 * usagePricing.ts derives displayed per-token and per-million-token prices from
 * recorded cost and token counts. Missing or nonfinite inputs and nonpositive counts
 * yield no price. Formatting keeps those absent values separate from a valid zero-cost
 * result and supports caller-selected currency text.
 */

/**
 * TOKENS_PER_MILLION converts the recorded per-token amount into the per-million
 * display unit used by usage pricing details and exports.
 */
export const TOKENS_PER_MILLION = 1_000_000

/**
 * TokenPriceFormatOptions customizes precision, currency prefix, and missing-price
 * display text.
 */
interface TokenPriceFormatOptions {
  /**
   * fractionDigits is passed to toFixed, defaulting to four; callers supply a
   * supported precision because formatter errors propagate.
   */
  fractionDigits?: number // displayed fractional digit count
  /**
   * withCurrencySymbol includes the prefix by default; false returns the formatted
   * number with no currency text attached.
   */
  withCurrencySymbol?: boolean // currency prefix enabled
  /**
   * currencySymbol defaults to the dollar sign and is used when currency-prefix
   * display is enabled by the other option.
   */
  currencySymbol?: string // display currency prefix
  /**
   * emptyValue replaces the default dash when cost or token count cannot produce a
   * unit price for display.
   */
  emptyValue?: string // missing-price display text
}

/**
 * isFiniteNumber narrows values to finite JavaScript numbers for price arithmetic.
 */
function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value)
}

/**
 * calculateTokenUnitPrice divides a finite recorded cost by a positive finite token
 * count. Missing, nonfinite, and nonpositive-count inputs return null; zero and
 * negative finite costs participate in the calculation unchanged.
 */
export function calculateTokenUnitPrice(
  cost: number | null | undefined,
  tokens: number | null | undefined,
): number | null {
  if (!isFiniteNumber(cost) || !isFiniteNumber(tokens) || tokens <= 0) {
    return null
  }

  return cost / tokens
}

/**
 * calculateTokenPricePerMillion scales calculateTokenUnitPrice's result into an amount
 * per million tokens. Invalid cost/count pairs remain null, while a valid zero-cost
 * result remains numeric zero for display.
 */
export function calculateTokenPricePerMillion(
  cost: number | null | undefined,
  tokens: number | null | undefined,
): number | null {
  const unitPrice = calculateTokenUnitPrice(cost, tokens)
  if (unitPrice == null) {
    return null
  }

  /**
   * Scale the per-token amount to the displayed million-token denominator.
   */
  return unitPrice * TOKENS_PER_MILLION
}

/**
 * formatTokenPricePerMillion formats the derived price with four fractional digits and
 * a dollar prefix by default. Invalid inputs use emptyValue or a dash; callers can
 * override precision, currency text, or suppress the prefix.
 */
export function formatTokenPricePerMillion(
  cost: number | null | undefined,
  tokens: number | null | undefined,
  options: TokenPriceFormatOptions = {},
): string {
  const pricePerMillion = calculateTokenPricePerMillion(cost, tokens)
  if (pricePerMillion == null) {
    /**
     * An unavailable unit price needs a distinct placeholder so a valid zero-cost token
     * record remains visibly distinguishable from missing data.
     */
    return options.emptyValue ?? '-'
  }

  const fractionDigits = options.fractionDigits ?? 4
  /**
   * Round the derived display amount to the caller-selected fixed precision.
   */
  const formatted = pricePerMillion.toFixed(fractionDigits)
  if (options.withCurrencySymbol == false) {
    return formatted
  }
  return `${options.currencySymbol ?? '$'}${formatted}`
}
