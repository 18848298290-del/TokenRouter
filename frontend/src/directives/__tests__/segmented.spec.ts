import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { vSegmented } from '../segmented'

// segmented.spec.ts verifies segmented indicator geometry, changing option membership,
// and observer cleanup. Dataset offsets supply layout values missing from jsdom.
// Observer doubles trigger resize notifications, while mounted buttons expose identity,
// active-state accessibility, and cleanup when option membership changes.

// The suite retains its mounted control and created observer doubles so tests can
// trigger measurement and teardown can release browser replacements.
let wrapper: VueWrapper | undefined
const observers: ResizeObserverMock[] = []

// ResizeObserverMock records observation calls and exposes the callback for
// deterministic layout notifications.
class ResizeObserverMock {
  observe = vi.fn() // Observed element registration spy.
  unobserve = vi.fn() // Removed element observation spy.
  disconnect = vi.fn() // Observer resource cleanup spy.

  // constructor retains the supplied resize callback and registers this observer with
  // the suite. Tests call notify after changing dataset geometry to emulate a browser
  // layout notification.
  constructor(
    readonly notify: () => void, // Test-invoked resize notification callback.
  ) {
    observers.push(this)
  }
}

// Fixture renders caller-owned active classes and measured geometry so directive tests
// can distinguish indicator updates from button identity or accessibility changes.
const Fixture = defineComponent({
  directives: { segmented: vSegmented },
  props: {
    // active selects a zero-based option index; values matching no rendered index leave
    // every option unselected, including the negative input used by the cleanup test.
    active: { type: Number, default: 0 },
    count: { type: Number, default: 3 }, // Rendered option button count.
  },
  template: `
    <div v-segmented class="segmented">
      <button v-for="(_, index) in count" :key="index"
        class="segmented-item" :class="{ 'segmented-item-active': active === index }"
        :data-x="index * 80 + 2" data-y="2" :data-width="80 + index * 20" data-height="28"
        :aria-pressed="active === index">选项 {{ index }}</button>
    </div>`,
})

beforeEach(() => {
  observers.length = 0
  vi.stubGlobal('ResizeObserver', ResizeObserverMock)
  // jsdom does not calculate layout, so dataset-driven offsets supply the unequal widths
  // and wrapped positions needed to test geometry updates.
  for (const [property, field] of Object.entries({
    offsetLeft: 'x',
    offsetTop: 'y',
    offsetWidth: 'width',
    offsetHeight: 'height',
  })) {
    vi.spyOn(HTMLElement.prototype, property as 'offsetLeft', 'get').mockImplementation(function (
      this: HTMLElement,
    ) {
      return Number(this.dataset[field] || 0)
    })
  }
})

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('分段切换背景', () => {
  // A segmented control selects options with different measured widths. Indicator
  // geometry follows each selected button while the existing button elements and
  // pressed-state accessibility semantics remain intact.
  it('首次直接定位，切换到不同宽度的选项时不替换按钮或焦点语义', async () => {
    wrapper = mount(Fixture)
    const root = wrapper.element as HTMLElement
    const buttons = wrapper.findAll('button').map((button) => button.element)
    expect(root.hasAttribute('data-segmented-ready')).toBe(true)
    expect(root.style.getPropertyValue('--segmented-x')).toBe('2px')
    expect(root.style.getPropertyValue('--segmented-width')).toBe('80px')

    await wrapper.setProps({ active: 1 })
    expect(root.style.getPropertyValue('--segmented-x')).toBe('82px')
    expect(root.style.getPropertyValue('--segmented-width')).toBe('100px')
    expect(wrapper.findAll('button').map((button) => button.element)).toEqual(buttons)
    expect(buttons[1].getAttribute('aria-pressed')).toBe('true')

    await wrapper.setProps({ active: 2 })
    await wrapper.setProps({ active: 0 })
    expect(root.style.getPropertyValue('--segmented-x')).toBe('2px')
  })

  // The selected option changes position, width, and visibility between resize
  // notifications. Indicator geometry updates for wrapping and reappearance, while
  // hidden zero-width content clears readiness.
  it('文案或容器尺寸变化后更新位置，覆盖换行和隐藏后重现', () => {
    wrapper = mount(Fixture)
    const root = wrapper.element as HTMLElement
    const selected = wrapper.get('button').element as HTMLElement
    selected.dataset.x = '4'
    selected.dataset.y = '34'
    selected.dataset.width = '130'
    observers[0].notify()
    expect(root.style.getPropertyValue('--segmented-y')).toBe('34px')
    expect(root.style.getPropertyValue('--segmented-width')).toBe('130px')

    selected.dataset.width = '0'
    observers[0].notify()
    expect(root.hasAttribute('data-segmented-ready')).toBe(false)
    selected.dataset.width = '96'
    observers[0].notify()
    expect(root.hasAttribute('data-segmented-ready')).toBe(true)
    expect(root.style.getPropertyValue('--segmented-width')).toBe('96px')
  })

  // A segmented control begins without selection and later changes its option list.
  // Removed children stop observation, new children join observation, and unmount clears
  // observer resources and geometry.
  it('没有选中项时隐藏背景，动态选项和卸载会清理观察器', async () => {
    wrapper = mount(Fixture, { props: { active: -1 } })
    const root = wrapper.element as HTMLElement
    expect(root.hasAttribute('data-segmented-ready')).toBe(false)
    const removed = wrapper.findAll('button')[2].element
    await wrapper.setProps({ count: 2, active: 1 })
    expect(observers[0].unobserve).toHaveBeenCalledWith(removed)
    expect(root.style.getPropertyValue('--segmented-x')).toBe('82px')
    await wrapper.setProps({ count: 3, active: 2 })
    expect(observers[0].observe).toHaveBeenCalledWith(wrapper.findAll('button')[2].element)

    wrapper.unmount()
    wrapper = undefined
    expect(observers[0].disconnect).toHaveBeenCalledOnce()
    observers[0].notify()
    expect(root.hasAttribute('data-segmented-ready')).toBe(false)
    expect(root.style.getPropertyValue('--segmented-x')).toBe('')
  })
})
