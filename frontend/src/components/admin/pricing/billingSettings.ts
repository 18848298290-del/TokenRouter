import type { BillingSettings } from '@/api/admin/pricing.types'

// billingSettings.ts builds and validates shared billing settings for administrator price
// configurations. Optional service prices distinguish an unconfigured null from numeric zero,
// and numeric inputs may temporarily contain empty strings. Submission converts those input
// values in a separate record before validation.
//
// Validation checks finite nonnegative numbers before comparing the image-batch reservation
// multiplier with the settlement multiplier. Enabled peak pricing then requires valid
// increasing same-day clock endpoints. The returned string is a localization key for the first
// failed rule; null means that the numeric, reservation, and enabled peak-period checks passed.

// defaultBillingSettings returns a fresh shared billing draft. Peak pricing starts disabled,
// its multiplier starts at one, batch settlement and reservation use 0.5 and 0.6, and optional
// service prices begin unconfigured.
export const defaultBillingSettings = (): BillingSettings => ({
  peak_rate_enabled: false,
  peak_start: '',
  peak_end: '',
  peak_rate_multiplier: 1,
  long_context_pricing_enabled: true,
  free_openai_fast: false,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  web_search_price_per_call: null,
  search_price_per_1k: null,
  audio_realtime_price_per_min: null,
  audio_tts_price_per_million_chars: null,
  audio_stt_price_per_hour: null,
})

// billingSettingsToAPI copies shared billing settings and normalizes optional service prices.
// Empty strings and null become null, numeric strings become numbers, and zero retains the
// meaning of a free service. Other billing values pass through unchanged.
//
// The caller's input object remains available with its current edit values. Validation runs
// after conversion and rejects nonfinite or negative amounts, allowing this function to
// concentrate on representing clearable browser inputs in the API payload.
export function billingSettingsToAPI(value: BillingSettings): BillingSettings {
  const result = { ...value }
  // Numeric input clearing produces an empty string; checking that state before Number
  // prevents an unconfigured price from becoming a free price.
  result.web_search_price_per_call =
    value.web_search_price_per_call === null || (value.web_search_price_per_call as unknown) === ''
      ? null
      : Number(value.web_search_price_per_call)
  result.search_price_per_1k =
    value.search_price_per_1k === null || (value.search_price_per_1k as unknown) === ''
      ? null
      : Number(value.search_price_per_1k)
  result.audio_realtime_price_per_min =
    value.audio_realtime_price_per_min === null ||
    (value.audio_realtime_price_per_min as unknown) === ''
      ? null
      : Number(value.audio_realtime_price_per_min)
  result.audio_tts_price_per_million_chars =
    value.audio_tts_price_per_million_chars === null ||
    (value.audio_tts_price_per_million_chars as unknown) === ''
      ? null
      : Number(value.audio_tts_price_per_million_chars)
  result.audio_stt_price_per_hour =
    value.audio_stt_price_per_hour === null || (value.audio_stt_price_per_hour as unknown) === ''
      ? null
      : Number(value.audio_stt_price_per_hour)
  return result
}

// validateBillingSettings returns the first validation key for the submitted billing form. It
// normalizes optional prices before checking finite nonnegative rates and amounts. Batch
// reservation must cover the configured settlement multiplier, and an enabled peak period needs
// valid increasing HH:mm endpoints.
//
// Numeric failures take precedence over reservation and peak failures, so the form receives one
// predictable error to display. A null result means these checks passed; the original draft is
// not mutated by validation.
export function validateBillingSettings(value: BillingSettings): string | null {
  const s = billingSettingsToAPI(value)
  const rates = [
    s.peak_rate_multiplier,
    s.batch_image_discount_multiplier,
    s.batch_image_hold_multiplier,
  ]
  if (rates.some((v) => typeof v !== 'number' || !Number.isFinite(v) || v < 0))
    return 'invalidNumber'
  if (
    s.web_search_price_per_call !== null &&
    (!Number.isFinite(s.web_search_price_per_call) || s.web_search_price_per_call < 0)
  )
    return 'invalidNumber'
  if (
    s.search_price_per_1k !== null &&
    (!Number.isFinite(s.search_price_per_1k) || s.search_price_per_1k < 0)
  )
    return 'invalidNumber'
  if (
    s.audio_realtime_price_per_min !== null &&
    (!Number.isFinite(s.audio_realtime_price_per_min) || s.audio_realtime_price_per_min < 0)
  )
    return 'invalidNumber'
  if (
    s.audio_tts_price_per_million_chars !== null &&
    (!Number.isFinite(s.audio_tts_price_per_million_chars) ||
      s.audio_tts_price_per_million_chars < 0)
  )
    return 'invalidNumber'
  if (
    s.audio_stt_price_per_hour !== null &&
    (!Number.isFinite(s.audio_stt_price_per_hour) || s.audio_stt_price_per_hour < 0)
  )
    return 'invalidNumber'
  if (s.batch_image_hold_multiplier < s.batch_image_discount_multiplier) return 'invalidHold'
  if (
    s.peak_rate_enabled &&
    (!/^([01]\d|2[0-3]):[0-5]\d$/.test(s.peak_start) ||
      !/^([01]\d|2[0-3]):[0-5]\d$/.test(s.peak_end) ||
      s.peak_start >= s.peak_end)
  )
    return 'invalidPeak'
  return null
}
