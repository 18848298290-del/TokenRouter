import { afterEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, h, nextTick, ref } from 'vue'
import { mount } from '@vue/test-utils'

import { mockMotionEnvironment } from '@/__tests__/helpers/motion'
import { useCountUp } from '../useCountUp'

// useCountUp.spec.ts checks animated numeric display through a mounted component and
// controlled animation frames. The cases cover interpolation, reduced-motion preference,
// and unmount cancellation, keeping elapsed-time behavior separate from the browser's real
// rendering schedule.

// mountCounter mounts a component displaying a reactive count with a one-second animation
// duration. It returns the source, display reader, and wrapper; callers unmount the
// wrapper to dispose its lifecycle hooks.
const mountCounter = (initial: number) => {
  const source = ref(initial)
  let display!: ReturnType<typeof useCountUp>
  const wrapper = mount(
    defineComponent({
      // setup connects the test source to the rendered animated counter.
      setup() {
        display = useCountUp(source, 1000)
        return () => h('span', display.value)
      },
    }),
  )
  return { source, display: () => display.value, wrapper }
}

describe('useCountUp', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  // A mounted count starts at ten and receives a target of one hundred ten. Controlled
  // half-duration and final frames must produce the expected eased value and exact target.
  it('初始直接显示当前值，变化后逐帧缓出到新值', async () => {
    mockMotionEnvironment(false)
    let now = 0
    const frames: FrameRequestCallback[] = []
    vi.spyOn(performance, 'now').mockImplementation(() => now)
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation((callback) => {
      frames.push(callback)
      return frames.length
    })

    const counter = mountCounter(10)
    expect(counter.display()).toBe(10)

    counter.source.value = 110
    await nextTick()
    now = 500
    frames.shift()?.(now)

    // Quartic easing has completed ninety-three point seven five percent at half duration,
    // establishing the numeric expectation independently of frame scheduling.
    expect(counter.display()).toBeCloseTo(103.75)

    now = 1000
    frames.shift()?.(now)
    expect(counter.display()).toBe(110)
    expect(frames).toHaveLength(0)
    counter.wrapper.unmount()
  })

  // The motion environment requests reduced motion before the counter is mounted. Updating
  // its source must publish the target after Vue flushes without intermediate
  // interpolation.
  it('系统减少动态效果时直接显示新值', async () => {
    mockMotionEnvironment(true)
    const counter = mountCounter(0)
    counter.source.value = 42
    await nextTick()
    expect(counter.display()).toBe(42)
    counter.wrapper.unmount()
  })

  // A source update schedules a known animation-frame handle before the component
  // unmounts. Teardown must cancel that handle so the unfinished animation cannot remain
  // scheduled.
  it('卸载时取消未完成的动画帧', async () => {
    mockMotionEnvironment(false)
    const cancel = vi.spyOn(window, 'cancelAnimationFrame')
    vi.spyOn(window, 'requestAnimationFrame').mockImplementation(() => 7)
    const counter = mountCounter(0)
    counter.source.value = 5
    await nextTick()
    counter.wrapper.unmount()
    expect(cancel).toHaveBeenCalledWith(7)
  })
})
