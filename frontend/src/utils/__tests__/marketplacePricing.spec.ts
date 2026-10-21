import { describe, expect, it } from 'vitest'
import { pricingKind, videoPricingEntries } from '../marketplacePricing'

/**
 * marketplacePricing.spec.ts tests availability of public video quotes and their
 * row identities. Fixtures distinguish free amounts from unavailable or invalid
 * prices and keep named resolution entries separate from the optional default-
 * price row.
 */

describe('video marketplace prices', () => {
  /**
   * A video quote combines named resolutions, a free resolution, and a free default.
   * All rows remain visible with distinct keys and the default appears last.
   */
  it('keeps free dimensions and a default in separate rows', () => {
    const quote = {
      pricing_mode: 'video' as const,
      price_status: 'priced' as const,
      video_prices: { '768p': 0.12, '2k': 0, 'default-price': 0.3 },
      video_price_per_second: 0,
    }
    expect(pricingKind(quote)).toBe('video')
    const entries = videoPricingEntries(quote)
    expect(entries).toHaveLength(4)
    expect(new Set(entries.map((item) => item.key)).size).toBe(4)
    expect(entries.at(-1)).toMatchObject({ resolution: null, price: 0 })
  })
  /**
   * Video quotes include a free default, missing amounts, invalid named amounts, and an
   * unavailable status. Classification accepts the free quote and rejects the unusable
   * combinations.
   */
  it('recognizes default-only video prices and rejects absent or invalid prices', () => {
    const base = { pricing_mode: 'video' as const, price_status: 'priced' as const }
    expect(pricingKind({ ...base, video_price_per_second: 0 })).toBe('video')
    expect(pricingKind(base)).toBe('unpriced')
    expect(pricingKind({ ...base, video_prices: { '2k': -1, '768p': Infinity } })).toBe('unpriced')
    expect(pricingKind({ ...base, price_status: 'unpriced', video_price_per_second: 1 })).toBe(
      'unpriced',
    )
  })
})
