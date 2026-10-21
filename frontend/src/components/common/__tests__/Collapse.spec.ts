import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import Collapse from '../Collapse.vue'
import { finishMotion, mockMotionEnvironment, nextMotionFrame } from '@/__tests__/helpers/motion'

// Collapse.spec.ts tests retained form drafts and on-demand child lifetimes during
// collapse transitions. Controlled motion events reproduce interrupted exits, reduced
// motion, and field revelation, with DOM state and completion notifications asserted
// at each relevant phase.

// wrapper owns the currently mounted test component so teardown can release its
// resources after each case.
let wrapper: VueWrapper | undefined
beforeEach(() => mockMotionEnvironment())
// Release the resources retained by this case before the next one, keeping browser
// state and component lifecycle observations isolated across assertions.
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
})

describe('Collapse', () => {
  // A retained input is edited before the region closes and finishes its transition.
  // Reopening must keep the draft while closed content becomes inert immediately and
  // completion arrives after exit.
  it('保留草稿，关闭时立即隔离交互，退出完成后隐藏', async () => {
    const Draft = defineComponent({
      setup: () => ({ value: ref('') }),
      template: '<input v-model="value" />',
    })
    wrapper = mount(Collapse, {
      props: { open: true },
      slots: { default: Draft },
      global: { stubs: { transition: false } },
    })
    await wrapper.get('input').setValue('draft')
    await wrapper.setProps({ open: false })
    expect(wrapper.attributes('inert')).toBeDefined()
    expect(wrapper.get('input').exists()).toBe(true)
    expect(wrapper.emitted('after-leave')).toBeUndefined()
    await finishMotion(wrapper.element)
    expect((wrapper.element as HTMLElement).style.display).toBe('none')
    expect(wrapper.emitted('after-leave')).toHaveLength(1)
    await wrapper.setProps({ open: true })
    await finishMotion(wrapper.element)
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('draft')
    expect(wrapper.attributes('inert')).toBeUndefined()
    expect(wrapper.classes()).not.toContain('motion-collapse-moving')
  })

  // On-demand content begins hidden, opens, and reopens during an unfinished close.
  // The assertions protect retained content during interruption and unmount it after a
  // subsequent completed close.
  it('按需内容在退出完成后卸载，快速重开不会被旧完成事件清除', async () => {
    wrapper = mount(Collapse, {
      props: { open: false, unmountOnHide: true },
      slots: { default: '<input />' },
      global: { stubs: { transition: false } },
    })
    expect(wrapper.find('input').exists()).toBe(false)
    await wrapper.setProps({ open: true })
    await finishMotion(wrapper.element)
    await wrapper.setProps({ open: false })
    await nextMotionFrame()
    await wrapper.setProps({ open: true })
    await finishMotion(wrapper.element)
    expect(wrapper.find('input').exists()).toBe(true)
    expect(wrapper.emitted('after-leave')).toBeUndefined()
    await wrapper.setProps({ open: false })
    await finishMotion(wrapper.element)
    expect(wrapper.find('input').exists()).toBe(false)
  })

  // The parent clears slot text in the same update that closes an on-demand region.
  // The outgoing frame keeps its previous text, and reopening renders the latest
  // supplied text.
  it('清空业务数据时保留退出内容，重新打开后采用最新内容', async () => {
    const Host = defineComponent({
      components: { Collapse },
      props: { open: Boolean, text: String },
      template: '<Collapse :open="open" unmount-on-hide><p>{{ text }}</p></Collapse>',
    })
    wrapper = mount(Host, {
      props: { open: true, text: 'result' },
      global: { stubs: { transition: false } },
    })
    await wrapper.setProps({ open: false, text: '' })
    expect(wrapper.get('p').text()).toBe('result')
    await finishMotion(wrapper.element)
    await wrapper.setProps({ open: true, text: 'new result' })
    await finishMotion(wrapper.element)
    expect(wrapper.get('p').text()).toBe('new result')
    expect((wrapper.element as HTMLElement).style.height).toBe('')
  })

  // The environment requests reduced motion before an on-demand region closes. Its
  // content disappears and emits after-leave during the update without waiting for a
  // synthetic height transition.
  it('减少动态效果不等待高度过渡', async () => {
    vi.restoreAllMocks()
    mockMotionEnvironment(true)
    wrapper = mount(Collapse, {
      props: { open: true, unmountOnHide: true },
      slots: { default: '<input />' },
      global: { stubs: { transition: false } },
    })
    await wrapper.setProps({ open: false })
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.emitted('after-leave')).toHaveLength(1)
  })

  // A hidden retained field dispatches the validation reveal event through its host.
  // The same DOM update reveals the control, clears inert state, and reports entry
  // without leaving a moving class.
  it('校验定位在同一次 DOM 更新中展开保留的字段', async () => {
    const Host = defineComponent({
      components: { Collapse },
      setup: () => ({ open: ref(false) }),
      template:
        '<div @form-field-reveal="open = true"><Collapse :open="open"><input required /></Collapse></div>',
    })
    wrapper = mount(Host, { global: { stubs: { transition: false } } })
    await wrapper.get('input').trigger('form-field-reveal')
    const collapse = wrapper.getComponent(Collapse)
    expect((collapse.element as HTMLElement).style.display).not.toBe('none')
    expect(collapse.classes()).not.toContain('motion-collapse-moving')
    expect(collapse.attributes('inert')).toBeUndefined()
    expect(collapse.emitted('after-enter')).toHaveLength(1)
  })

  // An on-demand region closes with one required and one disabled input. Required
  // validation is suspended during exit, while reopening restores editable input state
  // and retains intentional disabling.
  it('退出中的按需字段不参与原生校验，重新打开恢复可编辑状态', async () => {
    wrapper = mount(Collapse, {
      props: { open: true, unmountOnHide: true },
      slots: { default: '<input required /><input disabled />' },
      global: { stubs: { transition: false } },
    })
    await wrapper.setProps({ open: false })
    expect((wrapper.get('input').element as HTMLInputElement).willValidate).toBe(false)
    await wrapper.setProps({ open: true })
    await finishMotion(wrapper.element)
    const inputs = wrapper.findAll('input')
    expect((inputs[0].element as HTMLInputElement).disabled).toBe(false)
    expect((inputs[1].element as HTMLInputElement).disabled).toBe(true)
  })

  // A region is unmounted while its close transition is pending. The assertion
  // protects removal of the motion preference listener so the detached instance does
  // not retain that subscription.
  it('卸载时移除媒体查询监听且不再调用完成事件', async () => {
    vi.restoreAllMocks()
    const environment = mockMotionEnvironment()
    wrapper = mount(Collapse, { props: { open: true }, global: { stubs: { transition: false } } })
    await wrapper.setProps({ open: false })
    wrapper.unmount()
    expect(environment.listeners.size).toBe(0)
    wrapper = undefined
  })
})
