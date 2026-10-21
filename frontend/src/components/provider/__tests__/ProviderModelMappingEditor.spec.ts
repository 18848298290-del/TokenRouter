import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount, type VueWrapper } from '@vue/test-utils'

import ProviderModelMappingEditor from '../ProviderModelMappingEditor.vue'
import type { ModelMappingRow } from '@/utils/modelMappingRules'

// ProviderModelMappingEditor.spec.ts tests provider mapping defaults, optional wildcard
// diagnostics, and forwarded row actions. Mounted controls expose their labels and
// emitted events while shared editor behavior remains visible through the wrapper used
// by provider forms.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// wrapper retains the most recently mounted editor for afterEach cleanup so tests can
// inspect its events and release its component resources.
let wrapper: VueWrapper | undefined
// Release the retained editor wrapper after each case so mounted component resources do
// not survive into the next interaction test.
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
})

// mountEditor mounts mapping rows with caller option overrides and saves the wrapper for
// afterEach cleanup. Tests inspect its rendered diagnostics and emitted row actions
// through the returned wrapper.
const mountEditor = (rows: ModelMappingRow[], props: Record<string, unknown> = {}) => {
  wrapper = mount(ProviderModelMappingEditor, {
    props: { modelValue: rows, testId: 'mapping', ...props },
    global: { stubs: { Icon: true, 'transition-group': true } },
  })
  return wrapper
}

describe('ProviderModelMappingEditor', () => {
  // The mapping wrapper mounts with no optional labels or rows supplied by its caller. It
  // must expose the provider-specific instructions, empty state, and request-model
  // placeholder defaults.
  it('默认显示提供商映射说明、空态和请求模型占位', () => {
    mountEditor([])
    expect(wrapper!.text()).toContain('admin.providers.mapRequestModels')
    expect(wrapper!.text()).toContain('admin.providers.modelMappingEmpty')
    expect(wrapper!.get('[data-testid="mapping-add"]').text()).toContain(
      'admin.providers.addMapping',
    )

    mountEditor([{ from: '', to: '' }])
    expect(wrapper!.get('[data-testid="mapping-source-0"]').attributes('placeholder')).toBe(
      'admin.providers.requestModel',
    )
  })

  // The caller enables wildcard diagnostics and supplies invalid source and target
  // patterns. The shared editor must receive the localized field errors produced for
  // those mapping rows.
  it('开启通配符提示后实时显示错误', () => {
    mountEditor([{ from: 'a*b', to: 'c*' }], { wildcardValidation: true })

    const alerts = wrapper!.findAll('[role="alert"]').map((alert) => alert.text())
    expect(alerts).toEqual([
      'admin.providers.wildcardOnlyAtEnd',
      'admin.providers.targetNoWildcard',
    ])
  })

  // The caller leaves optional wildcard diagnostics disabled while supplying invalid
  // patterns. The wrapper must keep live error messages absent so its default
  // presentation remains unchanged.
  it('未开启通配符提示时不显示错误', () => {
    mountEditor([{ from: 'a*b', to: 'c*' }])
    expect(wrapper!.find('[role="alert"]').exists()).toBe(false)
  })

  // The shared row editor emits add and remove actions through its provider wrapper. The
  // wrapper must publish an add event and retain the removal index in the event sent
  // to its parent.
  it('透传增删事件', async () => {
    const rows = [{ from: 'a', to: 'b' }]
    mountEditor(rows)

    await wrapper!.get('[data-testid="mapping-add"]').trigger('click')
    await wrapper!.get('[data-testid="mapping-remove-0"]').trigger('click')

    expect(wrapper!.emitted('add')).toHaveLength(1)
    expect(wrapper!.emitted('remove')?.[0]?.[1]).toBe(0)
  })
})
