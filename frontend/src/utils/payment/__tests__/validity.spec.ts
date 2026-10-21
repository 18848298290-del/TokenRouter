import { describe, expect, it } from 'vitest'
import { planValiditySuffix } from '../validity'

/**
 * validity.spec.ts checks subscription duration labels against singular, plural, and unknown
 * stored units. A small translation fixture makes the selected unit and duration count
 * observable without mounting the purchase page or contacting billing services.
 */

/**
 * t resolves the duration translation keys used by the suffix fixtures.
 */
const t = (key: string): string =>
  ({
    'payment.perMonth': '月',
    'payment.days': '天',
    'payment.weeks': '周',
    'payment.months': '个月',
  })[key] ?? key

/**
 * suffix formats a duration fixture through the production subscription suffix helper.
 */
const suffix = (validity_days: number, validity_unit: string) =>
  planValiditySuffix({ validity_days, validity_unit }, t)

describe('planValiditySuffix', () => {
  // Stored plural month values share the singular month interpretation, so the suffix keeps
  // the subscription duration expressed in months.
  // Administrator forms can store the plural month identifier with a duration count. The
  // displayed suffix must use month wording and retain the configured count.
  it('renders admin-form plural months correctly', () => {
    expect(suffix(1, 'months')).toBe('月')
    expect(suffix(3, 'months')).toBe('3个月')
  })

  // Historical plans can store the singular month identifier. Its suffix must agree with the
  // plural representation so the same subscription duration keeps one meaning.
  it('renders singular month the same way', () => {
    expect(suffix(1, 'month')).toBe('月')
    expect(suffix(6, 'month')).toBe('6个月')
  })

  // Both week spellings represent seven-day units in billing, so the display retains the
  // configured number of weeks for users.
  // Plan duration may use either singular or plural week identifiers. Both representations must
  // display weeks and retain their counts instead of being interpreted as day values.
  it('renders weeks as weeks instead of mislabeled days', () => {
    expect(suffix(2, 'weeks')).toBe('2周')
    expect(suffix(1, 'week')).toBe('1周')
  })

  // Day-based plans include modern and older unit spellings. Their suffixes must retain the
  // configured duration count and use the day wording expected by billing.
  it('renders day-based and legacy units as days', () => {
    expect(suffix(30, 'days')).toBe('30天')
    expect(suffix(30, 'day')).toBe('30天')
    expect(suffix(30, '')).toBe('30天')
  })

  // Unknown duration units use day-based billing, so the formatter must show days for the same
  // stored values.
  // Stored units outside the supported month and week set reach the duration formatter. They
  // must display as days, matching the backend interpretation of those values.
  it('falls back to days for units billing does not honor', () => {
    expect(suffix(1, 'year')).toBe('1天')
    expect(suffix(365, 'unknown')).toBe('365天')
  })

  // Stored duration units include surrounding whitespace and mixed casing. Normalization must
  // select the same localized duration as the corresponding clean lowercase unit string.
  it('normalizes casing and whitespace', () => {
    expect(suffix(1, ' Months ')).toBe('月')
    expect(suffix(2, 'WEEKS')).toBe('2周')
  })
})
