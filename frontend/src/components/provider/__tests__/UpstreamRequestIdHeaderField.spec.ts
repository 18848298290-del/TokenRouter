import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UpstreamRequestIdHeaderField from '../UpstreamRequestIdHeaderField.vue'
import zh from '@/i18n/locales/zh/admin/providers'
import en from '@/i18n/locales/en/admin/providers'

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

function mountField(props: { platform?: string; type?: string; modelValue?: string }) {
  return mount(UpstreamRequestIdHeaderField, {
    props,
    global: {
      stubs: { Teleport: true },
    },
  })
}

describe('UpstreamRequestIdHeaderField', () => {
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

  it('lists only the official header for providers that connect to the platform directly', () => {
    const wrapper = mountField({ platform: 'anthropic', type: 'oauth' })
    const text = wrapper.text()

    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.official:Anthropic')
    expect(text).toContain('request-id')
    expect(text).not.toContain('X-Client-Request-ID')
    expect(text).not.toContain('X-Oneapi-Request-Id')
  })

  it('omits the examples section when no header is known for the platform', () => {
    const wrapper = mountField({ platform: 'kimi', type: 'oauth' })
    const text = wrapper.text()

    expect(text).toContain('admin.providers.upstreamRequestIdHeaderHelp.intro')
    expect(text).not.toContain('admin.providers.upstreamRequestIdHeaderHelp.examplesTitle')
    expect(wrapper.findAll('code')).toHaveLength(0)
  })

  // The query default differs from usage recording when the setting is blank.
  // Both translations must explain that distinction instead of promising usage backfill.
  it('explains the query default and unchanged empty usage identifier', () => {
    expect(zh.providers.upstreamRequestIdHeaderHelp.intro).toContain('请求查询默认读取 X-Request-ID')
    expect(zh.providers.upstreamRequestIdHeaderHelp.intro).toContain('使用记录的“上游ID”仍为空')
    expect(en.providers.upstreamRequestIdHeaderHelp.intro).toContain('request lookup reads X-Request-ID')
    expect(en.providers.upstreamRequestIdHeaderHelp.intro).toContain('Upstream ID in usage records remains empty')
    expect(zh.providers.upstreamRequestIdHeaderPlaceholder).toContain('X-Request-ID')
    expect(en.providers.upstreamRequestIdHeaderPlaceholder).toContain('X-Request-ID')
  })

  it('binds the header name through v-model', async () => {
    const wrapper = mountField({ platform: 'openai', type: 'apikey', modelValue: 'X-Request-ID' })
    const input = wrapper.get('[data-testid="upstream-request-id-header"]')
    expect((input.element as HTMLInputElement).value).toBe('X-Request-ID')

    await input.setValue('X-Oneapi-Request-Id')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['X-Oneapi-Request-Id'])
  })
})
