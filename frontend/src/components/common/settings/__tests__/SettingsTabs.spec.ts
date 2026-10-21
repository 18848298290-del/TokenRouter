import { defineComponent, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import SettingsTabs from '../SettingsTabs.vue'

/**
 * SettingsTabs.spec.ts checks panel selection, keyboard focus, native validation,
 * onboarding reveal, and retained drafts across hidden tabs. Mounted inputs and a
 * simulated scroll result expose ordering between tab changes and field positioning,
 * with each wrapper unmounted after use.
 */

/**
 * tabs supplies ordered panel keys and labels reused across tab selection, validation,
 * and retained draft scenarios.
 */
const tabs = ['general', 'models', 'scheduling', 'protocol', 'request'].map((key) => ({
  key,
  label: key,
}))

afterEach(() => {
  document.body.innerHTML = ''
})

describe('SettingsTabs', () => {
  /**
   * A tab set receives five panel definitions in a fixed order. Buttons preserve that
   * order and clicking the request tab makes its matching panel visible.
   */
  it('按传入顺序渲染页签并切换面板', async () => {
    const wrapper = mount(SettingsTabs, { props: { idPrefix: 'create', tabs, label: 'tabs' } })
    expect(
      wrapper
        .findAll('[data-settings-tab-button]')
        .map((tab) => tab.attributes('data-settings-tab-button')),
    ).toEqual(['general', 'models', 'scheduling', 'protocol', 'request'])
    await wrapper.get('[data-settings-tab-button="request"]').trigger('click')
    expect(wrapper.get('[data-settings-tab="request"]').isVisible()).toBe(true)
    wrapper.unmount()
  })

  /**
   * A panel contains local draft state before keyboard navigation switches tabs. Focus
   * and scroll reset follow navigation, and returning to the panel retains its input
   * value.
   */
  it('切页保持内部草稿并支持键盘导航及滚动复位', async () => {
    /**
     * The child owns its draft locally, making an accidental panel unmount
     * observable when the test returns to the edited tab.
     */
    const Draft = defineComponent({
      setup: () => ({ value: ref('') }),
      template: '<input v-model="value" />',
    })
    const wrapper = mount(SettingsTabs, {
      attachTo: document.body,
      props: { idPrefix: 'edit', tabs, label: 'tabs' },
      slots: { models: Draft },
    })
    await wrapper.get('[data-settings-tab-button="models"]').trigger('click')
    await wrapper.get('input').setValue('draft')
    const content = wrapper.get('.settings-tab-content').element
    content.scrollTop = 300
    await wrapper
      .get('[data-settings-tab-button="models"]')
      .trigger('keydown', { key: 'ArrowRight' })
    await flushPromises()
    expect(document.activeElement).toBe(
      wrapper.get('[data-settings-tab-button="scheduling"]').element,
    )
    expect(content.scrollTop).toBe(0)
    await wrapper.get('[data-settings-tab-button="scheduling"]').trigger('keydown', { key: 'Home' })
    expect(wrapper.get('[data-settings-tab-button="general"]').attributes('aria-selected')).toBe(
      'true',
    )
    await wrapper.get('[data-settings-tab-button="models"]').trigger('click')
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('draft')
    wrapper.unmount()
  })

  /**
   * An invalid numeric input lives inside a hidden tab panel. Validation reveals its
   * panel, focuses the input, and reports native invalidity after the panel is
   * visible.
   */
  it('显示无效输入所在页签后才报告原生校验错误', async () => {
    const wrapper = mount(SettingsTabs, {
      attachTo: document.body,
      props: { idPrefix: 'edit', tabs, label: 'tabs' },
      slots: { models: '<input type="number" min="0.001" value="-1" />' },
    })
    const field = wrapper.get('input').element as HTMLInputElement
    const report = vi.spyOn(field, 'reportValidity')
    expect(await wrapper.vm.validate()).toBe(false)
    expect(wrapper.get('[data-settings-tab="models"]').isVisible()).toBe(true)
    expect(document.activeElement).toBe(field)
    expect(report).toHaveBeenCalledOnce()
    wrapper.unmount()
  })

  /**
   * Onboarding reveal events target two fields in the general settings panel. Each
   * event leaves the matching panel visible after the asynchronous reveal work
   * settles.
   */
  it('创建引导可以往返定位倍率与通用字段', async () => {
    const wrapper = mount(SettingsTabs, {
      props: { idPrefix: 'create', tabs, label: 'tabs' },
      slots: {
        general: '<input data-tour="group-form-name" /><input data-tour="group-form-multiplier" />',
      },
    })
    for (const [field, tab] of [
      ['multiplier', 'general'],
      ['name', 'general'],
    ]) {
      wrapper
        .get(`[data-tour="group-form-${field}"]`)
        .element.dispatchEvent(new Event('onboarding-reveal', { bubbles: true }))
      await flushPromises()
      expect(wrapper.get(`[data-settings-tab="${tab}"]`).isVisible()).toBe(true)
    }
    wrapper.unmount()
  })

  /**
   * An error field is revealed after another tab is selected. Its requested scroll
   * position and focus survive the tab switch scroll reset after pending updates
   * complete.
   */
  it('跨页定位错误字段后保留滚动位置', async () => {
    const wrapper = mount(SettingsTabs, {
      attachTo: document.body,
      props: { idPrefix: 'edit', tabs, label: 'tabs' },
      slots: { general: '<input data-testid="probe-model" />' },
    })
    try {
      await wrapper.get('[data-settings-tab-button="models"]').trigger('click')
      const content = wrapper.get('.settings-tab-content').element
      const field = wrapper.get('[data-testid="probe-model"]').element as HTMLInputElement

      /**
       * jsdom has no layout engine, so this stub records the scroll position
       * produced when a browser reveals the requested error field.
       */
      field.scrollIntoView = vi.fn(() => {
        content.scrollTop = 480
      })

      await wrapper.vm.revealField('[data-testid="probe-model"]')
      await flushPromises()

      expect(wrapper.get('[data-settings-tab="general"]').isVisible()).toBe(true)
      expect(document.activeElement).toBe(field)
      expect(content.scrollTop).toBe(480)
    } finally {
      wrapper.unmount()
    }
  })

  /**
   * The active quota tab becomes hidden after its editor receives a draft. Selection
   * returns to the first visible tab while the mounted input retains its value.
   */
  it('隐藏当前页签时回到首个可见页签并保留面板挂载', async () => {
    const Draft = defineComponent({
      setup: () => ({ value: ref('') }),
      template: '<input v-model="value" />',
    })
    const wrapper = mount(SettingsTabs, {
      props: { idPrefix: 'edit', tabs, label: 'tabs' },
      slots: { quota: Draft },
    })
    await wrapper.setProps({ tabs: [...tabs, { key: 'quota', label: 'quota' }] })
    await wrapper.get('[data-settings-tab-button="quota"]').trigger('click')
    await wrapper.get('input').setValue('draft')
    await wrapper.setProps({ tabs: [...tabs, { key: 'quota', label: 'quota', hidden: true }] })
    expect(wrapper.find('[data-settings-tab-button="quota"]').exists()).toBe(false)
    expect(wrapper.get('[data-settings-tab="general"]').isVisible()).toBe(true)
    expect((wrapper.get('input').element as HTMLInputElement).value).toBe('draft')
    wrapper.unmount()
  })
})
