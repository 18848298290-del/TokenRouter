import type { MarketplaceModelPricing } from '@/api/marketplace.types'

/**
 * marketplacePricing.ts classifies public quotes and lists available video units.
 * Explicit zero amounts remain visible as free prices, while absent or invalid amounts
 * cannot make a media quote priced. Named video resolutions remain separate from the
 * optional default price so consumers can identify each row's applicability.
 *
 * Classification combines price status, pricing mode, and valid media amounts. Video
 * entries filter blank labels and invalid amounts before numeric label sorting, then
 * append a valid default. Distinct row keys separate a resolution literally named
 * default from the actual default row, preserving their identities when Vue updates a
 * price list.
 */

/**
 * VideoPriceEntry represents one available per-second public video quote row.
 */
export interface VideoPriceEntry {
  key: string // stable price row identifier
  resolution: string | null // null denotes default price
  price: number // quoted amount per second
}

/**
 * hasImagePricing reports an available finite nonnegative image unit price.
 */
function hasImagePricing(pricing: MarketplaceModelPricing): boolean {
  return [pricing.image_price_1k, pricing.image_price_2k, pricing.image_price_4k].some(isUnitPrice)
}

/**
 * isUnitPrice recognizes finite nonnegative quotes, including explicitly free units.
 */
function isUnitPrice(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

/**
 * videoPricingEntries lists valid named resolutions followed by the optional default
 * quote. It retains zero prices, rejects blank resolution labels, and sorts surviving
 * labels with numeric locale comparison.
 *
 * Each resolution receives a prefixed row key while the default uses its own distinct
 * key, so a resolution named default remains independently addressable. The row retains
 * the quoted amount and original resolution spelling. Missing tables supply no named
 * rows, and a valid default can appear by itself.
 */
export function videoPricingEntries(pricing: MarketplaceModelPricing): VideoPriceEntry[] {
  const entries: VideoPriceEntry[] = Object.entries(pricing.video_prices ?? {})
    .filter(([resolution, price]) => resolution.trim() !== '' && isUnitPrice(price))
    .sort(([left], [right]) => left.localeCompare(right, undefined, { numeric: true }))
    .map(([resolution, price]) => ({ key: `video:resolution:${resolution}`, resolution, price }))
  if (isUnitPrice(pricing.video_price_per_second)) {
    entries.push({
      key: 'video:default-price',
      resolution: null,
      price: pricing.video_price_per_second,
    })
  }
  return entries
}

/**
 * pricingKind selects a display unit from quote status, mode, and available amounts. An
 * unavailable status returns unpriced even when amount fields remain populated, and
 * token mode is accepted directly after the status check.
 *
 * Image and video modes additionally require at least one finite nonnegative unit
 * price. This distinguishes zero-cost quotes from missing or invalid amounts: a free
 * media unit is priced, while a media mode with no usable quote remains unpriced.
 * Unknown modes also remain unpriced.
 */
export function pricingKind(
  pricing: MarketplaceModelPricing,
): 'token' | 'image' | 'video' | 'unpriced' {
  if (pricing.price_status !== 'priced') return 'unpriced'
  if (pricing.pricing_mode === 'video' && videoPricingEntries(pricing).length > 0) return 'video'
  if (pricing.pricing_mode === 'image' && hasImagePricing(pricing)) return 'image'
  if (pricing.pricing_mode === 'token') return 'token'
  return 'unpriced'
}

/**
 * formatPriceNumber formats a quote in the browser's numeric locale. Absolute values at
 * least one use two decimals; smaller amounts retain four to six decimal places
 * according to their magnitude.
 */
export function formatPriceNumber(value: number): string {
  const abs = Math.abs(value)

  /**
   * Small unit amounts need extra precision to remain distinguishable.
   */
  const maximumFractionDigits = abs >= 1 ? 2 : abs >= 0.01 ? 4 : 6
  const minimumFractionDigits = abs >= 1 ? 2 : 4
  return new Intl.NumberFormat(undefined, { minimumFractionDigits, maximumFractionDigits }).format(
    value,
  )
}
