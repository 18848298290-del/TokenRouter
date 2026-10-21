import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'

// Hoisted router, order, and Stripe resource doubles let initialization and polling run through the mounted page without loading a remote SDK.
const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
}))
const routerPush = vi.hoisted(() => vi.fn())
const getOrder = vi.hoisted(() => vi.fn())
const paymentStore = vi.hoisted(() => ({
  config: { stripe_publishable_key: 'pk_test' } as {
    stripe_publishable_key?: string // configured Stripe browser key
  },
  fetchConfig: vi.fn(),
  pollOrderStatus: vi.fn(),
}))
const loadStripe = vi.hoisted(() => vi.fn())
const stripeElements = vi.hoisted(() => ({
  create: vi.fn(),
}))
const stripePaymentElement = vi.hoisted(() => ({
  mount: vi.fn(),
  on: vi.fn(),
}))
const stripeInstance = vi.hoisted(() => ({
  elements: vi.fn(),
  confirmPayment: vi.fn(),
  confirmAlipayPayment: vi.fn(),
  confirmWechatPayPayment: vi.fn(),
}))

vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return {
    ...actual,
    useRoute: () => routeState,
    useRouter: () => ({ push: routerPush }),
  }
})

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: { value: 'zh-CN' },
    }),
  }
})

vi.mock('@/stores/payment', () => ({
  usePaymentStore: () => paymentStore,
}))

vi.mock('@/api/payment', () => ({
  paymentAPI: {
    getOrder,
  },
}))

vi.mock('@stripe/stripe-js/pure', () => ({
  loadStripe,
}))

import StripePaymentView from '../StripePaymentView.vue'
import { formatPaymentAmount } from '@/utils/payment/currency'
import type { PaymentOrder } from '@/api/payment.types'

/**
 * StripePaymentView.spec.ts checks Stripe initialization, currency rendering, loading state,
 * and processing polling through the mounted SDK page. The Stripe object and order requests
 * are replaced with fixtures so no confirmation reaches a real payment service.
 */

/**
 * orderFactory creates a Stripe order fixture with optional API field overrides. Each call
 * returns independent order data so currency and lifecycle scenarios can vary without
 * sharing test state.
 */
function orderFactory(overrides: Partial<PaymentOrder> = {}): PaymentOrder {
  return {
    id: 42,
    user_id: 7,
    amount: 100,
    pay_amount: 103,
    currency: 'CNY',
    fee_rate: 0.03,
    payment_type: 'stripe',
    out_trade_no: 'sub2_stripe_42',
    status: 'PENDING',
    order_type: 'balance',
    created_at: '2026-04-20T12:00:00Z',
    expires_at: '2026-04-20T12:30:00Z',
    refund_amount: 0,
    ...overrides,
  }
}

/**
 * mountView mounts Stripe checkout with its surrounding layout stubbed. Tests retain the
 * actual page initialization while SDK methods and network calls use the isolated mocks
 * configured for each case.
 */
function mountView() {
  return shallowMount(StripePaymentView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        Icon: true,
      },
    },
  })
}

describe('StripePaymentView', () => {
  beforeEach(() => {
    routeState.query = {
      order_id: '42',
      client_secret: 'pi_secret_42',
    }
    routerPush.mockReset()
    getOrder.mockReset()
    paymentStore.config = { stripe_publishable_key: 'pk_test' }
    paymentStore.fetchConfig.mockReset().mockResolvedValue(undefined)
    paymentStore.pollOrderStatus.mockReset()
    loadStripe.mockReset().mockResolvedValue(stripeInstance)
    stripeElements.create.mockReset().mockReturnValue(stripePaymentElement)
    stripePaymentElement.mount.mockReset()
    stripePaymentElement.on
      .mockReset()
      .mockImplementation((event: string, callback: () => void) => {
        if (event === 'ready') callback()
      })
    stripeInstance.elements.mockReset().mockReturnValue(stripeElements)
    stripeInstance.confirmPayment.mockReset()
    stripeInstance.confirmAlipayPayment.mockReset()
    stripeInstance.confirmWechatPayPayment.mockReset()
    window.localStorage.clear()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // Local recovery is missing when the Stripe page loads an order. The amount display must use
  // the currency returned by the order API before mounting checkout.
  it('本地恢复快照缺失时使用订单接口返回的 Stripe 币种展示金额', async () => {
    getOrder.mockResolvedValue({
      data: orderFactory({ currency: 'HKD', pay_amount: 103 }),
    })

    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    expect(getOrder).toHaveBeenCalledWith(42)
    expect(loadStripe).toHaveBeenCalledWith('pk_test')
    expect(wrapper.text()).toContain(formatPaymentAmount(103, 'HKD', 'zh-CN'))
  })

  // Order loading is deferred while the Stripe page is mounted. The skeleton must remain until
  // data is ready, then the Payment Element must mount once.
  it('订单读取期间显示骨架，数据就绪后只挂载一次支付表单', async () => {
    let finish!: (value: {
      data: PaymentOrder // resolved payment order payload
    }) => void
    getOrder.mockReturnValue(
      new Promise((resolve) => {
        finish = resolve
      }),
    )
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-loading-skeleton]').attributes('aria-busy')).toBe('true')
    expect(wrapper.find('#stripe-payment-element').exists()).toBe(false)
    expect(stripePaymentElement.mount).not.toHaveBeenCalled()

    finish({ data: orderFactory() })
    await flushPromises()
    await flushPromises()
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.find('#stripe-payment-element').exists()).toBe(true)
    expect(stripePaymentElement.mount).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })

  // The order API rejects during Stripe initialization. Loading must finish and the established
  // error presentation must remain available instead of leaving the skeleton indefinitely.
  it('订单读取失败时结束骨架并显示原有错误提示', async () => {
    getOrder.mockRejectedValue(new Error('order unavailable'))
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.text()).toContain('payment.stripeLoadFailed')
    expect(stripePaymentElement.mount).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // The QR order enters PROCESSING after initial polling. The page must clear the payment
  // instruction and switch local order checks to the fifteen-second cadence.
  it('订单进入处理中后改为每 15 秒查询本地状态', async () => {
    vi.useFakeTimers()
    routeState.query = {
      order_id: '42',
      client_secret: 'pi_secret_42',
      method: 'wechat_pay',
    }
    getOrder.mockResolvedValue({ data: orderFactory() })
    stripeInstance.confirmWechatPayPayment.mockResolvedValue({
      paymentIntent: {
        status: 'requires_action',
        next_action: {
          wechat_pay_display_qr_code: { image_data_url: 'data:image/png;base64,qr' },
        },
      },
    })
    paymentStore.pollOrderStatus
      .mockResolvedValueOnce(orderFactory({ status: 'PROCESSING' }))
      .mockResolvedValueOnce(orderFactory({ status: 'COMPLETED' }))

    const wrapper = mountView()
    await flushPromises()
    await flushPromises()

    await vi.advanceTimersByTimeAsync(3000)
    await flushPromises()
    expect(paymentStore.pollOrderStatus).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('payment.result.processing')
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.find('.spinner').exists()).toBe(true)

    await vi.advanceTimersByTimeAsync(14999)
    expect(paymentStore.pollOrderStatus).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await flushPromises()
    expect(paymentStore.pollOrderStatus).toHaveBeenCalledTimes(2)
    expect(wrapper.text()).toContain('payment.result.success')

    wrapper.unmount()
  })
})
