import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import WechatPaymentCallbackView from '@/views/auth/WechatPaymentCallbackView.vue'

/**
 * WechatPaymentCallbackView.spec.ts checks fragment and query authorization handling through
 * the mounted callback page. Router and browser-location doubles expose the resulting purchase
 * context while avoiding navigation to an external authorization service.
 */

// Router and location fixtures supply callback parameters while navigation and
// toast spies capture the page effects asserted by each case.
const { replaceMock, routeState, locationState, showErrorMock } = vi.hoisted(() => ({
  replaceMock: vi.fn(),
  routeState: {
    query: {} as Record<string, unknown>,
  },
  locationState: {
    current: {
      href: 'http://localhost/auth/wechat/payment/callback',
      hash: '',
      search: '',
      pathname: '/auth/wechat/payment/callback',
      origin: 'http://localhost',
    } as Location & {
      origin: string // test browser origin URL
    },
  },
  showErrorMock: vi.fn(),
}))

vi.mock('vue-router', () => ({
  // useRoute returns the mutable query fixture consumed by callback parsing.
  useRoute: () => routeState,
  // useRouter exposes the navigation spy used to inspect purchase destinations.
  useRouter: () => ({
    replace: replaceMock,
  }),
}))

vi.mock('vue-i18n', () => ({
  // useI18n provides fixed Chinese labels and locale for callback rendering.
  useI18n: () => ({
    // t resolves the callback labels used by the rendered assertions. Unknown
    // keys pass through unchanged so missing fixture translations remain visible
    // in the mounted page.
    t: (key: string) => {
      if (key === 'auth.wechatPayment.callbackTitle') return '正在恢复微信支付'
      if (key === 'auth.wechatPayment.callbackProcessing') return '正在恢复微信支付...'
      if (key === 'auth.wechatPayment.backToPayment') return '返回支付页'
      if (key === 'auth.wechatPayment.callbackMissingResumeToken')
        return '微信支付回调缺少恢复令牌。'
      return key
    },
    locale: { value: 'zh-CN' },
  }),
}))

vi.mock('@/stores', () => ({
  // useAppStore exposes the toast recorder consumed by the callback watcher.
  useAppStore: () => ({
    // showError forwards callback toast arguments to the assertion spy.
    showError: (...args: any[]) => showErrorMock(...args),
  }),
}))

describe('WechatPaymentCallbackView', () => {
  beforeEach(() => {
    replaceMock.mockReset()
    showErrorMock.mockReset()
    routeState.query = {}
    locationState.current = {
      href: 'http://localhost/auth/wechat/payment/callback',
      hash: '',
      search: '',
      pathname: '/auth/wechat/payment/callback',
      origin: 'http://localhost',
    } as Location & {
      origin: string // test browser origin URL
    }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState.current,
    })
  })

  // WeChat returns an opaque resume token in the URL fragment. The callback page must replace
  // its route with purchase context that carries the token for resumed checkout.
  it('redirects back to purchase with an opaque resume token from hash fragment', async () => {
    locationState.current.hash =
      '#wechat_resume_token=resume-token-123&redirect=%2Fpurchase%3Ffrom%3Dwechat'

    mount(WechatPaymentCallbackView)
    await flushPromises()

    expect(replaceMock).toHaveBeenCalledWith({
      path: '/purchase',
      query: {
        from: 'wechat',
        wechat_resume: '1',
        wechat_resume_token: 'resume-token-123',
      },
    })
  })

  // An older callback returns OpenID and purchase fields. The callback page must retain that
  // context in its purchase destination so the checkout page can create the intended order.
  it('redirects legacy openid callback payloads back to purchase while preserving resume context', async () => {
    locationState.current.hash =
      '#openid=openid-123&state=oauth-state&scope=snsapi_base&payment_type=wxpay_direct&amount=128&order_type=subscription&plan_id=7&redirect=%2Fpayment%3Ffrom%3Dwechat'

    mount(WechatPaymentCallbackView)
    await flushPromises()

    expect(replaceMock).toHaveBeenCalledWith({
      path: '/purchase',
      query: {
        from: 'wechat',
        wechat_resume: '1',
        openid: 'openid-123',
        state: 'oauth-state',
        scope: 'snsapi_base',
        payment_type: 'wxpay_direct',
        amount: '128',
        order_type: 'subscription',
        plan_id: '7',
      },
    })
  })

  // The authorization return lacks both supported resume identities. The callback must display
  // its missing-token error and leave the user on a recoverable callback page.
  it('shows an error when the callback payload is missing the resume token', async () => {
    locationState.current.hash = '#payment_type=wxpay'

    const wrapper = mount(WechatPaymentCallbackView)
    await flushPromises()

    expect(replaceMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('微信支付回调缺少恢复令牌。')
    expect(wrapper.text()).toContain('微信支付回调缺少恢复令牌。')
    expect(wrapper.find('.bg-red-50').exists()).toBe(false)
  })
})
