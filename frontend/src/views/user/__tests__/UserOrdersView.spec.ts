import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import type { PaymentOrder } from '@/api/payment.types'
import UserOrdersView from '../UserOrdersView.vue'

// UserOrdersView.spec.ts checks buyer refund eligibility and the amounts shown
// when a refund is requested. API fixtures provide different purchase kinds,
// states, and provider eligibility while mounted action slots exercise the page's
// actual selection, confirmation, and balance-display behavior.

enableAutoUnmount(afterEach)

// API replacements keep order loading and refund submission observable without
// contacting a payment provider or changing an account's stored balance.
const { getMyOrders, getRefundEligibleProviders, requestRefund } = vi.hoisted(() => ({
  getMyOrders: vi.fn(),
  getRefundEligibleProviders: vi.fn(),
  requestRefund: vi.fn(),
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: { getMyOrders, getRefundEligibleProviders, requestRefund },
}))
vi.mock('@/stores', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))
vi.mock('vue-router', () => ({ useRouter: () => ({ push: vi.fn() }) }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (amount: number) => `Credits ${amount.toFixed(2)}`,
  }),
}))

// orderFixture returns a completed balance order with an eligible provider.
// Overrides independently replace purchase kind, state, provider identity, or
// recorded monetary units for each mounted scenario while keeping the remaining defaults.
function orderFixture(overrides: Partial<PaymentOrder> = {}): PaymentOrder {
  return {
    id: 42,
    user_id: 7,
    amount: 100,
    pay_amount: 20,
    currency: 'CNY',
    fee_rate: 0,
    fee_fixed: 0,
    fee_rate_amount: 0,
    fee_amount: 0,
    payment_type: 'alipay',
    out_trade_no: 'order-42',
    status: 'COMPLETED',
    order_type: 'balance',
    provider_instance_id: 'eligible',
    created_at: '2026-10-22T00:00:00Z',
    expires_at: '2026-10-22T00:30:00Z',
    refund_amount: 0,
    ...overrides,
  }
}

// mountView exposes the production page's action and dialog slots. Child stubs
// preserve row identities and confirmation controls while excluding unrelated layout behavior.
function mountView() {
  return mount(UserOrdersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        OrderTable: {
          props: ['orders'],
          template:
            '<div><div v-for="row in orders" :key="row.id"><slot name="actions" :row="row" /></div></div>',
        },
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show" data-testid="dialog"><slot /><slot name="footer" /></div>',
        },
        Pagination: true,
        Select: true,
        Icon: true,
      },
    },
  })
}

describe('UserOrdersView refund actions', () => {
  beforeEach(() => {
    getMyOrders.mockReset().mockResolvedValue({ data: { items: [orderFixture()], total: 1 } })
    getRefundEligibleProviders
      .mockReset()
      .mockResolvedValue({ data: { provider_instance_ids: ['eligible'] } })
    requestRefund.mockReset().mockResolvedValue({ data: {} })
  })

  // Completed subscriptions share refundable providers with balance orders but cannot use
  // buyer refunds. State and provider checks must continue to restrict the remaining rows.
  it.each([
    { name: 'completed balance', overrides: {}, allowed: true },
    {
      name: 'completed subscription',
      overrides: { order_type: 'subscription' as const },
      allowed: false,
    },
    { name: 'pending balance', overrides: { status: 'PENDING' as const }, allowed: false },
    {
      name: 'ineligible provider',
      overrides: { provider_instance_id: 'disabled' },
      allowed: false,
    },
    { name: 'missing provider', overrides: { provider_instance_id: undefined }, allowed: false },
  ])('matches backend refund eligibility for $name', async ({ overrides, allowed }) => {
    getMyOrders.mockResolvedValue({ data: { items: [orderFixture(overrides)], total: 1 } })
    const wrapper = mountView()
    await flushPromises()
    const actions = wrapper
      .findAll('button')
      .filter((button) => button.text() === 'payment.orders.requestRefund')
    expect(actions).toHaveLength(allowed ? 1 : 0)
    expect(requestRefund).not.toHaveBeenCalled()
  })

  // An eligible balance order has credited units that differ from its payment amount.
  // Opening and submitting the dialog must display those account units and send the selected order.
  it('renders the account unit and submits a trimmed refund reason', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper
      .findAll('button')
      .find((button) => button.text() === 'payment.orders.requestRefund')!
      .trigger('click')
    const dialog = wrapper.get('[data-testid="dialog"]')
    expect(dialog.text()).toContain('Credits 100.00')
    expect(dialog.text()).not.toContain('¥100.00')
    await dialog.get('textarea').setValue('  Duplicate purchase  ')
    await dialog
      .findAll('button')
      .find((button) => button.text() === 'payment.orders.requestRefund')!
      .trigger('click')
    await flushPromises()
    expect(requestRefund).toHaveBeenCalledWith(42, { reason: 'Duplicate purchase' })
    expect(wrapper.find('[data-testid="dialog"]').exists()).toBe(false)
  })

  // Monetary formatting also accepts subscription records from existing callers. A saved
  // price currency must win over payment currency, while legacy responses retain their fallback.
  it('formats subscription face amounts with their recorded price currency', async () => {
    const wrapper = mountView()
    await flushPromises()
    const page = wrapper.vm as unknown as {
      // formatOrderAmount is the production formatter exposed by the mounted setup state.
      formatOrderAmount: (amount: number, order: PaymentOrder) => string
    }
    const order = orderFixture({ order_type: 'subscription', amount: 10, amount_currency: 'USD' })
    expect(page.formatOrderAmount(10, order)).toBe('$10.00')
    expect(page.formatOrderAmount(10, { ...order, amount_currency: undefined })).toBe('¥10.00')
  })
})
