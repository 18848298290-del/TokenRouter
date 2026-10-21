import { defineComponent, h, nextTick, ref } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useIconAnimation } from '../useIconAnimation'
import type { IconDefinition } from '../types'

// useIconAnimation.spec.ts tests interaction ownership and cancellation with
// controllable motion promises. The host fixture supplies buttons, labels,
// custom triggers, or standalone SVG nodes so pointer, focus, suppression,
// programmatic activation, and unmount can be exercised through DOM events.

// controls replaces the motion controller so tests can inspect targets and
// pause individual animation steps.
const controls = vi.hoisted(() => ({
  start: vi.fn().mockResolvedValue(undefined), // observable playback request
  set: vi.fn(), // immediate target application
  stop: vi.fn(), // current playback cancellation
}))
vi.mock('motion-v', () => ({ useAnimationControls: () => controls }))

// definition supplies two ordered targets so cancellation assertions can
// distinguish the initial step from an incorrectly resumed successor.
const definition: IconDefinition = {
  name: 'test',
  normal: 'normal',
  animate: ['first', 'second'],
  render: () => null,
}
// media and change emulate the system motion preference and its subscription;
// wrapper retains the mounted host for cleanup after every test.
let media: MediaQueryList
let change: (() => void) | undefined
let wrapper: VueWrapper

// host mounts an icon lifecycle inside the requested interaction container and
// attaches it to the document. Tests retain the returned wrapper for prop
// updates and unmount it during cleanup to release listeners.
function host(
  options: {
    standalone?: boolean // render SVG alone
    marked?: boolean // use marked div
    disabled?: boolean // initial disabled state
    label?: boolean // wrap associated input
  } = {},
) {
  return mount(
    defineComponent({
      props: {
        enabled: { default: true }, // hover feedback switch
        animationActive: { default: false }, // programmatic playback request
        disabled: { default: false }, // control disabled state
        spinning: { default: false }, // external loading rotation
      },
      // setup connects a test SVG to useIconAnimation and renders the requested
      // host. The SVG ref becomes available at mount, allowing the composable
      // to find its trigger and register listeners.
      setup(props) {
        const svg = ref<SVGSVGElement | null>(null)
        useIconAnimation(
          svg,
          () => definition,
          () => props.enabled,
          () => props.animationActive,
        )
        return () => {
          const icon = h('svg', {
            ref: svg,
            class: props.spinning ? 'animate-spin' : '',
          })
          if (options.label) {
            return h('label', [h('input', { disabled: props.disabled }), icon])
          }
          return options.standalone
            ? icon
            : h(
                options.marked ? 'div' : 'button',
                {
                  disabled: props.disabled,
                  'data-icon-trigger': options.marked ? '' : undefined,
                },
                [icon, h('span', '操作')],
              )
        }
      },
    }),
    { props: { disabled: options.disabled }, attachTo: document.body },
  )
}

beforeEach(() => {
  controls.start.mockReset().mockResolvedValue(undefined)
  controls.set.mockClear()
  controls.stop.mockClear()
  change = undefined
  media = {
    matches: false,
    addEventListener: vi.fn((_event, listener) => {
      change = listener
    }),
    removeEventListener: vi.fn(),
  } as unknown as MediaQueryList
  vi.spyOn(window, 'matchMedia').mockReturnValue(media)
})
afterEach(() => {
  // Unmount first to release lifecycle listeners before clearing the document
  // and restoring shared browser mocks for the next case.
  wrapper?.unmount()
  document.body.innerHTML = ''
  vi.restoreAllMocks()
})

