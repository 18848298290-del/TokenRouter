import { describe, expect, it, vi } from 'vitest'
import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import ResponsesWSConnectionModeSelector from '../ResponsesWSConnectionModeSelector.vue'

// ResponsesWSConnectionModeSelector.spec.ts tests connection-mode labels and emitted
// selection values. A native select stub makes changes observable through ordinary DOM
// events while the wrapper continues to supply the shared section title, scope, and
// mode description.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// SelectStub renders the shared selector options as a native select and forwards change
// events so the wrapper test can exercise its model payload.
const SelectStub = defineComponent({
  props: { modelValue: String, options: Array },
  emits: ['update:modelValue'],
  // setup provides the Select stub state used by these mode-selection tests. Its exposed
  // props and emit function let the render function forward a chosen value through the component
  // model event.
  setup(props, { emit, attrs }) {
    return () =>
      h(
        'select',
        {
          ...attrs,
          value: props.modelValue,
          onChange: (event: Event) =>
            emit('update:modelValue', (event.target as HTMLSelectElement).value),
        },
        (
          props.options as {
            value: string // Selector option wire value.
            label: string // Displayed input label.
          }[]
        ).map((option) => h('option', { value: option.value }, option.label)),
      )
  },
})

// mountSelector mounts the connection selector with the supplied mode and a native
// Select stub. The stub exposes ordinary change events so tests can inspect the emitted
// mode value.
function mountSelector(modelValue: 'pooled' | 'per_session') {
  return mount(ResponsesWSConnectionModeSelector, {
    props: { modelValue },
    global: { stubs: { Select: SelectStub } },
  })
}

describe('ResponsesWSConnectionModeSelector', () => {
  // The selector receives the per-session connection mode from its owning form. It must render
  // the section title, scope explanation, and current mode description from the shared
  // labels.
  it('shows the section title, scope and the description of the current mode', () => {
    const text = mountSelector('per_session').text()

    expect(text).toContain('admin.providers.openai.wsConnectionTitle')
    expect(text).toContain('admin.providers.openai.wsConnectionScope')
    expect(text).toContain('admin.providers.openai.wsConnectionPerSessionHint')
  })

  // The administrator changes the connection mode through the shared Select control. The
  // wrapper must emit the chosen mode value so the owning provider form can persist it.
  it('emits the selected connection mode', async () => {
    const wrapper = mountSelector('pooled')

    await wrapper.get('[data-testid="responses-ws-connection-select"]').setValue('per_session')

    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['per_session'])
  })
})
