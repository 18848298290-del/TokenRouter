import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createI18n } from 'vue-i18n'
import { createRouter, createMemoryHistory } from 'vue-router'
import SubscriptionsView from '../SubscriptionsView.vue'

// SubscriptionsView.spec.ts checks plan-chain grouping and exhausted-subscription revocation
// through the production page. Real stores and routing surround mocked subscription
// requests, while a controllable confirmation shell verifies eligibility, required
// confirmation, and duplicate-submit prevention.

// Subscription request mocks independently control list retrieval, active-store refresh, and
// the exhausted-subscription mutation used by confirmation scenarios.
const mockGetMySubscriptions = vi.fn()
const mockGetActiveSubscriptions = vi.fn()
const mockRevokeExhaustedSubscription = vi.fn()

vi.mock('@/api/subscriptions', () => ({
  default: {
    getMySubscriptions: (...args: unknown[]) => mockGetMySubscriptions(...args),
    getActiveSubscriptions: (...args: unknown[]) => mockGetActiveSubscriptions(...args),
    revokeExhaustedSubscription: (...args: unknown[]) => mockRevokeExhaustedSubscription(...args),
  },
}))

// createTestI18n supplies the subscription and confirmation labels used by this page
// fixture. Missing runtime compilation can leave keys visible, so assertions accept the
// specific labels exercised by each case.
function createTestI18n() {
  return createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        payment: {
          renewNow: 'Renew now',
        },
        common: {
          today: 'Today',
          tomorrow: 'Tomorrow',
          cancel: 'Cancel',
          confirm: 'Confirm',
          processing: 'Processing...',
        },
        userSubscriptions: {
          noActiveSubscriptions: 'No active subscriptions',
          queuedPacks: 'Queued {count}',
          startsAt: 'Starts At',
          expires: 'Expires',
          currentPackEnds: 'Current pack ends {date}',
          daily: 'Daily',
          weekly: 'Weekly',
          monthly: 'Monthly',
          resetIn: 'Reset in {time}',
          quotaEndsIn: 'Ends in {time}',
          unlimited: 'Unlimited',
          unlimitedDesc: 'Unlimited usage',
          pendingOnly: 'Pending only',
          failedToLoad: 'Failed to load subscriptions',
          daysRemaining: '{days} days remaining',
          windowNotActive: 'Window not active',
          revoke: 'Revoke plan',
          revokeTitle: 'Revoke exhausted plan',
          revokeConfirmWithReplacement: 'Queued pack will start immediately.',
          revokeConfirmWithoutReplacement: 'No queued pack will replace this plan.',
          revokeSuccessWithReplacement: 'Plan revoked and {count} API key(s) rebound.',
          revokeSuccess: 'Plan revoked successfully.',
          revokeFailed: 'Failed to revoke plan.',
          groupAccess: 'Accessible groups',
          allGroups: 'All groups',
          restrictedGroups: 'Restricted groups',
          status: {
            active: 'Active',
            pending: 'Pending',
            expired: 'Expired',
          },
        },
      },
    },
  })
}

// mountView creates isolated stores, locale, and an initialized memory router before
// mounting the production subscription page. The confirmation stub preserves visibility,
// busy state, and emitted confirm or cancel actions.
async function mountView() {
  const pinia = createPinia()
  setActivePinia(pinia)
  const i18n = createTestI18n()
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/', component: { template: '<div />' } }],
  })
  await router.push('/')
  await router.isReady()

  return mount(SubscriptionsView, {
    global: {
      plugins: [pinia, i18n, router],
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: { template: '<span />' },
        ConfirmDialog: {
          props: ['show', 'title', 'message', 'confirmText', 'cancelText', 'danger', 'loading'],
          emits: ['confirm', 'cancel'],
          template: `
            <div v-if="show" data-testid="revoke-dialog">
              <p data-testid="revoke-dialog-message">{{ message }}</p>
              <button data-testid="revoke-dialog-confirm" :disabled="loading" @click="$emit('confirm')">{{ confirmText }}</button>
              <button data-testid="revoke-dialog-cancel" :disabled="loading" @click="$emit('cancel')">{{ cancelText }}</button>
            </div>
          `,
        },
      },
    },
  })
}

