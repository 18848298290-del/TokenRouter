import { nextMotionFrame } from '@/__tests__/helpers/motion'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import HelpTooltip from '@/components/common/HelpTooltip.vue'

// HelpTooltip.spec.ts tests tooltip activation, dismissal, and placement through
// rendered document panels. Measured rectangles exercise edge clamping and scrolling,
// while interactive content uses observer notifications and keyboard events to verify
// positioning and focus return.

// getTooltipElement returns the document tooltip as an HTMLDivElement for assertions.
// It throws when the expected panel is absent or has another element type, giving
// setup failures a direct message.
function getTooltipElement(): HTMLDivElement {
  const tooltip = document.body.querySelector('[role="tooltip"]')
  if (!(tooltip instanceof HTMLDivElement)) {
    throw new Error('tooltip element not found')
  }
  return tooltip
}

describe('HelpTooltip', () => {
  // Release the resources retained by this case before the next one, keeping browser
  // state and component lifecycle observations isolated across assertions.
  afterEach(() => {
    vi.restoreAllMocks()
    document.body.innerHTML = ''
  })

  // A manual tooltip receives controlled open updates and each supported outside
  // dismissal event. Hover and trigger clicks keep it hidden initially, while
  // dismissal requests permit a later controlled reopening.
  it.each(['pointerdown', 'touchstart', 'click', 'Escape'])(
    '手动提示受控显示，并通过外部 %s 关闭',
    async (eventName) => {
      const wrapper = mount(HelpTooltip, {
        attachTo: document.body,
        props: { trigger: 'manual', open: false, closable: false, content: '请同意条款' },
        slots: { trigger: '<input type="checkbox" />' },
      })
      const trigger = wrapper.get('.group')
      await trigger.trigger('mouseenter')
      await trigger.trigger('click')
      await nextMotionFrame()
      expect(getTooltipElement().style.display).toBe('none')
      await wrapper.setProps({ open: true })
      await nextTick()
      expect(getTooltipElement().style.display).not.toBe('none')
      expect(getTooltipElement().querySelector('button')).toBeNull()
      if (eventName === 'Escape') {
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
      } else {
        document.body.dispatchEvent(new Event(eventName, { bubbles: true }))
      }
      expect(wrapper.emitted('update:open')?.at(-1)).toEqual([false])
      await wrapper.setProps({ open: false })
      await nextMotionFrame()
      expect(getTooltipElement().style.display).toBe('none')
      await wrapper.setProps({ open: true })
      expect(getTooltipElement().style.display).not.toBe('none')
      wrapper.unmount()
    },
  )

  // A controlled tooltip is measured near the left viewport edge and shifted inward.
  // Its caret must still point at the checkbox center after the panel center is
  // clamped.
  it('提示框在左侧边缘避让时，箭头仍指向复选框', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: { trigger: 'manual', open: false },
    })
    wrapper.get('.group').element.getBoundingClientRect = vi.fn(
      () =>
        ({
          left: 30,
          top: 200,
          width: 16,
          height: 16,
          right: 46,
          bottom: 216,
        }) as DOMRect,
    )
    const tooltip = getTooltipElement()
    tooltip.getBoundingClientRect = vi.fn(() => ({ width: 288, height: 60 }) as DOMRect)
    await wrapper.setProps({ open: true })
    await nextTick()
    await nextTick()
    const center = Number.parseFloat(tooltip.style.left)
    const arrow = tooltip.lastElementChild as HTMLElement
    expect(center - 144 + Number.parseFloat(arrow.style.left)).toBe(38)
    wrapper.unmount()
  })

  // The tooltip uses its default trigger mode while the pointer enters and leaves its
  // trigger. It opens for hover and becomes hidden again after the outgoing motion
  // frame.
  it('keeps the existing hover interaction by default', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'hover details',
      },
    })

    const trigger = wrapper.get('.group')
    const tooltip = getTooltipElement()

    await nextMotionFrame()

    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('mouseenter')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    await trigger.trigger('mouseleave')
    await nextTick()
    await nextMotionFrame()
    expect(tooltip.style.display).toBe('none')

    wrapper.unmount()
  })

  // The pointer moves from the trigger into the tooltip and then back before leaving
  // both. The panel remains visible across internal moves and hides after the final
  // outside departure.
  it('keeps a hover tooltip open while the pointer moves between the trigger and the tooltip', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'copyable details',
      },
    })

    const trigger = wrapper.get('.group')
    const tooltip = getTooltipElement()

    await trigger.trigger('mouseenter')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    await trigger.trigger('mouseleave', { relatedTarget: tooltip })
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    tooltip.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: trigger.element }))
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    tooltip.dispatchEvent(new MouseEvent('mouseleave', { relatedTarget: null }))
    await nextTick()
    await nextMotionFrame()
    expect(tooltip.style.display).toBe('none')

    wrapper.unmount()
  })

  // A click-mode tooltip opens, closes through its button, opens again, and receives
  // an outside click. Each dismissal must hide the panel after its motion frame
  // completes.
  it('supports click-to-toggle details and closes on outside click', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'click details',
        trigger: 'click',
      },
    })

    const trigger = wrapper.get('.group')
    const tooltip = getTooltipElement()

    await nextMotionFrame()

    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('click')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')
    expect(tooltip.textContent).toContain('click details')

    const closeButton = tooltip.querySelector('button[aria-label="Close"]')
    if (!(closeButton instanceof HTMLButtonElement)) {
      throw new Error('close button not found')
    }
    closeButton.click()
    await nextTick()
    await nextMotionFrame()
    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('click')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    await nextMotionFrame()
    expect(tooltip.style.display).toBe('none')

    wrapper.unmount()
  })

  // A combined-mode tooltip is first hovered and then pinned by clicking. Hover exit
  // dismisses the unpinned panel, while a pinned panel stays open until an outside
  // click.
  it('supports hover and click together, with click keeping details open', async () => {
    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'combined details',
        trigger: 'both',
        placement: 'bottom',
      },
    })

    const trigger = wrapper.get('.group')
    const tooltip = getTooltipElement()

    await trigger.trigger('mouseenter')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    await trigger.trigger('mouseleave')
    await nextTick()
    await nextMotionFrame()
    expect(tooltip.style.display).toBe('none')

    await trigger.trigger('click')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    await trigger.trigger('mouseleave')
    await nextTick()
    expect(tooltip.style.display).not.toBe('none')

    document.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await nextTick()
    await nextMotionFrame()
    expect(tooltip.style.display).toBe('none')

    wrapper.unmount()
  })

  // A bottom-preferred tooltip has insufficient space below its measured trigger and
  // enough space above. The resulting top coordinate and translation class must
  // position the panel above the trigger.
  it('flips a bottom tooltip above the trigger when the viewport has no space below', async () => {
    vi.spyOn(window, 'innerHeight', 'get').mockReturnValue(720)

    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'positioned details',
        placement: 'bottom',
      },
    })

    const trigger = wrapper.get('.group')
    trigger.element.getBoundingClientRect = vi.fn(() => ({
      top: 620,
      left: 600,
      width: 24,
      height: 24,
      right: 624,
      bottom: 644,
      x: 600,
      y: 620,
      toJSON: () => ({}),
    }))
    const tooltip = getTooltipElement()
    tooltip.getBoundingClientRect = vi.fn(() => ({
      top: 0,
      left: 0,
      width: 320,
      height: 240,
      right: 320,
      bottom: 240,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    }))

    await trigger.trigger('mouseenter')
    await nextTick()

    expect(tooltip.style.top).toBe('calc(612px)')
    expect(tooltip.classList).toContain('-translate-y-full')

    wrapper.unmount()
  })

  // The page has nonzero scroll offsets while the trigger exposes viewport-relative
  // bounds. Panel coordinates follow those bounds before and after scrolling without
  // adding document scroll offsets.
  it('uses viewport coordinates and follows the trigger after scrolling', async () => {
    vi.spyOn(window, 'scrollX', 'get').mockReturnValue(240)
    vi.spyOn(window, 'scrollY', 'get').mockReturnValue(600)

    const wrapper = mount(HelpTooltip, {
      attachTo: document.body,
      props: {
        content: 'scroll details',
      },
    })

    const trigger = wrapper.get('.group')
    const getBoundingClientRect = vi.fn(() => ({
      top: 180,
      left: 120,
      width: 60,
      height: 20,
      right: 180,
      bottom: 200,
      x: 120,
      y: 180,
      toJSON: () => ({}),
    }))
    trigger.element.getBoundingClientRect = getBoundingClientRect

    await trigger.trigger('mouseenter')
    await nextTick()

    const tooltip = getTooltipElement()
    expect(tooltip.style.top).toBe('calc(172px)')
    expect(tooltip.style.left).toBe('150px')

    getBoundingClientRect.mockReturnValue({
      top: 80,
      left: 40,
      width: 60,
      height: 20,
      right: 100,
      bottom: 100,
      x: 40,
      y: 80,
      toJSON: () => ({}),
    })
    window.dispatchEvent(new Event('scroll'))
    await nextTick()

    expect(tooltip.style.top).toBe('calc(72px)')
    expect(tooltip.style.left).toBe('70px')

    wrapper.unmount()
  })
})

