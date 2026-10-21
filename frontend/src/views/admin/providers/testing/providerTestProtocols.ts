import type { Provider } from '@/api/admin/providers.types'
import type { ProtocolID } from '@/api/protocols.types'

/**
 * providerTestProtocols.ts builds endpoint choices and initial protocol selection for provider
 * connection tests. OpenAI API keys expose both text endpoints, while fixed-endpoint platforms
 * show a single informational option. CN platforms derive choices from saved native protocol
 * identifiers or legacy credential settings.
 *
 * An explicit empty upstream_protocols array yields no CN choices; a missing array invokes the
 * legacy compatibility mapping. This distinction keeps an intentionally empty configuration
 * separate from an older record. Default selection may prefer Chat Completions for an OpenAI
 * key whose saved capabilities omit Responses, but the available test choices still allow
 * administrators to probe either endpoint.
 */

/**
 * ProviderTestProtocol names explicit endpoint selectors accepted by the provider-test request.
 */
export type ProviderTestProtocol = 'responses' | 'chat_completions' | 'anthropic' | 'systemone'

/**
 * ProviderTestProtocolOption pairs a request selector with its human-readable endpoint display.
 */
export interface ProviderTestProtocolOption {
  /**
   * The native sentinel labels a fixed endpoint and is omitted from the submitted protocol
   * field.
   */
  value: ProviderTestProtocol | 'native'
  label: string // displayed endpoint protocol name
  path: string // displayed upstream endpoint path
}

/**
 * ProviderTestProtocolPlan describes endpoint choices and whether the dialog permits selection.
 */
export interface ProviderTestProtocolPlan {
  options: ProviderTestProtocolOption[] // ordered endpoint display choices
  /**
   * False disables protocol selection, and request construction omits the protocol except for
   * its separate compact-mode rules.
   */
  selectable: boolean
}

/**
 * CN_PLATFORMS identifies providers whose endpoint choices follow saved native protocols or the
 * legacy api_protocol compatibility mapping.
 */
const CN_PLATFORMS = new Set(['kimi', 'zhipu', 'deepseek'])

/**
 * CN_PROTOCOLS maps saved native protocol identifiers to test request values; array order
 * determines the displayed CN endpoint order.
 */
const CN_PROTOCOLS: Array<{
  id: ProtocolID // saved native protocol identifier
  value: ProviderTestProtocol // test request protocol selector
}> = [
  { id: 'openai_chat_completions', value: 'chat_completions' },
  { id: 'anthropic_messages', value: 'anthropic' },
  { id: 'openai_responses', value: 'responses' },
]

/**
 * option constructs one endpoint choice with its value, label, and displayed request path.
 */
function option(
  value: ProviderTestProtocolOption['value'],
  label: string,
  path: string,
): ProviderTestProtocolOption {
  return { value, label, path }
}

/**
 * chatOption returns the Chat Completions endpoint choice for text connection tests.
 */
function chatOption() {
  return option('chat_completions', 'Chat Completions', '/v1/chat/completions')
}

/**
 * messagesOption returns the Anthropic Messages endpoint choice for text connection tests.
 */
function messagesOption() {
  return option('anthropic', 'Messages', '/v1/messages')
}

/**
 * responsesOption returns a Responses choice with the platform-specific displayed path.
 * DeepSeek uses the root responses endpoint; the other callers display the versioned
 * OpenAI-compatible endpoint path.
 */
function responsesOption(platform: string) {
  // DeepSeek displays its root Responses path so the selector describes the endpoint used for
  // that platform.
  return option('responses', 'Responses', platform === 'deepseek' ? '/responses' : '/v1/responses')
}

/**
 * cnOption selects a display choice for one CN test protocol. Chat and Messages map directly,
 * while all remaining values use the platform-specific Responses endpoint choice.
 */
function cnOption(value: ProviderTestProtocol, platform: string) {
  if (value === 'chat_completions') return chatOption()
  if (value === 'anthropic') return messagesOption()
  return responsesOption(platform)
}