describe('SubscriptionsView', () => {
  beforeEach(() => {
    mockGetMySubscriptions.mockReset()
    mockGetActiveSubscriptions.mockReset().mockResolvedValue([])
    mockRevokeExhaustedSubscription.mockReset()
  })

  // Active and pending records share one plan while another plan has its own active record.
  // The test protects one card per plan and the queued-pack indication.
  it('groups same-plan active and pending subscriptions into one chain card', async () => {
    mockGetMySubscriptions.mockResolvedValue([
      {
        id: 1,
        user_id: 7,
        plan_id: 101,
        starts_at: '2026-04-01T00:00:00Z',
        expires_at: '2026-05-01T00:00:00Z',
        status: 'active',
        daily_limit_usd: 10,
        weekly_limit_usd: null,
        monthly_limit_usd: null,
        daily_usage_usd: 3,
        weekly_usage_usd: 0,
        monthly_usage_usd: 0,
        daily_window_start: '2026-04-20T00:00:00Z',
        weekly_window_start: null,
        monthly_window_start: null,
        created_at: '2026-04-01T00:00:00Z',
        updated_at: '2026-04-01T00:00:00Z',
        plan: {
          id: 101,
          name: 'Plan Alpha',
          description: 'Alpha description',
          price: 10,
          features: [],
          validity_days: 30,
          validity_unit: 'day',
          daily_limit_usd: 10,
          weekly_limit_usd: null,
          monthly_limit_usd: null,
          for_sale: true,
          sort_order: 0,
        },
      },
      {
        id: 2,
        user_id: 7,
        plan_id: 101,
        starts_at: '2026-05-01T00:00:00Z',
        expires_at: '2026-05-31T00:00:00Z',
        status: 'pending',
        daily_limit_usd: 10,
        weekly_limit_usd: null,
        monthly_limit_usd: null,
        daily_usage_usd: 0,
        weekly_usage_usd: 0,
        monthly_usage_usd: 0,
        daily_window_start: null,
        weekly_window_start: null,
        monthly_window_start: null,
        created_at: '2026-04-10T00:00:00Z',
        updated_at: '2026-04-10T00:00:00Z',
        plan: {
          id: 101,
          name: 'Plan Alpha',
          description: 'Alpha description',
          price: 10,
          features: [],
          validity_days: 30,
          validity_unit: 'day',
          daily_limit_usd: 10,
          weekly_limit_usd: null,
          monthly_limit_usd: null,
          for_sale: true,
          sort_order: 0,
        },
      },
      {
        id: 3,
        user_id: 7,
        plan_id: 202,
        starts_at: '2026-04-15T00:00:00Z',
        expires_at: '2026-04-25T00:00:00Z',
        status: 'active',
        daily_limit_usd: null,
        weekly_limit_usd: 40,
        monthly_limit_usd: null,
        daily_usage_usd: 0,
        weekly_usage_usd: 8,
        monthly_usage_usd: 0,
        daily_window_start: null,
        weekly_window_start: '2026-04-18T00:00:00Z',
        monthly_window_start: null,
        created_at: '2026-04-15T00:00:00Z',
        updated_at: '2026-04-15T00:00:00Z',
        plan: {
          id: 202,
          name: 'Plan Beta',
          description: 'Beta description',
          price: 20,
          features: [],
          validity_days: 10,
          validity_unit: 'day',
          daily_limit_usd: null,
          weekly_limit_usd: 40,
          monthly_limit_usd: null,
          for_sale: true,
          sort_order: 0,
        },
      },
    ])

    const wrapper = await mountView()
    await flushPromises()

    const text = wrapper.text()
    expect(mockGetMySubscriptions).toHaveBeenCalledTimes(1)
    expect(text.match(/Plan Alpha/g)?.length).toBe(1)
    expect(text.match(/Plan Beta/g)?.length).toBe(1)
    expect(text).toMatch(/Queued 1|userSubscriptions\.queuedPacks/)
  })

  // Lower quota windows are exhausted while the highest configured window still has
  // capacity. The test protects revocation visibility only after that highest window also
  // reaches its limit.
  it('shows revoke only when the highest configured quota is exhausted', async () => {
    const now = Date.now()
    const subscription = {
      id: 9,
      user_id: 7,
      plan_id: 101,
      starts_at: new Date(now - 60 * 60 * 1000).toISOString(),
      expires_at: new Date(now + 24 * 60 * 60 * 1000).toISOString(),
      status: 'active',
      daily_limit_usd: 1,
      daily_usage_usd: 1,
      weekly_limit_usd: 10,
      weekly_usage_usd: 10,
      monthly_limit_usd: 100,
      monthly_usage_usd: 99,
      daily_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
      weekly_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
      monthly_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
      created_at: new Date(now - 60 * 60 * 1000).toISOString(),
      updated_at: new Date(now - 60 * 60 * 1000).toISOString(),
      plan: {
        id: 101,
        name: 'Plan Alpha',
        description: '',
        price: 10,
        features: [],
        validity_days: 30,
        validity_unit: 'day',
        daily_limit_usd: 1,
        weekly_limit_usd: 10,
        monthly_limit_usd: 100,
        for_sale: true,
        sort_order: 0,
      },
    }
    mockGetMySubscriptions.mockResolvedValue([subscription])

    const wrapper = await mountView()
    await flushPromises()
    expect(wrapper.find('[data-testid="revoke-subscription"]').exists()).toBe(false)

    await wrapper.unmount()
    mockGetMySubscriptions.mockResolvedValue([{ ...subscription, monthly_usage_usd: 100 }])
    const exhaustedWrapper = await mountView()
    await flushPromises()
    expect(exhaustedWrapper.find('[data-testid="revoke-subscription"]').exists()).toBe(true)
  })

  // A revocation request remains pending after user confirmation. The test protects the
  // selected subscription identity, busy confirmation controls, and omission of duplicate
  // mutation requests.
  it('requires confirmation and prevents duplicate revoke submissions', async () => {
    const now = Date.now()
    mockGetMySubscriptions.mockResolvedValue([
      {
        id: 12,
        user_id: 7,
        plan_id: 303,
        starts_at: new Date(now - 60 * 60 * 1000).toISOString(),
        expires_at: new Date(now + 24 * 60 * 60 * 1000).toISOString(),
        status: 'active',
        daily_limit_usd: null,
        daily_usage_usd: 0,
        weekly_limit_usd: null,
        weekly_usage_usd: 0,
        monthly_limit_usd: 50,
        monthly_usage_usd: 50,
        daily_window_start: null,
        weekly_window_start: null,
        monthly_window_start: new Date(now - 30 * 60 * 1000).toISOString(),
        created_at: new Date(now - 60 * 60 * 1000).toISOString(),
        updated_at: new Date(now - 60 * 60 * 1000).toISOString(),
        plan: {
          id: 303,
          name: 'Plan Gamma',
          description: '',
          price: 10,
          features: [],
          validity_days: 30,
          validity_unit: 'day',
          daily_limit_usd: null,
          weekly_limit_usd: null,
          monthly_limit_usd: 50,
          for_sale: true,
          sort_order: 0,
        },
      },
    ])
    let resolveRevoke!: (value: {
      revoked_subscription_id: number /* Revoked subscription record identity. */
      replacement_subscription_id:
        | number
        | null /* Replacement record identity; null indicates that the server activated no queued subscription after revocation. */
      rebound_api_key_count: number /* Number of rebound API keys. */
    }) => void
    mockRevokeExhaustedSubscription.mockReturnValue(
      new Promise((resolve) => {
        resolveRevoke = resolve
      }),
    )

    const wrapper = await mountView()
    await flushPromises()
    await wrapper.get('[data-testid="revoke-subscription"]').trigger('click')
    const dialog = wrapper.get('[data-testid="revoke-dialog"]')
    expect(dialog.text()).toContain('userSubscriptions.revokeConfirmWithoutReplacement')

    await wrapper.get('[data-testid="revoke-dialog-confirm"]').trigger('click')
    await wrapper.vm.$nextTick()
    expect(mockRevokeExhaustedSubscription).toHaveBeenCalledTimes(1)
    expect(mockRevokeExhaustedSubscription).toHaveBeenCalledWith(12)
    expect(
      wrapper.get('[data-testid="revoke-dialog-confirm"]').attributes('disabled'),
    ).toBeDefined()

    await wrapper.get('[data-testid="revoke-dialog-confirm"]').trigger('click')
    expect(mockRevokeExhaustedSubscription).toHaveBeenCalledTimes(1)
    resolveRevoke({
      revoked_subscription_id: 12,
      replacement_subscription_id: null,
      rebound_api_key_count: 0,
    })
    await flushPromises()
  })
})