// An interactive panel changes height through a ResizeObserver notification after
// keyboard entry. Placement is recalculated, reverse Tab returns to the trigger, and
// unmount disconnects the observer exactly once.
it('repositions interactive content and supports keyboard return to its trigger', async () => {
  let notify: ResizeObserverCallback | undefined
  const disconnect = vi.fn()
  vi.spyOn(globalThis, 'ResizeObserver').mockImplementation(function (callback) {
    notify = callback
    return { observe: vi.fn(), unobserve: vi.fn(), disconnect }
  })
  const wrapper = mount(HelpTooltip, {
    attachTo: document.body,
    props: {
      trigger: 'both',
      interactive: true,
      accessibleLabel: 'Model attributes',
      closable: false,
    },
    slots: { trigger: '<button>Details</button>', default: '<button>All protocols</button>' },
  })
  const panel = document.body.querySelector<HTMLElement>('[role="dialog"]')!
  wrapper.get('.group').element.getBoundingClientRect = vi.fn(
    () => ({ left: 200, right: 220, top: 300, bottom: 320, width: 20, height: 20 }) as DOMRect,
  )
  let height = 40
  panel.getBoundingClientRect = vi.fn(() => ({ width: 288, height }) as DOMRect)
  await wrapper.get('.group').trigger('keydown', { key: 'ArrowDown' })
  await nextTick()
  expect(document.activeElement).toBe(panel.querySelector('button'))
  expect(panel.style.top).toBe('calc(292px)')
  height = 400
  notify!([], {} as ResizeObserver)
  await nextTick()
  expect(panel.style.top).not.toBe('calc(292px)')
  panel
    .querySelector('button')!
    .dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', shiftKey: true, bubbles: true }))
  expect(document.activeElement).toBe(wrapper.get('button').element)
  wrapper.unmount()
  expect(disconnect).toHaveBeenCalledOnce()
  vi.restoreAllMocks()
  document.body.innerHTML = ''
})
