import { describe, expect, it } from 'vitest'
import type { Group } from '@/api/groups.types'
import {
  availableClients,
  buildClientConfig,
  modelsForProtocol,
  groupForKeyConfig,
  openCodeModelInfo,
  type ClientKind,
} from '../clientConfig'

/**
 * clientConfig.spec.ts checks client exports against protocol eligibility,
 * model aliases, and supported metadata. Assertions inspect generated shell
 * and configuration text alongside structured JSON, preserving concrete model
 * selection, credential placement, and the distinction between omitted
 * attributes and explicitly empty support.
 */

describe('客户端配置', () => {
  const base = {
    model: 'custom-model',
    baseUrl: 'https://api.example.com/v1/',
    apiKey: 'secret',
    shell: 'unix' as const,
  }
  /**
   * A group mixes protocol-specific models and wildcard routing rules. Only
   * concrete models admitted by the requested protocol may appear in the client
   * model list.
   */
  it('只保留入口协议可请求的模型，通配规则不伪装为模型', () => {
    expect(
      modelsForProtocol(
        {
          models: ['a', 'b', '*'],
          model_protocols: { a: ['anthropic_messages'], b: ['openai_responses'] },
        },
        'openai_responses',
      ),
    ).toEqual(['b'])
    expect(modelsForProtocol({ models: ['real', 'custom*'] }, 'anthropic_messages')).toEqual([
      'real',
    ])
  })
  /**
   * The available protocol list contains only the batch-image entry point. No
   * text client should be offered because the list has no supported text
   * protocol.
   */
  it('只有媒体入口时不显示 OpenCode', () => expect(availableClients(['image_batches'])).toEqual([]))
  /**
   * Every supported client receives the same concrete selected model. Generated
   * content must retain that model and avoid appending an unrelated
   * provider-specific routing path.
   */
  it.each<ClientKind>(['claude', 'codex', 'gemini', 'grok', 'opencode'])(
    '%s 使用明确模型，不附加专用平台路由',
    (client) => {
      const files = buildClientConfig({ ...base, client, protocol: 'openai_responses' })
      expect(files.length).toBeGreaterThan(0)
      expect(files.map((file) => file.content).join('\n')).toContain('custom-model')
      expect(files.map((file) => file.content).join('\n')).not.toContain('/antigravity')
    },
  )
  /**
   * Codex exports are generated with default and direct credential settings.
   * Default output includes auth.json, while direct authentication and WebSocket
   * options appear independently in TOML.
   */
  it('Codex 默认使用 auth.json，显式认证与 WS 可独立打开', () => {
    const original = buildClientConfig({ ...base, client: 'codex', protocol: 'openai_responses' })
    expect(JSON.parse(original[1].content)).toEqual({ OPENAI_API_KEY: 'secret' })
    const direct = buildClientConfig({
      ...base,
      client: 'codex',
      protocol: 'openai_responses',
      directAuth: true,
      websocket: true,
    })
    expect(direct).toHaveLength(1)
    expect(direct[0].content).toContain('experimental_bearer_token = "secret"')
    expect(direct[0].content).toContain('responses_websockets_v2 = true')
    expect(direct[0].content).toContain('https://api.example.com/v1')
    expect(direct[0].content).not.toContain('/v1/v1')
  })
  /**
   * OpenCode receives the Gemini entry protocol with a selected model. Its JSON
   * must choose the Google adapter and v1beta endpoint while retaining the model
   * key.
   */
  it('OpenCode 根据入口协议选择 adapter', () => {
    const [file] = buildClientConfig({
      ...base,
      client: 'opencode',
      protocol: 'gemini_generate_content',
    })
    const config = JSON.parse(file.content)
    expect(config.provider.tokenrouter.npm).toBe('@ai-sdk/google')
    expect(config.provider.tokenrouter.options.baseURL).toBe('https://api.example.com/v1beta')
    expect(Object.keys(config.provider.tokenrouter.models)).toEqual(['custom-model'])
  })
  /**
   * Grok export receives a Responses model and gateway endpoint. Shell output
   * must contain the XAI credential variable, and TOML must preserve the
   * Responses backend selection.
   */
  it('Grok 配置保持 Responses 后端及用量端点', () => {
    const files = buildClientConfig({ ...base, client: 'grok', protocol: 'openai_responses' })
    expect(files[0].content).toContain('XAI_API_KEY')
    expect(files[1].content).toContain('api_backend = "responses"')
    expect(files[1].content).toContain('[model."custom-model"]')
  })
  /**
   * Credentials and models contain apostrophes, shell substitution text, or
   * quotes. Unix, PowerShell, and TOML output must retain those values using
   * their expected escaping forms.
   */
  it('shell 和 TOML 对特殊字符进行转义', () => {
    const [unix] = buildClientConfig({
      ...base,
      client: 'gemini',
      protocol: 'gemini_generate_content',
      apiKey: "key'$(id)",
    })
    expect(unix.content).toContain("'key'\"'\"'$(id)'")
    const [ps] = buildClientConfig({
      ...base,
      client: 'gemini',
      protocol: 'gemini_generate_content',
      apiKey: "key'$(id)",
      shell: 'powershell',
    })
    expect(ps.content).toContain("'key''$(id)'")
    const [toml] = buildClientConfig({
      ...base,
      model: 'a"b',
      client: 'codex',
      protocol: 'openai_responses',
    })
    expect(toml.content).toContain('model = "a\\"b"')
  })
  /**
   * A key mapping combines exact aliases, wildcard matches, and unavailable
   * targets. Exported public names must use one-hop target protocols without
   * mutating the original group.
   */
  it('Key 别名使用一跳目标的协议资格，不枚举通配符或无效目标', () => {
    const group = {
      models: ['gpt-real', 'claude-real'],
      model_protocols: { 'gpt-real': ['openai_responses'], 'claude-real': ['anthropic_messages'] },
    } as Group
    const result = groupForKeyConfig(group, {
      alias: 'gpt-real',
      'gpt-*': 'claude-real',
      'claude-real': 'missing',
      unavailable: 'missing',
      '*': 'missing',
    })!
    expect(result.models).toEqual(['gpt-real', 'alias'])
    expect(result.model_protocols).toEqual({
      'gpt-real': ['anthropic_messages'],
      alias: ['openai_responses'],
    })
    expect(group.models).toEqual(['gpt-real', 'claude-real'])
  })
  /**
   * Configuration generation receives an empty model or a wildcard rule. Both
   * inputs must produce no files because neither identifies a concrete selected
   * model.
   */
  it('空模型和通配符不能生成客户端配置', () => {
    expect(
      buildClientConfig({ ...base, model: '', client: 'codex', protocol: 'openai_responses' }),
    ).toEqual([])
    expect(
      buildClientConfig({ ...base, model: '*', client: 'codex', protocol: 'openai_responses' }),
    ).toEqual([])
  })
  /**
   * A public alias points to a model with display attributes. The alias must
   * inherit target metadata while leaving the original model and protocol
   * collections unchanged.
   */
  it('展示属性不改变模型和协议集合，别名导出使用目标属性', () => {
    const group = {
      models: ['real'],
      model_protocols: { real: ['openai_responses'] },
      model_attributes: { real: { tool_call: false, output_limit: 1 } },
    } as Group
    const result = groupForKeyConfig(group, { alias: 'real' })!
    expect(result.models).toEqual(['real', 'alias'])
    expect(result.model_protocols?.alias).toEqual(['openai_responses'])
    expect(result.model_attributes?.alias).toEqual({ tool_call: false, output_limit: 1 })
    expect(group.models).toEqual(['real'])
  })
  /**
   * OpenCode receives explicit false flags, empty modalities, and partial
   * limits. Exported JSON must preserve known empty values while omitting
   * unsupported fields and incomplete limits.
   */
  it('OpenCode 只导出 schema 接受的已知字段，保留 false 和空模态', () => {
    expect(
      openCodeModelInfo('public', {
        display_name: 'Custom',
        context: 100,
        input_limit: 80,
        output_limit: 20,
        reasoning: false,
        input_modalities: [],
        output_modalities: ['pdf'],
        structured_output: true,
        route_differences: true,
      }),
    ).toEqual({
      name: 'Custom',
      reasoning: false,
      limit: { context: 100, input: 80, output: 20 },
      modalities: { input: [], output: ['pdf'] },
    })
    expect(openCodeModelInfo('unknown', {})).toEqual({ name: 'unknown' })
    expect(openCodeModelInfo('partial', { context: 100 })).toEqual({ name: 'partial' })
  })
  /**
   * The same metadata is passed to OpenCode and Codex exports. Supported
   * OpenCode attributes must appear, while Codex output remains independent of
   * those metadata fields.
   */
  it('属性只进入支持的客户端格式，不作为请求参数写入其他配置', () => {
    const attributes = { context: 100, output_limit: 1, temperature: false }
    const [openCode] = buildClientConfig({
      ...base,
      client: 'opencode',
      protocol: 'openai_responses',
      attributes,
    })
    expect(JSON.parse(openCode.content).provider.tokenrouter.models['custom-model']).toEqual({
      name: 'custom-model',
      limit: { context: 100, output: 1 },
      temperature: false,
    })
    expect(
      buildClientConfig({ ...base, client: 'codex', protocol: 'openai_responses', attributes }),
    ).toEqual(buildClientConfig({ ...base, client: 'codex', protocol: 'openai_responses' }))
  })
  /**
   * Temperature and tool-call capabilities are conditional in model metadata.
   * OpenCode must export those capabilities as false while preserving the
   * unconditional reasoning capability.
   */
  it('OpenCode 把无法表达的条件能力导出为关闭', () => {
    expect(
      openCodeModelInfo('gpt-6-sol', {
        temperature: true,
        tool_call: true,
        reasoning: true,
        conditions: { temperature: { thinking: false }, tool_call: { efforts: ['none'] } },
      }),
    ).toEqual({ name: 'gpt-6-sol', temperature: false, tool_call: false, reasoning: true })
  })
})
