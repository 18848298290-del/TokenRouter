// providerEndpoints.ts supplies endpoint presets and default addresses for
// Grok and CN provider forms. Billing mode selects the paid or coding
// endpoint family, and protocol selects the forwarding address within that
// family. Grok customization checks compare parsed hosts with the default
// CLI gateway.

// GROK_DEFAULT_GATEWAY_HOST identifies the CLI gateway written by ordinary
// Grok OAuth creation and refresh operations.
const GROK_DEFAULT_GATEWAY_HOST = 'cli-chat-proxy.grok.com'

// isCustomGrokBaseUrl reports whether a stored address selects a custom Grok
// forwarding host. Empty, non-string, malformed, and default CLI gateway
// addresses return false; other valid URL hosts return true.
export function isCustomGrokBaseUrl(value: unknown): boolean {
  if (typeof value !== 'string') return false
  const trimmed = value.trim()
  if (!trimmed) return false
  let parsed: URL
  try {
    parsed = new URL(trimmed)
  } catch {
    // Malformed stored addresses cannot initialize a custom endpoint
    // selection, so the form treats them as unset.
    return false
  }
  return parsed.hostname.toLowerCase() !== GROK_DEFAULT_GATEWAY_HOST
}

// GrokBaseUrlPreset describes a selectable endpoint and its translated or
// literal label.
export interface GrokBaseUrlPreset {
  // label and labelKey provide alternate names for a preset. The preset control
  // uses a supplied literal label before translating the optional labelKey.
  labelKey?: 'cli' | 'official' // Translation label suffix.
  label?: string // Literal regional label.
  url: string // Forwarding endpoint URL.
}

// GROK_BASE_URL_PRESETS supplies clickable forwarding endpoints while the
// form continues accepting administrator-entered addresses as well.
export const GROK_BASE_URL_PRESETS: GrokBaseUrlPreset[] = [
  { labelKey: 'cli', url: 'https://cli-chat-proxy.grok.com/v1' },
  { labelKey: 'official', url: 'https://api.x.ai/v1' },
  { label: 'us-east-1', url: 'https://us-east-1.api.x.ai/v1' },
  { label: 'us-west-2', url: 'https://us-west-2.api.x.ai/v1' },
  { label: 'eu-west-1', url: 'https://eu-west-1.api.x.ai/v1' },
]

// CnProviderMode identifies the billing mode used to choose provider
// endpoint defaults.
export type CnProviderMode = 'payg' | 'coding'

// CnApiProtocol identifies a native protocol or adaptive endpoint selection
// for CN providers.
export type CnApiProtocol = 'adaptive' | 'chat_completions' | 'anthropic' | 'responses'

// CnNativeApiProtocol identifies an individual native protocol with a
// configurable forwarding endpoint.
export type CnNativeApiProtocol = Exclude<CnApiProtocol, 'adaptive'>

// cnSupportsNativeResponses reports whether the CN platform offers native
// Responses endpoints.
export function cnSupportsNativeResponses(platform: string): boolean {
  return platform === 'deepseek' || platform === 'kimi'
}

// CnBaseUrlPreset associates a billing mode and protocol with a selectable
// endpoint.
export interface CnBaseUrlPreset {
  mode: CnProviderMode // Provider billing mode.
  protocol: CnApiProtocol // Selected API protocol.
  label: string // Literal preset label.
  url: string // Forwarding endpoint URL.
}

