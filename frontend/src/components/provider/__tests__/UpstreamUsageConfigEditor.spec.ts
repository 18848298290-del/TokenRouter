import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import UpstreamUsageConfigEditor from '../UpstreamUsageConfigEditor.vue'

// UpstreamUsageConfigEditor.spec.ts tests adapter selection and dependent management-
// query fields. A Select stub exposes model events so the cases can inspect New API
// wallet controls, Derouter address guidance, and hiding the editor when queries are
// disabled.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// SelectStub emits the New API value on click and displays the option count so tests
// can inspect adapter wiring without mounting the full dropdown.
const SelectStub = {
  props: ['modelValue', 'options'],
  emits: ['update:modelValue'],
  template:
    '<button data-testid="select" @click="$emit(\'update:modelValue\', \'new_api\')">{{ options?.length }}</button>',
}

describe('UpstreamUsageConfigEditor', () => {
  // The usage editor mounts with its default adapter and then receives disabled
  // query state from its parent. It must use the shared Select component and retain the supplied
  // configuration values.
  it('使用项目 Select 并保留默认配置与禁用状态', async () => {
    const wrapper = mount(UpstreamUsageConfigEditor, {
      props: { enabled: true, adapter: 'sub2api', baseUrl: '' },
      global: {
        stubs: { Select: SelectStub },
        mocks: { $t: (key: string) => key },
      },
    })
    expect(wrapper.find('[data-testid="upstream-usage-enabled"]').element).toBeTruthy()
    expect(wrapper.find('[data-testid="upstream-usage-adapter"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="upstream-usage-adapter"]').text()).toBe('4')
    await wrapper.find('[data-testid="upstream-usage-adapter"]').trigger('click')
    expect(wrapper.emitted('update:adapter')?.[0]).toEqual(['new_api'])

    await wrapper.setProps({ adapter: 'new_api' })
    expect(wrapper.find('[data-testid="upstream-usage-wallet-access-token"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="upstream-usage-wallet-user-id"]').exists()).toBe(true)
    await wrapper.find('[data-testid="upstream-usage-wallet-user-id"]').setValue('42')
    expect(wrapper.emitted('update:walletUserId')?.at(-1)).toEqual(['42'])

    await wrapper.setProps({ adapter: 'zivv' })
    expect(wrapper.find('[data-testid="upstream-usage-wallet-access-token"]').exists()).toBe(false)

    await wrapper.setProps({ enabled: false })
    expect(wrapper.find('[data-testid="select"]').exists()).toBe(false)
  })

  // The administrator chooses the Derouter usage adapter in the shared selector. The
  // editor must emit that adapter value and display its account-management address
  // guidance for the parent form.
  it('selects derouter and shows the account management default', async () => {
    const wrapper = mount(UpstreamUsageConfigEditor, {
      props: { enabled: true, adapter: 'sub2api', baseUrl: '' },
      global: { stubs: { Select: SelectStub } },
    })
    wrapper.findComponent(SelectStub).vm.$emit('update:modelValue', 'derouter')
    expect(wrapper.emitted('update:adapter')?.at(-1)).toEqual(['derouter'])
    await wrapper.setProps({ adapter: 'derouter' })
    expect(wrapper.get('[data-testid="upstream-usage-base-url"]').attributes('placeholder')).toBe(
      'https://cf-api.derouter.ai',
    )
    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.derouterBaseUrlHint')
    expect(wrapper.find('[data-testid="upstream-usage-wallet-access-token"]').exists()).toBe(false)
    await wrapper
      .get('[data-testid="upstream-usage-base-url"]')
      .setValue('https://management.example.test')
    expect(wrapper.emitted('update:baseUrl')?.at(-1)).toEqual(['https://management.example.test'])
  })
})
