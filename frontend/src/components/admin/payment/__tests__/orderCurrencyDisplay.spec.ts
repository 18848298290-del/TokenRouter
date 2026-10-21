import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import type { PaymentOrder } from '@/api/payment.types'
import AdminRefundDialog from '../AdminRefundDialog.vue'

// orderCurrencyDisplay.spec.ts checks monetary labels in the administrator refund dialog
// for a partially refunded subscription. The fixture supplies USD order amounts while the
// balance-display replacement uses a different unit, making gateway and entitlement
// displays distinguishable.

// formatBalanceAmountMock replaces balance amount formatting while the rendered order
// amounts exercise the subscription currency formatter in this test.
const formatBalanceAmountMock = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitSymbol: '点',
    formatBalanceAmount: (...args: any[]) => formatBalanceAmountMock(...args),
  }),
}))

// BaseDialogStub renders dialog content and footer slots so tests can exercise the
// contained form controls and emitted actions.
const BaseDialogStub = {
  props: ['show'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
}

// orderFactory creates a payment order fixture with the requested overrides. Defaults
// describe a USD subscription purchase, allowing individual tests to replace status and
// refund totals while retaining its gateway currency.
function orderFactory(overrides: Partial<PaymentOrder> = {}): PaymentOrder {
  return {
    id: 1,
    user_id: 10,
    amount: 100,
    pay_amount: 108,
    currency: 'USD',
    fee_rate: 8,
    fee_fixed: 2,
    fee_rate_amount: 6,
    fee_amount: 8,
    payment_type: 'stripe',
    out_trade_no: 'sub2_202606250001',
    status: 'COMPLETED',
    order_type: 'subscription',
    created_at: '2026-06-25T10:00:00Z',
    expires_at: '2026-06-25T10:30:00Z',
    refund_amount: 25,
    ...overrides,
  }
}

describe('admin order currency display', () => {
  // A subscription order uses a currency distinct from the configured balance display. Its
  // paid, credited, already-refunded, and refundable amounts must render with the USD
  // symbol and expected numeric values.
  it('uses order currency for subscription refund amounts', () => {
    const wrapper = mount(AdminRefundDialog, {
      props: {
        show: true,
        order: orderFactory({
          status: 'PARTIALLY_REFUNDED',
          refund_amount: 20,
        }),
        userBalance: 200,
      },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
        },
      },
    })

    const text = wrapper.text()
    expect(text).toContain('$108.00')
    expect(text).toContain('$100.00')
    expect(text).toContain('$20.00')
    expect(text).toContain('$80.00')
  })
  // Subscription face quantities and collected payment can use different currencies. The
  // dialog must label its face amounts with USD while keeping the channel charge in CNY.
  it('keeps face quantities separate from the payment currency', () => {
    const wrapper = mount(AdminRefundDialog, {
      props: {
        show: true,
        order: orderFactory({
          amount: 10,
          amount_currency: 'USD',
          pay_amount: 70,
          currency: 'CNY',
          refund_amount: 0,
        }),
        userBalance: 200,
      },
      global: { stubs: { BaseDialog: BaseDialogStub } },
    })
    expect(wrapper.text()).toContain('$10.00')
    expect(wrapper.text()).toContain('¥70.00')
    expect(wrapper.text()).not.toContain('¥10.00')
  })
})
