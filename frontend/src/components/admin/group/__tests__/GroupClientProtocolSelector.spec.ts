import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import GroupClientProtocolSelector from '../GroupClientProtocolSelector.vue'

// GroupClientProtocolSelector.spec.ts checks protocol controls against a stubbed shared
// capability catalog. Rendered rows expose supported endpoints and enablement, while conversion
// policy and image-policy events retain their distinct persisted values when selected by the
// administrator.

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))

describe('GroupClientProtocolSelector', () => {
  // The selector renders the catalog protocol rows with its current selection. The test
  // protects the compact row structure by rejecting an additional enclosing frame.
  it('renders the protocol rows without an outer frame', () => {
    const wrapper = mount(GroupClientProtocolSelector, {
      props: {
        modelValue: ['openai_responses', 'openai_chat_completions'],
      },
    })

    const list = wrapper.get('[data-testid="client-protocol-list"]')
    expect(list.classes()).toContain('divide-y')
    expect(list.classes()).not.toContain('border')
    expect(list.classes()).not.toContain('rounded-control')
  })

  // Each supported protocol is supplied by the catalog fixture. Rendered endpoint text must
  // match its canonical route so administrators see the request entry point being configured.
  it('shows the canonical endpoint for each supported protocol', () => {
    const wrapper = mount(GroupClientProtocolSelector, {
      props: {
        modelValue: [],
      },
    })

    expect(wrapper.get('[data-protocol-endpoint="anthropic_messages"]').text()).toContain(
      '/v1/messages',
    )
    expect(wrapper.get('[data-protocol-endpoint="openai_responses"]').text()).toContain(
      '/v1/responses',
    )
    expect(wrapper.get('[data-protocol-endpoint="openai_chat_completions"]').text()).toContain(
      '/v1/chat/completions',
    )
    expect(wrapper.get('[data-protocol-endpoint="gemini_generate_content"]').text()).toContain(
      '/v1beta/models/{model}:generateContent',
    )
  })

  // An enabled protocol row is rendered through the real toggle component. Its active
  // appearance uses the shared switch color classes expected across the settings interface.
  it('uses the shared enabled switch color', () => {
    const wrapper = mount(GroupClientProtocolSelector, {
      props: {
        modelValue: ['openai_responses'],
      },
    })

    expect(wrapper.get('[data-protocol="openai_responses"]').classes()).toContain('toggle-active')
    expect(wrapper.get('[data-protocol="openai_chat_completions"]').classes()).not.toContain(
      'toggle-active',
    )
  })

  // A protocol included in the catalog defaults is currently enabled. Toggling it emits a list
  // that removes the protocol, allowing an administrator to override those defaults.
  it('allows default protocols to be disabled', async () => {
    const wrapper = mount(GroupClientProtocolSelector, {
      props: {
        modelValue: ['openai_responses', 'openai_chat_completions'],
      },
    })

    expect(wrapper.get('[data-protocol="openai_responses"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-protocol="openai_responses"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual(['openai_chat_completions'])
  })

  // The selected list contains the final enabled protocol for a platform. Turning it off emits
  // an empty list, preserving the administrator choice to disable every entry.
  it('allows any platform to disable its final protocol', async () => {
    const wrapper = mount(GroupClientProtocolSelector, {
      props: {
        modelValue: ['anthropic_messages'],
      },
    })

    await wrapper.get('[data-protocol="anthropic_messages"]').trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)?.[0]).toEqual([])
  })
})

// A conversion-capable protocol is rendered with its entry switch and target controls. Target
// configuration remains separate and uses the shared Select component for updates.
it('转换目标与客户端入口开关独立，并使用项目 Select', async () => {
  const wrapper = mount(GroupClientProtocolSelector, {
    props: {
      modelValue: ['anthropic_messages'],
      fallbacks: { anthropic_messages: ['openai_responses'] },
    },
  })
  const selects = wrapper.findAllComponents({ name: 'Select' })
  expect(selects.length).toBeGreaterThan(0)
  const messages = selects.find((select) => select.props('modelValue') === 'openai_responses')!
  expect(messages.props('options')).toEqual(
    expect.arrayContaining([{ value: 'openai_responses', label: 'OpenAI Responses' }]),
  )
  messages.vm.$emit('update:modelValue', 'openai_chat_completions')
  expect(wrapper.emitted('update:fallbacks')?.at(-1)?.[0]).toEqual({
    anthropic_messages: ['openai_chat_completions'],
  })
  expect(wrapper.emitted('update:modelValue')).toBeUndefined()
})

useProtocolCatalogFixture()

// Automatic, native-only, and restricted conversion modes are selected in turn. Emitted values
// distinguish an omitted source key, an empty target list, and ordered target identifiers.
it('自动、仅原生和有序目标使用不同持久语义', async () => {
  const wrapper = mount(GroupClientProtocolSelector, {
    props: { modelValue: ['anthropic_messages'], fallbacks: {} },
  })
  const mode = wrapper.findAllComponents({ name: 'Select' })[0]
  expect(mode.props('modelValue')).toBe('auto')
  mode.vm.$emit('update:modelValue', 'native')
  expect(wrapper.emitted('update:fallbacks')?.at(-1)?.[0]).toEqual({ anthropic_messages: [] })
  await wrapper.setProps({
    fallbacks: { anthropic_messages: ['openai_responses', 'openai_chat_completions'] },
  })
  mode.vm.$emit('update:modelValue', 'auto')
  expect(wrapper.emitted('update:fallbacks')?.at(-1)?.[0]).toEqual({})
})

// Each image-policy choice is selected through the same control. The emitted update retains
// inherit, enabled, disabled, or block exactly so distinct policies reach the parent.
it.each(['inherit', 'enabled', 'disabled', 'block'] as const)('图片策略 %s 原样更新', (mode) => {
  const wrapper = mount(GroupClientProtocolSelector, {
    props: { modelValue: [], idPrefix: 'image-test' },
  })
  const field = wrapper
    .findAllComponents({ name: 'Select' })
    .find((select) => select.props('id') === 'image-test-image-policy')!
  expect(
    field.props('options').map(
      (option: {
        value: string // stored selector value
      }) => option.value,
    ),
  ).toEqual(['inherit', 'enabled', 'disabled', 'block'])
  field.vm.$emit('update:modelValue', mode)
  expect(wrapper.emitted('update:imagePolicy')?.at(-1)).toEqual([mode])
  wrapper.unmount()
})
