import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import Collapse from '../Collapse.vue'
import Select from '../Select.vue'
import HelpTooltip from '../HelpTooltip.vue'
import { finishMotion, mockMotionEnvironment } from '@/__tests__/helpers/motion'

// floatingMotion.spec.ts tests how floating controls react when their containing
// Collapse closes. The fixtures keep selection state and externally controlled
// visibility available, so assertions distinguish retained model values from portals
// that must leave the hidden region.

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
// wrapper owns the currently mounted test component so teardown can release its
// resources after each case.
let wrapper: VueWrapper | undefined
beforeEach(() => mockMotionEnvironment())
// Release the resources retained by this case before the next one, keeping browser
// state and component lifecycle observations isolated across assertions.
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('折叠区域内的浮层', () => {
  // A Select opens inside a retained Collapse before its parent closes
  // programmatically. The portal becomes inert and exits, while reopening retains the
  // selected label and permits a fresh menu opening.
  it('父区程序化收起时同步关闭 teleported 选择框，重开保留选值', async () => {
    const Host = defineComponent({
      components: { Collapse, AppSelect: Select },
      props: { open: Boolean },
      setup: () => ({ value: ref('a'), options: [{ value: 'a', label: '选项 A' }] }),
      template:
        '<Collapse :open="open"><AppSelect v-model="value" :options="options" /></Collapse>',
    })
    wrapper = mount(Host, {
      attachTo: document.body,
      props: { open: true },
      global: { stubs: { transition: false, Icon: true } },
    })
    await wrapper.get('button').trigger('click')
    await flushPromises()
    const panel = document.body.querySelector('[role="listbox"]')!
    expect(panel).not.toBeNull()
    await wrapper.setProps({ open: false })
    await flushPromises()
    expect(panel.hasAttribute('inert')).toBe(true)
    await finishMotion(panel)
    expect(document.body.querySelector('[role="listbox"]')).toBeNull()
    await wrapper.setProps({ open: true })
    await finishMotion(wrapper.element)
    expect(wrapper.get('button').text()).toContain('选项 A')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(document.body.querySelector('[role="listbox"]')).not.toBeNull()
  })

  // A manually controlled tooltip remains requested open while its ancestor Collapse
  // closes. Ancestor blocking must isolate and hide the panel even before the parent
  // accepts its close request.
  it('受控提示即使尚未收到外部关闭值，也不会留在隐藏父区之外', async () => {
    const Host = defineComponent({
      components: { Collapse, HelpTooltip },
      props: { open: Boolean },
      template:
        '<Collapse :open="open"><HelpTooltip trigger="manual" :open="true" content="提示内容" /></Collapse>',
    })
    wrapper = mount(Host, {
      attachTo: document.body,
      props: { open: true },
      global: { stubs: { transition: false, Icon: true } },
    })
    await flushPromises()
    const tooltip = document.body.querySelector<HTMLElement>('[role="tooltip"]')!
    expect(tooltip.style.display).not.toBe('none')
    await wrapper.setProps({ open: false })
    await flushPromises()
    expect(tooltip.hasAttribute('inert')).toBe(true)
    await finishMotion(tooltip)
    expect(tooltip.style.display).toBe('none')
  })
})
