import type { ModelAttributes } from '@/api/modelAttributes.types'
import type { Group } from '@/api/groups.types'
import type { ProtocolID } from '@/api/protocols.types'

/**
 * clientConfig.ts exports selected gateway models into client configuration
 * files and shell assignments. Client availability follows protocol support
 * supplied by the server, while model lists exclude wildcard rules that cannot
 * identify one requestable model.
 *
 * API-key aliases retain their public names but derive protocol and display
 * attributes from a single mapped target. Exact mappings precede the longest
 * wildcard prefix, and targets outside the requestable set are removed.
 * Generated configurations then apply client-specific endpoint and quoting
 * rules. OpenCode receives a restricted attribute subset, including explicit
 * false and empty modalities, while other clients receive only their supported
 * configuration fields.
 */

/**
 * ClientKind names the client formats supported by gateway configuration
 * export.
 */
export type ClientKind = 'claude' | 'codex' | 'gemini' | 'grok' | 'opencode'
/**
 * ConfigShell selects the quoting rules and paths used by generated
 * instructions.
 */
export type ConfigShell = 'unix' | 'powershell' | 'cmd'
/**
 * ClientConfigFile pairs a destination path or terminal label with exported
 * configuration text.
 */
export interface ClientConfigFile {
  path: string // destination file path
  content: string // exported configuration text
}
/**
 * CLIENT_PROTOCOLS maps vendor clients to gateway protocols, CLIENT_LABELS and
 * CLIENT_ICON_KEYS supply selection presentation, and textProtocols defines
 * OpenCode preference order. OpenCode uses the caller terminal icon rather
 * than a brand entry.
 */
export const CLIENT_PROTOCOLS: Record<Exclude<ClientKind, 'opencode'>, ProtocolID> = {
  claude: 'anthropic_messages',
  codex: 'openai_responses',
  gemini: 'gemini_generate_content',
  grok: 'openai_responses',
}
export const CLIENT_LABELS: Record<ClientKind, string> = {
  claude: 'Claude Code',
  codex: 'Codex CLI',
  gemini: 'Gemini CLI',
  grok: 'Grok CLI',
  opencode: 'OpenCode',
}

export const CLIENT_ICON_KEYS: Record<Exclude<ClientKind, 'opencode'>, string> = {
  claude: 'claude',
  codex: 'openai',
  gemini: 'gemini',
  grok: 'xai',
}
const textProtocols: ProtocolID[] = [
  'openai_responses',
  'anthropic_messages',
  'openai_chat_completions',
  'gemini_generate_content',
]

/**
 * modelsForProtocol filters concrete group models by the requested protocol.
 * Missing protocol metadata permits all concrete listed models, whereas a
 * present map requires the model entry to include the protocol.
 */
export function modelsForProtocol(
  group: Pick<Group, 'models' | 'model_protocols'> | undefined,
  protocol: ProtocolID,
): string[] {
  return (group?.models ?? []).filter(
    (model) =>
      !model.includes('*') &&
      (!group?.model_protocols || group.model_protocols[model]?.includes(protocol)),
  )
}

/**
 * groupForKeyConfig derives the models exposed through a key's one-hop alias
 * mapping. Exact rules override the longest matching wildcard prefix, and only
 * targets in the group's requestable set survive.
 *
 * The returned group keeps public source names while copying target protocol
 * arrays and retaining target attribute object references. Wildcard sources
 * are selection rules rather than enumerated aliases. The original group is
 * not rewritten, but its nested attribute values remain shared; consumers must
 * not assume this is a deep independent copy.
 */
export function groupForKeyConfig(
  group: Group | undefined,
  mapping: Record<string, string> = {},
): Group | undefined {
  if (!group) return undefined
  const requestable = new Set(group.models ?? [])
  const patterns = Object.keys(mapping)
    .filter((source) => source.endsWith('*'))
    .sort((a, b) => b.length - a.length)
  const candidates = [
    ...new Set([
      ...(group.models ?? []),
      ...Object.keys(mapping).filter((source) => !source.includes('*')),
    ]),
  ]
  const modelProtocols: Record<string, ProtocolID[]> = {}
  const modelAttributes: Record<string, ModelAttributes> = {}
  const models = candidates.filter((model) => {
    const pattern = patterns.find((source) => model.startsWith(source.slice(0, -1)))
    /**
     * Resolve only one mapping hop, preferring exact names over the longest
     * wildcard prefix, so alias metadata follows the same selected target as
     * eligibility.
     */
    const target = mapping[model] ?? (pattern ? mapping[pattern] : model)
    if (!requestable.has(target)) return false
    if (group.model_protocols) modelProtocols[model] = [...(group.model_protocols[target] ?? [])]
    if (group.model_attributes?.[target]) modelAttributes[model] = group.model_attributes[target]!
    return true
  })
  return {
    ...group,
    models,
    ...(group.model_attributes ? { model_attributes: modelAttributes } : {}),
    ...(group.model_protocols ? { model_protocols: modelProtocols } : {}),
  }
}
/**
 * clientProtocol selects a supported protocol for one client from the supplied
 * list. OpenCode uses the fixed text-protocol preference order, while named
 * vendor clients require their configured protocol to be present.
 */
