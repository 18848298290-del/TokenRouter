import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'

import { formatDateTimeToMinute } from '@/utils/format'
import SubscriptionProgressMini from '../SubscriptionProgressMini.vue'

/**
 * SubscriptionProgressMini.spec.ts checks subscription expiration and the header
 * status variant using controlled API responses. Cases cover empty and nearly
 * exhausted subscriptions, retaining balance slot content, and opening details with
 * the shared local minute precision date formatter.
 */

/**
 * mockGetActiveSubscriptions controls the active subscription response returned
 * through the API substitute for each mounted header scenario.
 */
const mockGetActiveSubscriptions = vi.fn()

vi.mock('@/api/subscriptions', () => ({
  default: {
    getActiveSubscriptions: (...args: unknown[]) => mockGetActiveSubscriptions(...args),
  },
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number) => value.toFixed(2),
  }),
}))

/**
 * createTestI18n supplies English subscription messages with deterministic argument
 * interpolation.
 */
function createTestI18n() {
  return createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        subscriptionProgress: {
          title: () => 'My Subscriptions',
          viewDetails: () => 'View subscription details',
          activeCount: ({
            named,
          }: {
            named: (key: string) => unknown /* translated argument lookup */
          }) => `${named('count')} active subscription(s)`,
          daily: () => 'Daily',
          weekly: () => 'Weekly',
          monthly: () => 'Monthly',
          expiresAt: ({
            named,
          }: {
            named: (key: string) => unknown /* translated argument lookup */
          }) => `Expires ${named('time')}`,
          viewAll: () => 'View all subscriptions',
          unlimited: () => 'Unlimited',
        },
      },
    },
  })
}

describe('SubscriptionProgressMini', () => {
  beforeEach(() => {
    mockGetActiveSubscriptions.mockReset()
  })

  /**
   * A subscription has a local expiration timestamp including seconds. Opening the
   * popover displays the shared minute precision formatter result instead of a
   * relative tomorrow label.
   */
  it('在订阅弹层展示精确到分钟的本地到期时间', async () => {
    /**
     * The fixture omits a timezone suffix so the shared date formatter interprets
     * the timestamp in the same local calendar used by the assertion.
     */
    const expiresAt = '2026-08-08T10:33:45'
    mockGetActiveSubscriptions.mockResolvedValue([
      {
        id: 1,
        user_id: 7,
        plan_id: 101,
        starts_at: '2026-07-09T10:33:45',
        expires_at: expiresAt,
        status: 'active',
        daily_limit_usd: 200,
        weekly_limit_usd: 1000,
        monthly_limit_usd: 1000,
        daily_usage_usd: 200,
        weekly_usage_usd: 548.99,
        monthly_usage_usd: 860.98,
        daily_window_start: '2026-08-07T00:00:00',
        weekly_window_start: '2026-08-03T00:00:00',
        monthly_window_start: '2026-07-09T00:00:00',
        created_at: '2026-07-09T10:33:45',
        updated_at: '2026-08-08T00:16:41',
        plan: { name: 'Lite+' },
      },
    ])

    const wrapper = mount(SubscriptionProgressMini, {
      global: {
        plugins: [createPinia(), createTestI18n()],
        stubs: {
          Icon: { template: '<span />' },
          RouterLink: { template: '<a><slot /></a>' },
        },
      },
    })
    await flushPromises()
    await wrapper.get('button').trigger('click')

    expect(wrapper.text()).toContain(`Expires ${formatDateTimeToMinute(expiresAt)}`)
    expect(wrapper.text()).not.toContain('Expires tomorrow')
    wrapper.unmount()
  })

  /**
   * The status variant loads an empty active subscription list. Its balance slot
   * remains visible, the trigger is disabled, and no usage status dot appears.
   */
  it('状态变体没有订阅时仍渲染插槽，但不显示状态点也不可展开', async () => {
    mockGetActiveSubscriptions.mockResolvedValue([])

    const wrapper = mountStatusVariant()
    await flushPromises()

    const button = wrapper.get('button')
    expect(button.text()).toContain('8,215.03')
    expect(button.attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="subscription-status-dot"]').exists()).toBe(false)
    wrapper.unmount()
  })

  /**
   * The status variant loads a subscription near its daily quota. The leading dot uses
   * the high usage warning color and the trigger opens subscription details.
   */
  it('状态变体有订阅时按最高用量显示状态点并可展开详情', async () => {
    mockGetActiveSubscriptions.mockResolvedValue([
      {
        id: 1,
        user_id: 7,
        plan_id: 101,
        expires_at: '2026-08-08T10:33:45',
        status: 'active',
        daily_limit_usd: 100,
        daily_usage_usd: 95,
        plan: { name: 'Lite+' },
      },
    ])

    const wrapper = mountStatusVariant()
    await flushPromises()

    const dot = wrapper.get('[data-testid="subscription-status-dot"]')
    expect(dot.classes()).toContain('bg-red-500')
    await wrapper.get('button').trigger('click')
    expect(wrapper.text()).toContain('My Subscriptions')
    wrapper.unmount()
  })
})

/**
 * mountStatusVariant mounts the header status presentation with balance text in its
 * slot. A fresh Pinia instance and the test locale isolate subscription state, and
 * callers unmount the returned wrapper after assertions.
 */
function mountStatusVariant() {
  return mount(SubscriptionProgressMini, {
    props: { variant: 'status' },
    slots: { default: '<span>8,215.03</span>' },
    global: {
      plugins: [createPinia(), createTestI18n()],
      stubs: {
        Icon: { template: '<span />' },
        RouterLink: { template: '<a><slot /></a>' },
      },
    },
  })
}