describe('图标交互', () => {
  // Programmatic activity is toggled while hover handling is disabled. It must
  // play one sequence per activation, ignore additional pointer entry, reset
  // when cleared, and allow a later replay.
  it('业务状态独立于悬停触发一次，复位后可重播', async () => {
    wrapper = host()
    await wrapper.setProps({ enabled: false })
    await wrapper.trigger('pointerenter')
    expect(controls.start).not.toHaveBeenCalled()
    await wrapper.setProps({ animationActive: true })
    await nextTick()
    expect(controls.start.mock.calls.map(([target]) => target)).toEqual(['first', 'second'])
    await wrapper.trigger('pointerenter')
    expect(controls.start).toHaveBeenCalledTimes(2)
    await wrapper.setProps({ animationActive: false })
    expect(controls.set).toHaveBeenLastCalledWith('normal')
    controls.start.mockClear()
    await wrapper.setProps({ animationActive: true })
    await nextTick()
    expect(controls.start.mock.calls.map(([target]) => target)).toEqual(['first', 'second'])
  })

  // A programmatic request encounters disabled state and reduced motion before
  // its first step is paused. Clearing activity must cancel the successor even
  // when that first promise later resolves.
  it('业务触发仍遵守禁用和减少动态效果，关闭状态会取消旧序列', async () => {
    wrapper = host({ disabled: true })
    await wrapper.setProps({ enabled: false, animationActive: true })
    expect(controls.start).not.toHaveBeenCalled()
    Object.assign(media, { matches: true })
    await wrapper.setProps({ disabled: false })
    expect(controls.start).not.toHaveBeenCalled()
    // Hold the first completion promise so a later state change can invalidate
    // its ownership before the next target is considered.
    let complete: (() => void) | undefined
    controls.start.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          complete = resolve
        }),
    )
    Object.assign(media, { matches: false })
    change?.()
    expect(controls.start).toHaveBeenCalledWith('first')
    await wrapper.setProps({ animationActive: false })
    complete?.()
    await nextTick()
    expect(controls.start).toHaveBeenCalledTimes(1)
    expect(controls.set).toHaveBeenLastCalledWith('normal')
  })

  // A label's associated input becomes disabled while the label is hovered.
  // Attribute observation must apply the resting target immediately to stop
  // decorative playback for that control.
  it('标签关联的表单控件在悬停中禁用时立即复位', async () => {
    wrapper = host({ label: true })
    await wrapper.trigger('pointerenter')
    expect(controls.start).toHaveBeenCalledWith('first')
    await wrapper.setProps({ disabled: true })
    await nextTick()
    expect(controls.set).toHaveBeenLastCalledWith('normal')
  })

  // A newly mounted trigger receives repeated pointer entry before leaving.
  // Mounting must stay idle, entry must run one ordered sequence, and departure
  // must request the resting target.
  it('初次挂载静止，悬停整个控件播放一次，停留不重复', async () => {
    wrapper = host()
    expect(controls.start).not.toHaveBeenCalled()
    await wrapper.trigger('pointerenter')
    await nextTick()
    expect(controls.start.mock.calls.map(([target]) => target)).toEqual(['first', 'second'])
    await wrapper.trigger('pointerenter')
    expect(controls.start).toHaveBeenCalledTimes(2)
    await wrapper.trigger('pointerleave')
    expect(controls.start).toHaveBeenLastCalledWith('normal')
  })

  // Pointer hover and keyboard focus overlap on the same trigger. Leaving with
  // the pointer or moving focus inside the subtree must retain activity until
  // focus leaves the trigger.
  it('聚焦后保持反馈，移出鼠标不覆盖键盘焦点', async () => {
    wrapper = host()
    await wrapper.trigger('pointerenter')
    await wrapper.trigger('focusin')
    await wrapper.trigger('pointerleave')
    expect(controls.start).not.toHaveBeenCalledWith('normal')
    await wrapper.trigger('focusout', {
      relatedTarget: wrapper.find('span').element,
    })
    expect(controls.start).not.toHaveBeenCalledWith('normal')
    await wrapper.trigger('focusout', { relatedTarget: null })
    expect(controls.start).toHaveBeenLastCalledWith('normal')
  })

  // An SVG excluded from sequential navigation receives focus directly. The
  // lifecycle must forward focus to its button and continue allowing normal
  // button blur and refocus afterward.
  it('SVG 获得点击焦点时将焦点交回按钮，键盘焦点继续保留在按钮上', () => {
    wrapper = host()
    const svg = wrapper.find('svg').element
    // Browsers can focus an SVG with negative tabindex after a click, so
    // reproduce direct focus in addition to navigation exclusion.
    svg.setAttribute('tabindex', '-1')
    svg.focus()
    expect(document.activeElement).toBe(wrapper.element)
    expect(controls.start).toHaveBeenCalledWith('first')
    wrapper.element.blur()
    wrapper.element.focus()
    expect(document.activeElement).toBe(wrapper.element)
  })

  // A decorative SVG inside a label receives focus directly. The associated
  // form input must become the active element, keeping the decoration out of
  // the control's focus position.
  it('label 内的 SVG 获得焦点时转交给关联控件', () => {
    wrapper = host({ label: true })
    const svg = wrapper.find('svg').element
    svg.setAttribute('tabindex', '-1')
    svg.focus()
    expect(document.activeElement).toBe(wrapper.find('input').element)
  })

  // Standalone SVGs and triggers unable to accept focus receive direct SVG
  // focus. The lifecycle must clear decorative focus and keep the animation
  // idle for each host configuration.
  it.each([{ standalone: true }, { marked: true }, { label: true, disabled: true }])(
    '外层无法接收焦点时清除 SVG 焦点 %o',
    (options) => {
      wrapper = host(options)
      const svg = wrapper.find('svg').element
      svg.setAttribute('tabindex', '-1')
      svg.focus()
      expect(document.activeElement).toBe(document.body)
      expect(controls.start).not.toHaveBeenCalled()
    },
  )

  // An internal SVG shape gains focus while its custom container is hovered.
  // Clearing that focus must retain hover feedback and click propagation until
  // pointer departure requests a reset.
  it('清除内部图形的点击焦点后，仍能悬停、点击和正常复位', async () => {
    wrapper = host({ marked: true })
    const svg = wrapper.find('svg').element
    const circle = document.createElementNS('http://www.w3.org/2000/svg', 'circle')
    circle.setAttribute('tabindex', '-1')
    svg.append(circle)
    const click = vi.fn()
    wrapper.element.addEventListener('click', click)

    await wrapper.trigger('pointerenter')
    await nextTick()
    circle.focus()
    expect(document.activeElement).toBe(document.body)
    expect(controls.start).not.toHaveBeenCalledWith('normal')

    circle.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(click).toHaveBeenCalledOnce()
    await wrapper.trigger('pointerleave')
    expect(controls.start).toHaveBeenLastCalledWith('normal')
  })

  // A standalone SVG and an explicitly marked container each receive pointer
  // entry. Both host forms must serve as valid triggers that start the
  // artwork's first animation target.
  it.each([{ standalone: true }, { marked: true }])(
    '支持独立图标和显式触发容器 %o',
    async (options) => {
      wrapper = host(options)
      await wrapper.trigger('pointerenter')
      expect(controls.start).toHaveBeenCalledWith('first')
    },
  )

  // A disabled trigger receives pointer entry before becoming enabled and then
  // loading. Playback must begin when allowed, reset during loading, and remain
  // reset when hover feedback is disabled.
  it('禁用、加载和关闭属性都立即停止动画', async () => {
    wrapper = host({ disabled: true })
    // The mount helper skips trigger dispatch on disabled controls; dispatch
    // directly to reproduce pointer entry before the control becomes enabled.
    wrapper.element.dispatchEvent(new Event('pointerenter'))
    await nextTick()
    expect(controls.start).not.toHaveBeenCalled()
    await wrapper.setProps({ disabled: false })
    await nextTick()
    expect(controls.start).toHaveBeenCalledWith('first')
    await wrapper.setProps({ spinning: true })
    await nextTick()
    expect(controls.set).toHaveBeenLastCalledWith('normal')
    await wrapper.setProps({ spinning: false })
    await wrapper.setProps({ enabled: false })
    expect(controls.set).toHaveBeenLastCalledWith('normal')
  })

  // The system motion preference changes during playback and again during a
  // reset. Both transitions to reduced motion must apply the resting target
  // immediately and suppress later pointer entry.
  it('减少动态效果在播放中或复位途中变化都立即生效', async () => {
    wrapper = host()
    await wrapper.trigger('pointerenter')
    Object.assign(media, { matches: true })
    change?.()
    expect(controls.set).toHaveBeenLastCalledWith('normal')
    controls.start.mockClear()
    await wrapper.trigger('pointerleave')
    await wrapper.trigger('pointerenter')
    expect(controls.start).not.toHaveBeenCalled()
    Object.assign(media, { matches: false })
    change?.()
    await wrapper.trigger('pointerleave')
    controls.set.mockClear()
    Object.assign(media, { matches: true })
    change?.()
    expect(controls.set).toHaveBeenCalledWith('normal')
  })

  // The first animation step remains pending when the pointer leaves. Resolving
  // that promise afterward must not start the second target, while departure
  // still requests the resting target.
  it('快速移出后，旧异步序列不能继续播放下一段', async () => {
    // Hold the first completion promise so a later state change can invalidate
    // its ownership before the next target is considered.
    let complete: (() => void) | undefined
    controls.start.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          complete = resolve
        }),
    )
    wrapper = host()
    await wrapper.trigger('pointerenter')
    await wrapper.trigger('pointerleave')
    complete?.()
    await nextTick()
    expect(controls.start.mock.calls.map(([target]) => target)).toEqual(['first', 'normal'])
  })

  // The component unmounts with its first animation promise pending. Later
  // completion and dispatched events must not restart playback, and cleanup
  // must stop controls and remove the preference listener.
  it('卸载后移除事件监听并取消旧序列', async () => {
    // Hold the first completion promise so a later state change can invalidate
    // its ownership before the next target is considered.
    let complete: (() => void) | undefined
    controls.start.mockImplementationOnce(
      () =>
        new Promise<void>((resolve) => {
          complete = resolve
        }),
    )
    wrapper = host()
    const button = wrapper.element
    await wrapper.trigger('pointerenter')
    wrapper.unmount()
    complete?.()
    button.dispatchEvent(new Event('pointerenter'))
    change?.()
    await nextTick()
    expect(controls.start).toHaveBeenCalledTimes(1)
    expect(controls.stop).toHaveBeenCalled()
    expect(media.removeEventListener).toHaveBeenCalledWith('change', expect.any(Function))
  })

  // A trigger receives pointer entry identified as a touch event. The lifecycle
  // must leave the animation controller idle so a tap cannot create persistent
  // hover feedback.
  it('触屏不留下模拟悬停动画', async () => {
    wrapper = host()
    await wrapper.trigger('pointerenter', { pointerType: 'touch' })
    expect(controls.start).not.toHaveBeenCalled()
  })
})