export function clientProtocol(
  client: ClientKind,
  protocols: readonly ProtocolID[],
): ProtocolID | undefined {
  if (client === 'opencode') return textProtocols.find((protocol) => protocols.includes(protocol))
  const protocol = CLIENT_PROTOCOLS[client]
  return protocols.includes(protocol) ? protocol : undefined
}
/**
 * availableClients returns client kinds supported by the supplied protocol
 * collection.
 */
export function availableClients(protocols: readonly ProtocolID[]): ClientKind[] {
  return (Object.keys(CLIENT_LABELS) as ClientKind[]).filter((client) =>
    clientProtocol(client, protocols),
  )
}
/**
 * quote converts a string to the JSON literal used in generated configuration.
 */
const quote = (value: string) => JSON.stringify(value)
/**
 * unixQuote represents a value as a single shell argument using single-quoted
 * segments. Ordinary characters remain inside those segments, including dollar
 * signs that would otherwise invite shell expansion.
 *
 * An embedded apostrophe cannot remain inside a single-quoted segment, so the
 * replacement closes that segment, emits the apostrophe through a
 * double-quoted segment, and resumes single quoting. The result is intended
 * for generated Unix shell assignments. Callers provide the original unquoted
 * value and use separate rules for PowerShell or CMD.
 */
const unixQuote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`
/**
 * environment emits assignments for the selected shell while retaining entry
 * order from the supplied values object. Callers provide environment-variable
 * names and unquoted values, and receive newline-separated instructions for
 * export.
 *
 * Unix values use quote-segment handling for apostrophes, PowerShell doubles
 * apostrophes inside its single-quoted form, and CMD escapes the percent and
 * quote characters handled by this exporter. These branches encode different
 * shell representations instead of sharing a generic quote wrapper. The helper
 * produces text only and does not execute the assignments.
 */
function environment(values: Record<string, string>, shell: ConfigShell): string {
  /**
   * Apply quoting for the selected shell before joining assignments, preserving
   * embedded apostrophes and the distinct expansion syntax handled by each
   * output format.
   */
  return Object.entries(values)
    .map(([key, value]) => {
      if (shell === 'powershell') return `$env:${key} = '${value.replace(/'/g, "''")}'`
      if (shell === 'cmd') return `set "${key}=${value.replace(/%/g, '%%').replace(/"/g, '^"')}"`
      return `export ${key}=${unixQuote(value)}`
    })
    .join('\n')
}

/**
 * buildClientConfig exports files or shell instructions for a concrete
 * selected model. Blank models and wildcard names produce an empty list, while
 * the caller supplies a client and protocol combination already admitted by
 * the UI.
 *
 * Base URLs lose their terminal API suffix before client-specific suffixes are
 * added. Codex places credentials in auth.json unless directAuth embeds the
 * bearer token in TOML, independently of the websocket option. OpenCode
 * chooses an adapter from the selected protocol and uses filtered model
 * attributes. Returned content includes credentials and is intended for
 * explicit user export.
 */
