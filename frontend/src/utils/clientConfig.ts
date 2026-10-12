import type { ModelAttributes } from '@/types/modelAttributes'
import type { Group, ProtocolID } from '@/types'

export type ClientKind = 'claude' | 'codex' | 'gemini' | 'grok' | 'opencode'
export type ConfigShell = 'unix' | 'powershell' | 'cmd'
export interface ClientConfigFile { path: string; content: string }
export const CLIENT_PROTOCOLS: Record<Exclude<ClientKind, 'opencode'>, ProtocolID> = {
  claude: 'anthropic_messages', codex: 'openai_responses', gemini: 'gemini_generate_content', grok: 'openai_responses',
}
export const CLIENT_LABELS: Record<ClientKind, string> = {
  claude: 'Claude Code', codex: 'Codex CLI', gemini: 'Gemini CLI', grok: 'Grok CLI', opencode: 'OpenCode',
}
// 客户端按钮使用所属厂商的品牌图形；OpenCode 没有收录品牌图形，由调用方显示终端图标。
export const CLIENT_ICON_KEYS: Record<Exclude<ClientKind, 'opencode'>, string> = {
  claude: 'claude', codex: 'openai', gemini: 'gemini', grok: 'xai',
}
const textProtocols: ProtocolID[] = ['openai_responses', 'anthropic_messages', 'openai_chat_completions', 'gemini_generate_content']

// 可请求集合来自服务端，不用分组品牌或模型名称推断上游平台。
export function modelsForProtocol(group: Pick<Group, 'models' | 'model_protocols'> | undefined, protocol: ProtocolID): string[] {
  return (group?.models ?? []).filter(model => !model.includes('*') && (!group?.model_protocols || group.model_protocols[model]?.includes(protocol)))
}
// Key 重定向只执行一跳；精确规则优先，其次选择最长的尾通配前缀。
export function groupForKeyConfig(group: Group | undefined, mapping: Record<string, string> = {}): Group | undefined {
  if (!group) return undefined
  const requestable = new Set(group.models ?? [])
  const patterns = Object.keys(mapping).filter(source => source.endsWith('*')).sort((a, b) => b.length - a.length)
  const candidates = [...new Set([...(group.models ?? []), ...Object.keys(mapping).filter(source => !source.includes('*'))])]
  const modelProtocols: Record<string, ProtocolID[]> = {}
  const modelAttributes: Record<string, ModelAttributes> = {}
  const models = candidates.filter(model => {
    const pattern = patterns.find(source => model.startsWith(source.slice(0, -1)))
    const target = mapping[model] ?? (pattern ? mapping[pattern] : model)
    if (!requestable.has(target)) return false
    if (group.model_protocols) modelProtocols[model] = [...(group.model_protocols[target] ?? [])]
    if (group.model_attributes?.[target]) modelAttributes[model] = group.model_attributes[target]!
    return true
  })
  return { ...group, models, ...(group.model_attributes ? { model_attributes: modelAttributes } : {}), ...(group.model_protocols ? { model_protocols: modelProtocols } : {}) }
}
export function clientProtocol(client: ClientKind, protocols: readonly ProtocolID[]): ProtocolID | undefined {
  if (client === 'opencode') return textProtocols.find(protocol => protocols.includes(protocol))
  const protocol = CLIENT_PROTOCOLS[client]
  return protocols.includes(protocol) ? protocol : undefined
}
export function availableClients(protocols: readonly ProtocolID[]): ClientKind[] {
  return (Object.keys(CLIENT_LABELS) as ClientKind[]).filter(client => clientProtocol(client, protocols))
}
const quote = (value: string) => JSON.stringify(value)
const unixQuote = (value: string) => `'${value.replace(/'/g, `'"'"'`)}'`
function environment(values: Record<string, string>, shell: ConfigShell): string {
  return Object.entries(values).map(([key, value]) => {
    if (shell === 'powershell') return `$env:${key} = '${value.replace(/'/g, "''")}'`
    if (shell === 'cmd') return `set "${key}=${value.replace(/%/g, '%%').replace(/"/g, '^"')}"`
    return `export ${key}=${unixQuote(value)}`
  }).join('\n')
}

