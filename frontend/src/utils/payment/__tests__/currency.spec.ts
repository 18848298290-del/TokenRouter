import { describe, expect, it } from 'vitest'
import { currencySymbol, formatPaymentAmount, paymentCurrencyFractionDigits } from '../currency'

/**
 * currency.spec.ts checks currency normalization, symbols, and decimal precision shared by
 * checkout and order display. The fixtures exercise Intl behavior and deterministic symbol
 * lookup with supplied numeric amounts, without payment API calls.
 */

describe('formatPaymentAmount', () => {
  // Currency display receives values with zero, two, and three fractional digits. The formatted
  // output must follow each currency's Intl precision rather than a fixed decimal count.
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
  })

  // Fee computation requests precision for currencies with different minor units. The returned
  // digit counts must match the precision used by payment amount display.
  it('exposes currency fraction digits for fee calculation', () => {
    expect(paymentCurrencyFractionDigits('JPY')).toBe(0)
    expect(paymentCurrencyFractionDigits('HKD')).toBe(2)
    expect(paymentCurrencyFractionDigits('KWD')).toBe(3)
  })

  // Currency labels can be absent or malformed at display time. Formatting must normalize those
  // values to the configured default while still producing a readable amount.
  it('normalizes invalid currencies to the default display currency', () => {
    expect(formatPaymentAmount(108, '', 'en-US')).toBe('¥108.00')
    expect(formatPaymentAmount(108, 'USD', 'en-US')).toBe('$108.00')
  })

  // Known symbols and an unmapped currency code enter the same lookup. Known currencies must use
  // deterministic configured symbols, while the unknown symbol remains its currency code.
  it('returns deterministic configured currency symbols', () => {
    expect(currencySymbol('CNY')).toBe('¥')
    expect(currencySymbol('USD')).toBe('$')
    expect(currencySymbol('NZD')).toBe('NZ$')
    expect(currencySymbol('KWD')).toBe('KWD')
  })
})
