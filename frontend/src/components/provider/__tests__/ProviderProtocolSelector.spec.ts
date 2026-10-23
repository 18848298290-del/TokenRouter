import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import ProviderProtocolSelector from '../ProviderProtocolSelector.vue'
import Select from '@/components/common/Select.vue'
// ProviderProtocolSelector.spec.ts tests native protocol options and shared catalog
// recovery across provider and group selectors. Authentication changes replace provider
// selections, while explicit empty arrays stay empty when a failed catalog load later
// succeeds.

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('ProviderProtocolSelector', () => {
  // The catalog contains different native sets for platform, provider type, and
  // authentication combinations. Each case must show its native options and preserve the
  // explicitly empty selection supplied by the caller.
  it.each([
    [
      'openai',
      'apikey',
      '',
      ['openai_responses', 'openai_chat_completions', 'openai_embeddings'],
      ['anthropic_messages'],
    ],
    [
      'openai',
      'oauth',
      'personalAccessToken',
      ['openai_responses', 'openai_responses_websocket'],
      [
        'anthropic_messages',
        'openai_chat_completions',
        'openai_images_generations',
        'openai_alpha_search',
        'openai_live',
      ],
    ],
    [
      'grok',
      'oauth',
      '',
      ['openai_responses', 'grok_tts', 'grok_voice_realtime'],
      ['openai_responses_websocket', 'grok_web_search', 'openai_responses_compact'],
    ],
    [
      'zhipu',
      'apikey',
      '',
      ['anthropic_messages', 'openai_chat_completions'],
      ['openai_responses'],
    ],
    [
      'gemini',
      'service_account',
      '',
      ['gemini_generate_content', 'vertex_batch_prediction'],
      ['gemini_batch_generate_content', 'openai_images_generations'],
    ],
  ] as const)('%s/%s/%s 仅显示原生项', (platform, type, authMode, present, absent) => {
    const wrapper = mount(ProviderProtocolSelector, {
      props: { platform, type, authMode, modelValue: [] },
    })
    for (const id of present)
      expect(wrapper.find(`[data-native-protocol="${id}"]`).exists()).toBe(true)
    for (const id of absent)
      expect(wrapper.find(`[data-native-protocol="${id}"]`).exists()).toBe(false)
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })
  // An OpenAI OAuth provider changes authentication mode while Live is selected. The
  // emitted replacement must contain the personal access-token native set and remove
  // unsupported protocol choices.
  it('认证方式切换时收窄原生选项', async () => {
    const wrapper = mount(ProviderProtocolSelector, {
      props: { platform: 'openai', type: 'oauth', modelValue: ['openai_live'] },
    })
    await wrapper.setProps({ authMode: 'personalAccessToken' })
    await flushPromises()
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual([
      'openai_responses',
      'openai_responses_websocket',
      'openai_responses_compact',
    ])
  })
})

useProtocolCatalogFixture()

// Provider and group selectors mount while the shared catalog request fails, then one
// selector retries. Both must recover from the same successful load while preserving
// their explicitly empty selections.
it('提供商与分组选项共享错误恢复，保留显式关闭的提供商集合', async () => {
  const { protocolCatalog } = await import('@/api/admin/protocolCapabilities')
  const { default: client } = await import('@/api/client')
  const { default: fixture } = await import('@/__tests__/fixtures/protocol-catalog.json')
  const { default: GroupSelector } =
    await import('@/components/admin/group/GroupClientProtocolSelector.vue')
  protocolCatalog.value = null
  const request = vi.spyOn(client, 'get').mockRejectedValue(new Error('offline'))
  const provider = mount(ProviderProtocolSelector, {
    props: { platform: 'openai', type: 'apikey', modelValue: [] },
  })
  const group = mount(GroupSelector, { props: { platform: 'openai', modelValue: [] } })
  try {
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(1)
    expect(provider.find('[role="alert"]').exists()).toBe(true)
    expect(group.find('[role="alert"]').exists()).toBe(true)
    request.mockResolvedValue({ data: structuredClone(fixture) })
    await provider.get('[data-testid="protocol-catalog-retry"]').trigger('click')
    await flushPromises()
    expect(request).toHaveBeenCalledTimes(2)
    expect(provider.find('[role="alert"]').exists()).toBe(false)
    expect(group.find('[role="alert"]').exists()).toBe(false)
    expect(provider.find('[data-native-protocol="openai_responses"]').exists()).toBe(true)
    expect(group.find('[data-protocol="openai_responses"]').exists()).toBe(true)
    expect(provider.emitted('update:modelValue')).toBeUndefined()
    expect(group.emitted('update:modelValue')).toBeUndefined()
    // Restore the shared client spy and unmount both selectors even when an assertion fails,
    // keeping catalog dependency state isolated for later cases.
  } finally {
    request.mockRestore()
    provider.unmount()
    group.unmount()
  }
})

// Endpoint modes belong to independent enabled Images rows. Editing one mode must
// retain the other endpoint and toggling a protocol must not clear either draft value.
it('编辑生图模式时保留另一个端点与关闭协议的草稿', async () => {
  const wrapper = mount(ProviderProtocolSelector, {
    props: {
      platform: 'openai',
      type: 'apikey',
      modelValue: ['openai_images_generations', 'openai_images_edits'],
      imageModes: { images_generations_mode: 'both', images_edits_mode: 'streaming' },
    },
  })
  await flushPromises()
  const row = wrapper.get('[data-setting-row="image-mode-openai_images_generations"]')
  row.findComponent(Select).vm.$emit('update:modelValue', 'non_streaming')
  await flushPromises()
  expect(wrapper.emitted('update:imageModes')?.at(-1)?.[0]).toEqual({
    images_generations_mode: 'non_streaming',
    images_edits_mode: 'streaming',
  })
  await wrapper.setProps({ modelValue: ['openai_images_edits'] })
  expect(wrapper.props('imageModes')).toEqual({
    images_generations_mode: 'both',
    images_edits_mode: 'streaming',
  })
  expect(wrapper.emitted('update:imageModes')).toHaveLength(1)
  wrapper.unmount()
})
