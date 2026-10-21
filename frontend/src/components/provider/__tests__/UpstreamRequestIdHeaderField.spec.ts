import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamRequestIdHeaderField from '../UpstreamRequestIdHeaderField.vue'
import zh from '@/i18n/locales/zh/admin/providers'
import en from '@/i18n/locales/en/admin/providers'

// UpstreamRequestIdHeaderField.spec.ts tests response-header examples and the editable
// header-name model. Separate locale assertions preserve the shared default and
// configured-header guidance, while provider metadata determines which platform and
// relay examples appear in help.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params?.platform ? `${key}:${String(params.platform)}` : key,
    }),
  }
})

// mountField mounts the request-ID header field with caller-supplied provider metadata
// and draft value. Teleport is stubbed so the help content remains inspectable within the
// wrapper created for the test.
function mountField(props: {
  platform?: string // Provider platform identifier.
  type?: string // Provider authentication type.
  modelValue?: string // Parent-owned selected value.
}) {
  return mount(UpstreamRequestIdHeaderField, {
    props,
    global: {
      stubs: { Teleport: true },
    },
  })
}

describe('UpstreamRequestIdHeaderField', () => {
  // An OpenAI API-key provider opens the request-ID header help panel. The examples must
  // include relay header names, the platform header, and the relay-specific explanatory
  // note.
  it('lists relay and official header examples for API key providers', () => {
    const wrapper = mountField({ platform: 'openai', type: 'apikey' })
    const text = wrapper.text()

    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.intro')
    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.examplesTitle')
    expect(text).toContain('X-Client-Request-ID')
    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.sub2apiNote')
    expect(text).toContain('X-Oneapi-Request-Id')
    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.official:OpenAI')
    expect(text).toContain('x-request-id')
  })

  // An Anthropic OAuth provider opens the request-ID header help panel. The examples must
  // show the platform header while omitting suggestions gated on API-key credential type.
  it('lists only the official header for providers that connect to the platform directly', () => {
    const wrapper = mountField({ platform: 'anthropic', type: 'oauth' })
    const text = wrapper.text()

    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.official:Anthropic')
    expect(text).toContain('request-id')
    expect(text).not.toContain('X-Client-Request-ID')
    expect(text).not.toContain('X-Oneapi-Request-Id')
  })

  // The provider platform has no configured example and its type does not enable relay
  // suggestions. The help introduction must remain visible while the example section is
  // omitted.
  it('omits the examples section when no header is known for the platform', () => {
    const wrapper = mountField({ platform: 'kimi', type: 'oauth' })
    const text = wrapper.text()

    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.intro')
    expect(text).not.toContain('admin.providers.upstreamRequestIdHeaderHelp.examplesTitle')
    expect(wrapper.findAll('code')).toHaveLength(0)
  })

  // Both locale messages describe the shared request lookup and usage-recording header
  // rule. They must retain the empty-setting default, configured-header precedence, and
  // missing-header behavior in their guidance.
  it('explains the shared default and configured header selection', () => {
    expect(zh.providers.upstreamRequestIdHeaderHelp.intro).toContain(
      '请求查询和使用记录使用相同规则',
    )
    expect(zh.providers.upstreamRequestIdHeaderHelp.intro).toContain(
      '留空时默认读取上游响应头 X-Request-ID',
    )
    expect(zh.providers.upstreamRequestIdHeaderHelp.intro).toContain('填写后只读取指定头')
    expect(zh.providers.upstreamRequestIdHeaderHelp.intro).toContain('未返回或为空时不记录')
    expect(en.providers.upstreamRequestIdHeaderHelp.intro).toContain(
      'Request lookup and usage records use the same rule',
    )
    expect(en.providers.upstreamRequestIdHeaderHelp.intro).toContain(
      'an empty setting reads the upstream X-Request-ID response header',
    )
    expect(en.providers.upstreamRequestIdHeaderHelp.intro).toContain(
      'a configured setting reads only the selected header',
    )
    expect(en.providers.upstreamRequestIdHeaderHelp.intro).toContain(
      'A missing or empty selected header records no ID',
    )
    expect(zh.providers.upstreamRequestIdHeaderPlaceholder).toContain('X-Request-ID')
    expect(en.providers.upstreamRequestIdHeaderPlaceholder).toContain('X-Request-ID')
  })

  // The parent supplies an initial request-ID header name and the administrator edits it.
  // The rendered input must echo that value and emit the replacement through v-model.
  it('binds the header name through v-model', async () => {
    const wrapper = mountField({ platform: 'openai', type: 'apikey', modelValue: 'X-Request-ID' })
    const input = wrapper.get('[data-testid="upstream-request-id-header"]')
    expect((input.element as HTMLInputElement).value).toBe('X-Request-ID')

    await input.setValue('X-Oneapi-Request-Id')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['X-Oneapi-Request-Id'])
  })
})