export function buildClientConfig(input: {
  client: ClientKind // selected export client
  protocol: ProtocolID // selected gateway protocol
  model: string // selected request model
  baseUrl: string // gateway base URL
  apiKey: string // exported API credential
  shell: ConfigShell // instruction shell syntax
  /**
   * Enables generated Codex WebSocket settings when true; other clients ignore
   * this option during export.
   */
  websocket?: boolean
  /**
   * Embeds the Codex bearer token in TOML when true and omits the separate
   * auth.json export.
   */
  directAuth?: boolean
  /**
   * Optional model metadata contributes only fields supported by the chosen
   * client export format.
   */
  attributes?: ModelAttributes
}): ClientConfigFile[] {
  if (!input.model || input.model.includes('*')) return []
  const { client, protocol, model, apiKey, shell } = input
  const base = input.baseUrl.replace(/\/+$/, '').replace(/\/v1(?:beta)?$/, '')
  const apiBase = `${base}/v1`
  const terminal = shell === 'unix' ? 'Terminal' : shell === 'cmd' ? 'CMD' : 'PowerShell'
  if (client === 'claude')
    return [
      {
        path: terminal,
        content: environment(
          {
            ANTHROPIC_BASE_URL: base,
            ANTHROPIC_AUTH_TOKEN: apiKey,
            ANTHROPIC_MODEL: model,
            ANTHROPIC_DEFAULT_OPUS_MODEL: model,
            ANTHROPIC_DEFAULT_SONNET_MODEL: model,
            ANTHROPIC_DEFAULT_HAIKU_MODEL: model,
          },
          shell,
        ),
      },
    ]
  if (client === 'gemini')
    return [
      {
        path: terminal,
        content: environment(
          { GOOGLE_GEMINI_BASE_URL: base, GEMINI_API_KEY: apiKey, GEMINI_MODEL: model },
          shell,
        ),
      },
    ]
  if (client === 'codex') {
    const folder = shell === 'unix' ? '~/.codex/' : '%userprofile%\\.codex\\'
    /**
     * Direct authentication embeds the bearer token into the generated provider
     * table; otherwise auth.json carries the credential, independently of
     * WebSocket feature selection.
     */
    const auth = input.directAuth
      ? `requires_openai_auth = false\nexperimental_bearer_token = ${quote(apiKey)}`
      : 'requires_openai_auth = true'
    const files = [
      {
        path: `${folder}config.toml`,
        content: `model_provider = "tokenrouter"\nmodel = ${quote(model)}\nreview_model = ${quote(model)}\ndisable_response_storage = true\n\n[model_providers.tokenrouter]\nname = "TokenRouter"\nbase_url = ${quote(apiBase)}\nwire_api = "responses"\nsupports_websockets = ${input.websocket === true}\n${auth}${input.websocket ? '\n\n[features]\nresponses_websockets_v2 = true' : ''}`,
      },
    ]
    if (!input.directAuth)
      files.push({
        path: `${folder}auth.json`,
        content: JSON.stringify({ OPENAI_API_KEY: apiKey }, null, 2),
      })
    return files
  }
  if (client === 'grok')
    return [
      {
        path: terminal,
        content: environment({ GROK_MODELS_BASE_URL: apiBase, XAI_API_KEY: apiKey }, shell),
      },
      {
        path: shell === 'unix' ? '~/.grok/config.toml' : '%userprofile%\\.grok\\config.toml',
        content: `[endpoints]
models_base_url = ${quote(apiBase)}
models_list_url = ${quote(`${apiBase}/models`)}
xai_api_base_url = ${quote(apiBase)}
cli_chat_proxy_base_url = ${quote(apiBase)}

[auth]
preferred_method = "api_key"

[model.${quote(model)}]
model = ${quote(model)}
name = ${quote(input.attributes?.display_name ?? model)}
env_key = "XAI_API_KEY"
api_backend = "responses"

[models]
default = ${quote(model)}`,
      },
    ]
  const npm =
    protocol === 'anthropic_messages'
      ? '@ai-sdk/anthropic'
      : protocol === 'gemini_generate_content'
        ? '@ai-sdk/google'
        : protocol === 'openai_chat_completions'
          ? '@ai-sdk/openai-compatible'
          : '@ai-sdk/openai'
  return [
    {
      path: 'opencode.json',
      content: JSON.stringify(
        {
          $schema: 'https://opencode.ai/config.json',
          model: `tokenrouter/${model}`,
          provider: {
            tokenrouter: {
              npm,
              name: 'TokenRouter',
              options: {
                baseURL: protocol === 'gemini_generate_content' ? `${base}/v1beta` : apiBase,
                apiKey,
              },
              models: { [model]: openCodeModelInfo(model, input.attributes) },
            },
          },
        },
        null,
        2,
      ),
    },
  ]
}

/**
 * openCodeModelInfo selects the model attributes representable in OpenCode
 * configuration. The display name defaults to the public model name, and
 * absent attributes leave only that name.
 *
 * Conditional temperature or tool-call support cannot be represented as
 * unconditional booleans, so those fields become false when a condition is
 * present. Limits are emitted only when both context and output limits are
 * truthy. Explicit empty modality arrays and false capability values remain
 * present, preserving the difference between known empty support and missing
 * metadata.
 */
export function openCodeModelInfo(
  model: string,
  attributes?: ModelAttributes,
): Record<string, unknown> {
  const result: Record<string, unknown> = { name: attributes?.display_name ?? model }
  if (!attributes) return result
  for (const key of ['attachment', 'reasoning', 'temperature', 'tool_call'] as const) {
    if (attributes[key] !== undefined) {
      /**
       * OpenCode booleans cannot express request-dependent capability conditions, so
       * conditional temperature and tool support are exported as disabled rather
       * than unconditional promises.
       */
      const conditional =
        (key === 'temperature' || key === 'tool_call') && attributes.conditions?.[key]
      result[key] = conditional ? false : attributes[key]
    }
  }
  if (attributes.context && attributes.output_limit) {
    result.limit = {
      context: attributes.context,
      output: attributes.output_limit,
      ...(attributes.input_limit ? { input: attributes.input_limit } : {}),
    }
  }
  if (attributes.input_modalities !== undefined || attributes.output_modalities !== undefined) {
    result.modalities = {
      ...(attributes.input_modalities !== undefined ? { input: attributes.input_modalities } : {}),
      ...(attributes.output_modalities !== undefined
        ? { output: attributes.output_modalities }
        : {}),
    }
  }
  return result
}
