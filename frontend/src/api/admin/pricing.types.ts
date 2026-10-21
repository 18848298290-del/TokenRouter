// pricing.types.ts defines shared billing settings edited alongside model price cards.
// Service prices may be null to represent an unconfigured override, while zero remains a
// valid free price and multipliers configure peak periods and batch-image charging
// behavior.

// BillingSettings configures shared service prices and pricing-configuration multipliers
// independently of model cards.
export interface BillingSettings {
  peak_rate_enabled: boolean // Peak pricing activation switch.
  peak_start: string // Peak start in HH:mm.
  peak_end: string // Peak end in HH:mm.
  peak_rate_multiplier: number // Peak rate multiplier.
  long_context_pricing_enabled: boolean // Long-context pricing activation switch.
  free_openai_fast: boolean // OpenAI Fast surcharge waiver.
  batch_image_discount_multiplier: number // Batch image settlement multiplier.
  batch_image_hold_multiplier: number // Batch image reservation multiplier.
  // web_search_price_per_call, search_price_per_1k, audio_realtime_price_per_min,
  // audio_tts_price_per_million_chars, and audio_stt_price_per_hour share price override
  // rules: null leaves that service price unconfigured, and zero configures a free service.
  web_search_price_per_call: number | null // Web search price per call.
  search_price_per_1k: number | null // Search price per 1k.
  audio_realtime_price_per_min: number | null // Realtime audio price per minute.
  audio_tts_price_per_million_chars: number | null // Speech generation price per million characters.
  audio_stt_price_per_hour: number | null // Speech recognition price per hour.
}
