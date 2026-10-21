/**
 * currency.ts formats payment amounts and resolves currency precision for checkout, orders,
 * and administration. Invalid currency labels use CNY, while valid three-letter labels
 * retain their identity even when the display symbol table has no entry.
 */
// Payment display defaults and known symbols are shared by checkout previews and historical
// order rows.
export const DEFAULT_PAYMENT_CURRENCY = 'CNY'

const PAYMENT_CURRENCY_SYMBOLS: Record<string, string> = {
  USD: '$',
  CNY: '¥',
  RMB: '¥',
  EUR: '€',
  GBP: '£',
  JPY: '¥',
  HKD: 'HK$',
  TWD: 'NT$',
  KRW: '₩',
  AUD: 'A$',
  CAD: 'C$',
  SGD: 'S$',
  NZD: 'NZ$',
  MOP: 'MOP$',
  MYR: 'RM',
  THB: '฿',
  PHP: '₱',
  INR: '₹',
}

/**
 * normalizePaymentCurrency trims and uppercases a supplied currency label. Empty or
 * malformed labels become CNY; any three-letter ASCII label remains available for Intl
 * formatting or textual display by callers.
 */
export function normalizePaymentCurrency(currency?: string | null): string {
  const normalized = String(currency || '')
    .trim()
    .toUpperCase()
  return /^[A-Z]{3}$/.test(normalized) ? normalized : DEFAULT_PAYMENT_CURRENCY
}

/**
 * currencySymbol returns the configured symbol for a normalized payment currency. A valid
 * code absent from the symbol table is displayed as its own label, while malformed input
 * uses the shared CNY default.
 */
export function currencySymbol(currency?: string | null): string {
  const normalized = normalizePaymentCurrency(currency)
  return PAYMENT_CURRENCY_SYMBOLS[normalized] || normalized
}

/**
 * paymentCurrencyFractionDigits returns the currency precision resolved by Intl. Unsupported
 * formatter inputs use two decimal places, allowing amount display and fee rounding to share
 * the same precision rule.
 */
export function paymentCurrencyFractionDigits(currency: string): number {
  try {
    // Resolve ISO precision through Intl so fee previews and displayed totals agree for zero-
    // decimal and three-decimal currencies.
    return (
      new Intl.NumberFormat(undefined, {
        style: 'currency',
        currency,
      }).resolvedOptions().maximumFractionDigits ?? 2
    )
  } catch {
    // An unsupported Intl currency input still needs a usable precision for callers
    // constructing checkout amount previews.
    return 2
  }
}

/**
 * formatPaymentAmount formats a finite amount using the normalized payment currency.
 * Nonfinite amounts display as zero, and invalid locale or currency formatter inputs produce
 * a currency-code prefix with fixed decimals.
 */
export function formatPaymentAmount(
  amount: number,
  currency?: string | null,
  locale?: string,
): string {
  const normalized = normalizePaymentCurrency(currency)
  const fractionDigits = paymentCurrencyFractionDigits(normalized)
  try {
    return new Intl.NumberFormat(locale || undefined, {
      style: 'currency',
      currency: normalized,
      currencyDisplay: 'narrowSymbol',
      minimumFractionDigits: fractionDigits,
      maximumFractionDigits: fractionDigits,
    }).format(Number.isFinite(amount) ? amount : 0)
  } catch {
    // Keep the currency code visible when Intl rejects a locale or currency, so the amount
    // remains identifiable.
    return `${normalized} ${(Number.isFinite(amount) ? amount : 0).toFixed(fractionDigits)}`
  }
}
