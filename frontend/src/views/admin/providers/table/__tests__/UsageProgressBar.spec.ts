import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UsageProgressBar from '../UsageProgressBar.vue'

// UsageProgressBar.spec.ts checks usage bar reset labels, capacity coloring,
// and label layout. A fixed clock separates idle windows from expired
// observations, while exact threshold and class assertions verify utilization
// warnings and remaining-capacity presentation.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

describe('UsageProgressBar', () => {
  beforeEach(() => {
    // Freeze the clock before mounting bars so future and expired reset
    // fixtures use the same reference time across every countdown assertion.
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-03-17T00:00:00Z'))
  })

  afterEach(() => {
    // Restore real timers after each case so the fixed countdown clock does
    // not determine reset labels in unrelated component tests.
    vi.useRealTimers()
  })

  // An idle window has a future reset timestamp and showNowWhenIdle enabled.
  // Its label reports reset-now instead of displaying the remaining
  // two-hour-thirty-minute countdown.
  it('showNowWhenIdle=true 且利用率为 0 时显示“现在”', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '5h',
        utilization: 0,
        resetsAt: '2026-03-17T02:30:00Z',
        showNowWhenIdle: true,
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('usage.resetNow')
    expect(wrapper.text()).not.toContain('2h 30m')
  })

  // A nonzero utilization window enables showNowWhenIdle with a future reset.
  // The label retains the countdown and shows neither the idle nor
  // pending-refresh message.
  it('showNowWhenIdle=true 但利用率大于 0 时显示倒计时', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '7d',
        utilization: 12,
        resetsAt: '2026-03-17T02:30:00Z',
        showNowWhenIdle: true,
        color: 'emerald',
      },
    })

    expect(wrapper.text()).toContain('2h 30m')
    expect(wrapper.text()).not.toContain('usage.resetNow')
    expect(wrapper.text()).not.toContain('usage.resetPending')
  })

  // An idle window disables showNowWhenIdle while its reset remains in the
  // future. The bar displays the countdown instead of treating zero usage as
  // reset-now.
  it('showNowWhenIdle=false 时保持原有倒计时行为', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '1d',
        utilization: 0,
        resetsAt: '2026-03-17T02:30:00Z',
        showNowWhenIdle: false,
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('2h 30m')
    expect(wrapper.text()).not.toContain('usage.resetNow')
  })

  // A window retains positive utilization after its reset timestamp has
  // passed. The bar reports pending refresh rather than claiming its usage has
  // already reset.
  it('resetsAt 已过期且利用率大于 0 时显示「待刷新」', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '5h',
        utilization: 53,
        // This timestamp predates the fixed clock, separating expired
        // observations from future reset countdowns.
        resetsAt: '2026-03-16T22:00:00Z',
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('usage.resetPending')
    expect(wrapper.text()).not.toContain('usage.resetNow')
  })

  // A window has zero utilization and an expired reset timestamp. Its label
  // remains reset-now and does not request a refresh for an already idle
  // window.
  it('resetsAt 已过期且利用率为 0 时仍显示「现在」', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '5h',
        utilization: 0,
        resetsAt: '2026-03-16T22:00:00Z',
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('usage.resetNow')
    expect(wrapper.text()).not.toContain('usage.resetPending')
  })

  // Remaining-capacity mode receives a full one-hundred-percent value. The bar
  // fills its entire width and uses green coloring to represent available
  // capacity rather than exhaustion.
  it('剩余容量模式在 100% 时显示满格绿色', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: 'Req',
        utilization: 100,
        remainingCapacity: true,
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('100%')
    expect(wrapper.get('.h-1\\.5 > div').attributes('style')).toContain('width: 100%')
    expect(wrapper.get('.h-1\\.5 > div').classes()).toContain('bg-green-500')
  })

  // Remaining-capacity mode moves from fifteen percent to zero. The bar
  // shrinks with the remaining amount and keeps red coloring at both
  // low-capacity values.
  it('剩余容量模式在低量和耗尽时缩短并变红', async () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: 'Req',
        utilization: 15,
        remainingCapacity: true,
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('15%')
    expect(wrapper.get('.h-1\\.5 > div').attributes('style')).toContain('width: 15%')
    expect(wrapper.get('.h-1\\.5 > div').classes()).toContain('bg-red-500')

    await wrapper.setProps({ utilization: 0 })

    expect(wrapper.text()).toContain('0%')
    expect(wrapper.get('.h-1\\.5 > div').attributes('style')).toContain('width: 0%')
    expect(wrapper.get('.h-1\\.5 > div').classes()).toContain('bg-red-500')
  })

  // Ordinary utilization exceeds its limit at one hundred twenty percent. The
  // text retains the excess value while the red bar width clamps to one
  // hundred percent.
  it('默认利用率模式仍把超限显示为满格红色', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: '5h',
        utilization: 120,
        color: 'indigo',
      },
    })

    expect(wrapper.text()).toContain('120%')
    expect(wrapper.get('.h-1\\.5 > div').attributes('style')).toContain('width: 100%')
    expect(wrapper.get('.h-1\\.5 > div').classes()).toContain('bg-red-500')
  })

  // The Credits label enables wide-label mode beside a progress bar. The label
  // reserves forty-eight pixels and prevents wrapping rather than using the
  // narrower standard width.
  it('宽标签模式为 Credits 预留固定空间，避免与进度条重叠', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: 'Credits',
        utilization: 0,
        wideLabel: true,
        color: 'indigo',
      },
    })

    const label = wrapper.get('span')
    expect(label.classes()).toContain('w-[48px]')
    expect(label.classes()).toContain('whitespace-nowrap')
    expect(label.classes()).not.toContain('w-[32px]')
  })

  // Utilization values straddle the seventy-five and ninety percent warning
  // thresholds. Bar and percentage-text colors change at those boundaries
  // while values just below retain their preceding levels.
  it('默认利用率模式按 75/90 阈值提前预警分级', () => {
    // mountAt renders a bar at the supplied utilization with the common five-hour
    // label and indigo label color. Callers inspect its bar and text classes to
    // compare warning thresholds.
    const mountAt = (utilization: number) =>
      mount(UsageProgressBar, {
        props: { label: '5h', utilization, color: 'indigo' },
      })

    // Adjacent percentages bracket both warning cutoffs, distinguishing
    // inclusive threshold comparisons from values immediately below each
    // boundary.
    expect(mountAt(74).get('.h-1\\.5 > div').classes()).toContain('bg-green-500')
    expect(mountAt(75).get('.h-1\\.5 > div').classes()).toContain('bg-amber-500')
    expect(mountAt(89).get('.h-1\\.5 > div').classes()).toContain('bg-amber-500')
    expect(mountAt(90).get('.h-1\\.5 > div').classes()).toContain('bg-red-500')

    expect(mountAt(74).get('.h-1\\.5 + span').classes()).toContain('text-gray-600')
    expect(mountAt(75).get('.h-1\\.5 + span').classes()).toContain('text-amber-600')
    expect(mountAt(89).get('.h-1\\.5 + span').classes()).toContain('text-amber-600')
    expect(mountAt(90).get('.h-1\\.5 + span').classes()).toContain('text-red-600')
  })

  // The default label-width mode renders a short window label. The label stays
  // fixed-width and centered while the percentage column remains fixed-width
  // and right-aligned.
  it('labelWidth 默认 fixed：标签保持定宽居中，百分比列不变', () => {
    const wrapper = mount(UsageProgressBar, {
      props: { label: '5h', utilization: 30, color: 'indigo' },
    })

    const label = wrapper.get('.gap-1 > span')
    expect(label.classes()).toContain('w-[32px]')
    expect(label.classes()).toContain('text-center')
    expect(label.classes()).not.toContain('max-w-[72px]')

    const percent = wrapper.get('.h-1\\.5 + span')
    expect(percent.classes()).toContain('w-[32px]')
    expect(percent.classes()).toContain('text-right')
  })

  // Automatic label width receives a longer plan label. The label becomes
  // bounded, truncated, and left-aligned while the percentage column keeps its
  // fixed right-aligned layout.
  it('labelWidth=auto 时标签限宽截断左对齐，百分比列保持不变', () => {
    const wrapper = mount(UsageProgressBar, {
      props: {
        label: 'Pro/7 天',
        utilization: 30,
        color: 'purple',
        labelWidth: 'auto',
      },
    })

    const label = wrapper.get('.gap-1 > span')
    expect(label.text()).toBe('Pro/7 天')
    expect(label.classes()).toContain('max-w-[72px]')
    expect(label.classes()).toContain('truncate')
    expect(label.classes()).toContain('text-left')
    expect(label.classes()).not.toContain('w-[32px]')
    expect(label.classes()).not.toContain('text-center')

    const percent = wrapper.get('.h-1\\.5 + span')
    expect(percent.classes()).toContain('w-[32px]')
    expect(percent.classes()).toContain('text-right')
  })
})
