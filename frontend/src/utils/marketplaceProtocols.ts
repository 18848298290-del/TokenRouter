import type { ProtocolID } from '@/api/protocols.types'
import type { ProviderBrandKey } from '@/utils/providerBrand'

/**
 * marketplaceProtocols.ts supplies the client protocol labels, brand identities, and
 * endpoint hints displayed in the public marketplace. Its static ordered catalog
 * supports public pages before an administrator catalog is available, and selection
 * returns recognized entries in that display order.
 */

/**
 * MarketplaceProtocol pairs a public client protocol with its branding and endpoint.
 */
export interface MarketplaceProtocol {
  id: ProtocolID // client protocol identifier
  brand: Extract<ProviderBrandKey, 'anthropic' | 'openai' | 'google' | 'xai' | 'typesafe'> // request format brand
  endpoint: string // displayed HTTP endpoint
}

/**
 * MARKETPLACE_PROTOCOLS defines the public protocol display order and endpoint hints
 * checked by the marketplace protocol catalog fixture test.
 */
export const MARKETPLACE_PROTOCOLS: readonly MarketplaceProtocol[] = [
  { id: 'anthropic_messages', brand: 'anthropic', endpoint: 'POST /v1/messages' },
  { id: 'openai_responses', brand: 'openai', endpoint: 'POST /v1/responses' },
  { id: 'openai_chat_completions', brand: 'openai', endpoint: 'POST /v1/chat/completions' },
  {
    id: 'gemini_generate_content',
    brand: 'google',
    endpoint: 'POST /v1beta/models/{model}:generateContent / :streamGenerateContent',
  },
  { id: 'systemone', brand: 'typesafe', endpoint: 'POST /v1/systemone' },
  { id: 'openai_embeddings', brand: 'openai', endpoint: 'POST /v1/embeddings' },
  { id: 'openai_images_generations', brand: 'openai', endpoint: 'POST /v1/images/generations' },
  { id: 'openai_images_edits', brand: 'openai', endpoint: 'POST /v1/images/edits' },
  { id: 'image_batches', brand: 'google', endpoint: 'POST /v1/images/batches' },
  { id: 'grok_videos_generations', brand: 'xai', endpoint: 'POST /v1/videos/generations' },
  { id: 'grok_videos_edits', brand: 'xai', endpoint: 'POST /v1/videos/edits' },
  { id: 'grok_videos_extensions', brand: 'xai', endpoint: 'POST /v1/videos/extensions' },
  { id: 'grok_tts', brand: 'xai', endpoint: 'POST /v1/tts' },
  { id: 'grok_stt', brand: 'xai', endpoint: 'POST /v1/stt' },
  { id: 'grok_custom_voices', brand: 'xai', endpoint: '/v1/custom-voices' },
  { id: 'grok_voice_realtime', brand: 'xai', endpoint: 'GET /v1/realtime (WebSocket)' },
  { id: 'openai_responses_websocket', brand: 'openai', endpoint: 'GET /v1/responses (WebSocket)' },
  { id: 'openai_live', brand: 'openai', endpoint: 'POST /v1/live' },
  { id: 'openai_responses_compact', brand: 'openai', endpoint: 'POST /v1/responses/compact' },
  { id: 'openai_alpha_search', brand: 'openai', endpoint: 'POST /v1/alpha/search' },
  { id: 'grok_web_search', brand: 'xai', endpoint: 'POST /v1/web_search' },
  { id: 'grok_x_search', brand: 'xai', endpoint: 'POST /v1/x_search' },
]

/**
 * marketplaceProtocols returns recognized protocol entries in the public catalog's
 * order. Missing IDs produce an empty array; repeated IDs collapse through membership
 * lookup, and returned entries reference the shared catalog objects.
 */
export function marketplaceProtocols(
  ids: readonly ProtocolID[] | undefined,
): MarketplaceProtocol[] {
  const selected = new Set(ids ?? [])
  return MARKETPLACE_PROTOCOLS.filter((protocol) => selected.has(protocol.id))
}