// CN_BASE_URL_PRESETS groups endpoint shortcuts by platform, billing mode,
// and native protocol for provider forms.
export const CN_BASE_URL_PRESETS: Record<'kimi' | 'zhipu' | 'deepseek', CnBaseUrlPreset[]> = {
  kimi: [
    {
      mode: 'payg',
      protocol: 'chat_completions',
      label: 'Moonshot',
      url: 'https://api.moonshot.cn/v1',
    },
    {
      mode: 'payg',
      protocol: 'anthropic',
      label: 'Moonshot Anthropic',
      url: 'https://api.moonshot.cn/anthropic',
    },
    {
      mode: 'payg',
      protocol: 'responses',
      label: 'Moonshot Responses',
      url: 'https://api.moonshot.cn/v1',
    },
    {
      mode: 'coding',
      protocol: 'chat_completions',
      label: 'Kimi For Coding',
      url: 'https://api.kimi.com/coding/v1',
    },
    {
      mode: 'coding',
      protocol: 'anthropic',
      label: 'Kimi Coding Anthropic',
      url: 'https://api.kimi.com/coding',
    },
    {
      mode: 'coding',
      protocol: 'responses',
      label: 'Kimi Coding Responses',
      url: 'https://api.kimi.com/coding/v1',
    },
  ],
  zhipu: [
    {
      mode: 'payg',
      protocol: 'chat_completions',
      label: 'GLM PaaS',
      url: 'https://open.bigmodel.cn/api/paas/v4',
    },
    {
      mode: 'payg',
      protocol: 'anthropic',
      label: 'GLM Anthropic',
      url: 'https://open.bigmodel.cn/api/anthropic',
    },
    {
      mode: 'coding',
      protocol: 'chat_completions',
      label: 'GLM Coding',
      url: 'https://open.bigmodel.cn/api/coding/paas/v4',
    },
    {
      mode: 'coding',
      protocol: 'anthropic',
      label: 'GLM Coding Anthropic',
      url: 'https://open.bigmodel.cn/api/anthropic',
    },
  ],
  deepseek: [
    {
      mode: 'payg',
      protocol: 'chat_completions',
      label: 'DeepSeek',
      url: 'https://api.deepseek.com',
    },
    {
      mode: 'payg',
      protocol: 'anthropic',
      label: 'DeepSeek Anthropic',
      url: 'https://api.deepseek.com/anthropic',
    },
    {
      mode: 'payg',
      protocol: 'responses',
      label: 'DeepSeek Responses',
      url: 'https://api.deepseek.com',
    },
  ],
}

// defaultCNBaseUrl returns the endpoint for a platform, billing mode, and
// protocol. Chat Completions is the default protocol; unknown platforms
// return an empty string, and adaptive selection uses that default address.
export function defaultCNBaseUrl(
  platform: string,
  mode: CnProviderMode,
  protocol: CnApiProtocol = 'chat_completions',
): string {
  if (protocol === 'anthropic') {
    switch (platform) {
      case 'kimi':
        return mode === 'coding'
          ? 'https://api.kimi.com/coding'
          : 'https://api.moonshot.cn/anthropic'
      case 'zhipu':
        return 'https://open.bigmodel.cn/api/anthropic'
      case 'deepseek':
        return 'https://api.deepseek.com/anthropic'
      default:
        return ''
    }
  }
  // Responses and Chat Completions share the base address; the backend
  // appends the protocol-specific endpoint path.
  switch (platform) {
    case 'kimi':
      return mode === 'coding' ? 'https://api.kimi.com/coding/v1' : 'https://api.moonshot.cn/v1'
    case 'zhipu':
      return mode === 'coding'
        ? 'https://open.bigmodel.cn/api/coding/paas/v4'
        : 'https://open.bigmodel.cn/api/paas/v4'
    case 'deepseek':
      return 'https://api.deepseek.com'
    default:
      return ''
  }
}

// defaultCNAdaptiveBaseUrls returns separate endpoint defaults for every
// native protocol entry. Responses receives an empty string for platforms
// that do not support it, allowing forms to retain the full record shape.
export function defaultCNAdaptiveBaseUrls(
  platform: 'kimi' | 'zhipu' | 'deepseek',
  mode: CnProviderMode,
): Record<CnNativeApiProtocol, string> {
  return {
    chat_completions: defaultCNBaseUrl(platform, mode, 'chat_completions'),
    anthropic: defaultCNBaseUrl(platform, mode, 'anthropic'),
    responses: cnSupportsNativeResponses(platform)
      ? defaultCNBaseUrl(platform, mode, 'responses')
      : '',
  }
}