// 配置使用用户选定且服务端可请求的模型，分组和提供商仍按网关规则路由。
export function buildClientConfig(input: {
  client: ClientKind; protocol: ProtocolID; model: string; baseUrl: string; apiKey: string;
  shell: ConfigShell; websocket?: boolean; directAuth?: boolean; attributes?: ModelAttributes;
}): ClientConfigFile[] {
  if (!input.model || input.model.includes('*')) return []
  const { client, protocol, model, apiKey, shell } = input
  const base = input.baseUrl.replace(/\/+$/, '').replace(/\/v1(?:beta)?$/, '')
  const apiBase = `${base}/v1`
  const terminal = shell === 'unix' ? 'Terminal' : shell === 'cmd' ? 'CMD' : 'PowerShell'
  if (client === 'claude') return [{ path: terminal, content: environment({
    ANTHROPIC_BASE_URL: base, ANTHROPIC_AUTH_TOKEN: apiKey, ANTHROPIC_MODEL: model,
    ANTHROPIC_DEFAULT_OPUS_MODEL: model, ANTHROPIC_DEFAULT_SONNET_MODEL: model, ANTHROPIC_DEFAULT_HAIKU_MODEL: model,
  }, shell) }]
  if (client === 'gemini') return [{ path: terminal, content: environment({ GOOGLE_GEMINI_BASE_URL: base, GEMINI_API_KEY: apiKey, GEMINI_MODEL: model }, shell) }]
  if (client === 'codex') {
    const folder = shell === 'unix' ? '~/.codex/' : '%userprofile%\\.codex\\'
    const auth = input.directAuth ? `requires_openai_auth = false\nexperimental_bearer_token = ${quote(apiKey)}` : 'requires_openai_auth = true'
    const files = [{ path: `${folder}config.toml`, content: `model_provider = "tokenrouter"\nmodel = ${quote(model)}\nreview_model = ${quote(model)}\ndisable_response_storage = true\n\n[model_providers.tokenrouter]\nname = "TokenRouter"\nbase_url = ${quote(apiBase)}\nwire_api = "responses"\nsupports_websockets = ${input.websocket === true}\n${auth}${input.websocket ? '\n\n[features]\nresponses_websockets_v2 = true' : ''}` }]
    if (!input.directAuth) files.push({ path: `${folder}auth.json`, content: JSON.stringify({ OPENAI_API_KEY: apiKey }, null, 2) })
    return files
  }
  if (client === 'grok') return [
    { path: terminal, content: environment({ GROK_MODELS_BASE_URL: apiBase, XAI_API_KEY: apiKey }, shell) },
    { path: shell === 'unix' ? '~/.grok/config.toml' : '%userprofile%\\.grok\\config.toml', content: `[endpoints]
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
default = ${quote(model)}` },
  ]
  const npm = protocol === 'anthropic_messages' ? '@ai-sdk/anthropic' : protocol === 'gemini_generate_content' ? '@ai-sdk/google' : protocol === 'openai_chat_completions' ? '@ai-sdk/openai-compatible' : '@ai-sdk/openai'
  return [{ path: 'opencode.json', content: JSON.stringify({
    $schema: 'https://opencode.ai/config.json', model: `tokenrouter/${model}`,
    provider: { tokenrouter: { npm, name: 'TokenRouter', options: { baseURL: protocol === 'gemini_generate_content' ? `${base}/v1beta` : apiBase, apiKey }, models: { [model]: openCodeModelInfo(model, input.attributes) } } },
  }, null, 2) }]
}

// 仅导出 OpenCode schema 接受的字段；limit 需要 context 与 output 同时存在。
export function openCodeModelInfo(model: string, attributes?: ModelAttributes): Record<string, unknown> {
  const result: Record<string, unknown> = { name: attributes?.display_name ?? model }
  if (!attributes) return result
  for (const key of ['attachment', 'reasoning', 'temperature', 'tool_call'] as const) {
    if (attributes[key] !== undefined) {
      const conditional = (key === 'temperature' || key === 'tool_call') && attributes.conditions?.[key]
      result[key] = conditional ? false : attributes[key]
    }
  }
  if (attributes.context && attributes.output_limit) {
    result.limit = { context: attributes.context, output: attributes.output_limit, ...(attributes.input_limit ? { input: attributes.input_limit } : {}) }
  }
  if (attributes.input_modalities !== undefined || attributes.output_modalities !== undefined) {
    result.modalities = { ...(attributes.input_modalities !== undefined ? { input: attributes.input_modalities } : {}), ...(attributes.output_modalities !== undefined ? { output: attributes.output_modalities } : {}) }
  }
  return result
}