/**
 * enabledProtocols reads the saved native protocol array from provider credentials. Missing or
 * non-array values return null, while an explicit empty array remains distinguishable for
 * callers handling older records.
 */
function enabledProtocols(provider: Provider): ProtocolID[] | null {
  const raw = provider.credentials?.upstream_protocols
  return Array.isArray(raw) ? (raw as ProtocolID[]) : null
}

/**
 * legacyCNProtocols derives CN test choices from the legacy api_protocol credential. Adaptive
 * mode includes native Responses except on Zhipu, and absent or unsupported legacy values
 * select Chat Completions.
 */
function legacyCNProtocols(provider: Provider): ProviderTestProtocol[] {
  const legacy = provider.credentials?.api_protocol
  const nativeResponses = provider.platform !== 'zhipu'
  if (legacy === 'adaptive') {
    return nativeResponses
      ? ['chat_completions', 'anthropic', 'responses']
      : ['chat_completions', 'anthropic']
  }
  if (legacy === 'anthropic') return ['anthropic']
  if (legacy === 'responses' && nativeResponses) return ['responses']
  return ['chat_completions']
}

/**
 * providerTestProtocolPlan lists test endpoint choices and whether administrators may switch
 * between them. A missing provider produces an empty plan. OpenAI API keys can probe both text
 * endpoints, while OAuth records receive the fixed Responses choice.
 *
 * CN records use their saved protocol array when present, including an empty array. Older
 * records with no array use legacy protocol compatibility rules. Other recognized platforms
 * receive one native display option whose value tells request construction to omit an explicit
 * protocol. Unrecognized platforms have no endpoint choices.
 */
export function providerTestProtocolPlan(provider: Provider | null): ProviderTestProtocolPlan {
  if (!provider) return { options: [], selectable: false }
  const { platform, type } = provider

  if (platform === 'openai') {
    // API-key test choices include both endpoints regardless of saved capability selection,
    // allowing administrators to probe either text protocol.
    if (type === 'apikey') {
      return { options: [responsesOption(platform), chatOption()], selectable: true }
    }
    return { options: [responsesOption(platform)], selectable: false }
  }

  if (CN_PLATFORMS.has(platform)) {
    const enabled = enabledProtocols(provider)
    const values = enabled
      ? CN_PROTOCOLS.filter((item) => enabled.includes(item.id)).map((item) => item.value)
      : legacyCNProtocols(provider)
    return {
      options: values.map((value) => cnOption(value, platform)),
      selectable: values.length > 1,
    }
  }

  // Fixed-endpoint platforms expose a native display choice; request construction uses that
  // sentinel to omit an explicit protocol override.
  switch (platform) {
    case 'jev':
      return { options: [option('native', 'SystemOne', '/v1/systemone')], selectable: false }
    case 'anthropic':
      return { options: [option('native', 'Messages', '/v1/messages')], selectable: false }
    case 'grok':
      return { options: [option('native', 'Responses', '/v1/responses')], selectable: false }
    case 'qoder':
      return {
        options: [option('native', 'Chat Completions', '/v1/chat/completions')],
        selectable: false,
      }
    case 'antigravity':
      return {
        options: [option('native', 'GenerateContent', ':streamGenerateContent')],
        selectable: false,
      }
    case 'gemini':
      return {
        options: [option('native', 'GenerateContent', ':streamGenerateContent')],
        selectable: false,
      }
    default:
      return { options: [], selectable: false }
  }
}

/**
 * defaultProviderTestProtocol chooses the initial protocol from the supplied plan. OpenAI API
 * keys with saved Chat support and no Responses support start on Chat; other providers use the
 * first choice or native.
 */
export function defaultProviderTestProtocol(
  provider: Provider | null,
  plan: ProviderTestProtocolPlan,
) {
  const first = plan.options[0]?.value ?? 'native'
  if (provider?.platform === 'openai' && provider.type === 'apikey') {
    const enabled = enabledProtocols(provider)
    if (
      enabled &&
      !enabled.includes('openai_responses') &&
      enabled.includes('openai_chat_completions')
    ) {
      return 'chat_completions'
    }
  }
  return first
}
