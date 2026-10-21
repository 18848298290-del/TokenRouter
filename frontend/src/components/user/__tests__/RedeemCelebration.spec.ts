import { afterEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { mount, type VueWrapper } from '@vue/test-utils'
import RedeemCelebration from '../RedeemCelebration.vue'
import { finishMotion, mockMotionEnvironment } from '@/__tests__/helpers/motion'

// RedeemCelebration.spec.ts checks redemption feedback lifetime with controlled motion
// events and media-query preferences. The suite distinguishes retained message content from
// decoration, verifies sequence-aware exit cleanup, and checks disposal of the feedback
// timer and reduced-motion listener.

// wrapper tracks the mounted celebration so shared teardown disposes its timer and listeners
// even when a case ends before feedback disappears.
let wrapper: VueWrapper | undefined

// mountCelebration installs the requested motion environment and opens one success sequence.
// The retained wrapper is disposed by afterEach, and callers can drive media-query changes
// or real exit completion through the returned environment.
async function mountCelebration(reduced = false, realTransition = false) {
  const environment = mockMotionEnvironment(reduced)
  wrapper = mount(RedeemCelebration, {
    props: { sequence: 0, title: '兑换成功！', detail: '余额已到账 +$12.50' },
    slots: { default: '<dl><dt>当前余额</dt><dd>$22.50</dd></dl>' },
    global: { stubs: { Icon: true, transition: !realTransition } },
  })
  await wrapper.setProps({ sequence: 1 })
  return environment
}

afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
  vi.useRealTimers()
})

describe('RedeemCelebration', () => {
  // The success overlay begins leaving while summary statistics remain underneath it. The
  // test protects reserved space and restores accessible statistics only after exit motion
  // completes.
  it('保留统计占位，退出动画完成后才恢复显示', async () => {
    await mountCelebration(false, true)
    const statistics = wrapper!.get('dl').element.parentElement!
    expect(statistics.classList.contains('invisible')).toBe(true)
    expect(statistics.getAttribute('aria-hidden')).toBe('true')
    const feedback = wrapper!.get('[data-testid="redeem-celebration"]').element
    await wrapper!.setProps({ sequence: 0 })
    expect(statistics.classList.contains('invisible')).toBe(true)
    await finishMotion(feedback)
    expect(wrapper!.find('[data-testid="redeem-celebration"]').exists()).toBe(false)
    expect(statistics.classList.contains('invisible')).toBe(false)
    expect(statistics.hasAttribute('aria-hidden')).toBe(false)
  })

  // Confetti finishes while the success message remains visible and props later change. The
  // test protects captured message content and prevents a text-only update from replaying
  // decoration.
  it('粒子动画完成时移除装饰，保留成功文案且不因文案变化重播', async () => {
    await mountCelebration()
    expect(wrapper!.findAll('.redeem-confetti-piece')).toHaveLength(16)
    await wrapper!.get('.redeem-confetti-piece').trigger('animationend')
    expect(wrapper!.find('.redeem-confetti').exists()).toBe(true)
    await wrapper!.get('.redeem-confetti').trigger('animationend')
    expect(wrapper!.find('.redeem-confetti').exists()).toBe(false)
    expect(wrapper!.get('[role="status"]').text()).toContain('余额已到账 +$12.50')
    await wrapper!.setProps({ detail: '其他内容' })
    expect(wrapper!.find('.redeem-confetti').exists()).toBe(false)
    expect(wrapper!.get('[role="status"]').text()).toContain('余额已到账 +$12.50')
  })

  // An older success starts leaving before a newer redemption result appears. The test
  // protects the new message and retained statistics placeholder when the old exit event
  // completes.
  it('旧反馈的退出完成事件不能清除新一次成功', async () => {
    await mountCelebration(false, true)
    const previous = wrapper!.get('[data-testid="redeem-celebration"]').element
    await wrapper!.setProps({ sequence: 0 })
    await wrapper!.setProps({ sequence: 2, detail: '并发数 +3' })
    await finishMotion(previous)
    expect(wrapper!.get('[data-testid="redeem-celebration"]').text()).toContain('并发数 +3')
    expect(wrapper!.get('dl').element.parentElement!.classList.contains('invisible')).toBe(true)
  })

  // Reduced-motion preference is active when redemption feedback appears. The test protects
  // static success content and the same three-second reading period used by animated
  // feedback.
  it('减少动态效果时展示静态信息，保持相同阅读时间', async () => {
    vi.useFakeTimers()
    await mountCelebration(true)
    expect(wrapper!.find('.redeem-confetti').exists()).toBe(false)
    expect(wrapper!.find('.redeem-success-pop').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(2999)
    expect(wrapper!.find('[data-testid="redeem-celebration"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(1)
    expect(wrapper!.find('[data-testid="redeem-celebration"]').exists()).toBe(false)
  })

  // Reduced motion becomes enabled during an active celebration and is later disabled again.
  // The test protects immediate decoration removal while retaining success text and avoiding
  // a replay.
  it('播放期间开启减少动态效果立即停止，关闭该偏好也不重播', async () => {
    const environment = await mountCelebration()
    environment.reduce(true)
    await nextTick()
    expect(wrapper!.find('.redeem-confetti').exists()).toBe(false)
    expect(wrapper!.find('.redeem-success-pop').exists()).toBe(false)
    expect(wrapper!.get('[role="status"]').text()).toContain('兑换成功！')
    environment.reduce(false)
    await nextTick()
    expect(wrapper!.find('.redeem-confetti').exists()).toBe(false)
  })

  // The feedback component unmounts with its hold timer and media-query listener active. The
  // test protects release of both resources from the disposed instance.
  it('卸载时清理反馈定时器和媒体查询监听', async () => {
    vi.useFakeTimers()
    const environment = await mountCelebration()
    expect(vi.getTimerCount()).toBe(1)
    wrapper!.unmount()
    wrapper = undefined
    expect(vi.getTimerCount()).toBe(0)
    expect(environment.listeners.size).toBe(0)
  })
})
