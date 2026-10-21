import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import OrderTable from '@/components/payment/OrderTable.vue'

/**
 * OrderTable.spec.ts checks account-unit and gateway-currency rendering in payment order rows.
 * Mounted table slots use supplied order records and formatter doubles so credited quantities
 * remain distinguishable from the amount collected by a gateway.
 */

// The balance formatter double records credited-quantity rendering so table assertions can distinguish account units from gateway currency amounts.
const formatBalanceAmountMock = vi.hoisted(() => vi.fn())

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (...args: any[]) => formatBalanceAmountMock(...args),
  }),
}))

const baseOrder = {
  id: 42,
  user_id: 7,
  amount: 65,
  pay_amount: 12.92,
  fee_rate: 2.2,
  fee_fixed: 0,
  fee_rate_amount: 0,
  fee_amount: 0,
  payment_type: 'alipay',
  out_trade_no: 'sub2_42',
  status: 'COMPLETED' as const,
  order_type: 'balance' as const,
  created_at: '2026-05-11T12:00:00Z',
  expires_at: '2026-05-11T12:30:00Z',
  refund_amount: 0,
}

describe('OrderTable', () => {
  beforeEach(() => {
    formatBalanceAmountMock.mockReset()
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    })
  })

  // A balance order uses a custom account unit in its credited amount. The table must invoke the
  // shared balance formatter so credited quantity is distinct from gateway currency.
  it('uses configured balance unit for credited balance orders', () => {
    // A custom account unit distinguishes credited balance from gateway currency and proves
    // the row uses the configured balance formatter.
    formatBalanceAmountMock.mockReturnValue('点数65.00')

    const wrapper = mount(OrderTable, {
      props: {
        orders: [baseOrder],
        loading: false,
      },
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    expect(formatBalanceAmountMock).toHaveBeenCalledWith(65, { fractionDigits: 2 })
    expect(wrapper.text()).toContain('payment.orders.creditedAmount: 点数65.00')
    expect(wrapper.text()).not.toContain('$65.00')
  })

  // A subscription order stores a gateway currency different from the default. The table must
  // display both the charge and purchase amount using the currency on that order.
  it('uses order currency for pay amount and subscription amount', () => {
    const wrapper = mount(OrderTable, {
      props: {
        orders: [
          {
            ...baseOrder,
            order_type: 'subscription' as const,
            amount: 100,
            pay_amount: 108,
            currency: 'USD',
          },
        ],
        loading: false,
      },
      global: {
        stubs: {
          OrderStatusBadge: true,
        },
      },
    })

    expect(wrapper.text()).toContain('$108.00')
    expect(wrapper.text()).toContain('payment.orders.amount: $100.00')
    expect(wrapper.text()).not.toContain('¥108.00')
  })
  // A converted subscription uses different units for its face price and collected money.
  // Both remain visible even when their numeric values happen to be equal.
  it.each([70, 10])('separates USD subscription prices from CNY payments of %s', (paid) => {
    const wrapper = mount(OrderTable, {
      props: {
        orders: [
          {
            ...baseOrder,
            order_type: 'subscription',
            amount: 10,
            amount_currency: 'USD',
            pay_amount: paid,
            currency: 'CNY',
          },
        ],
        loading: false,
      },
      global: { stubs: { OrderStatusBadge: true } },
    })
    expect(wrapper.text()).toContain('payment.orders.amount: $10.00')
    expect(wrapper.text()).toContain(`¥${paid.toFixed(2)}`)
    expect(wrapper.text()).not.toContain('payment.orders.creditedAmount')
  })
})
