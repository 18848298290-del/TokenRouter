import { describe, expect, it } from 'vitest'
import {
  buildPaymentErrorToastMessage,
  describePaymentScenarioError,
  normalizePaymentMethodForDisplay,
} from '../paymentUx'

/**
 * paymentUx.spec.ts checks wallet display aliases and scenario-specific error translation
 * keys. Supplied browser flags and error records make recovery guidance observable without
 * rendering a page or initiating any browser payment operation.
 */

describe('normalizePaymentMethodForDisplay', () => {
  // User-facing method labels receive several built-in wallet aliases. Normalization must return
  // their canonical identifiers so checkout errors and result labels use matching translation
  // keys.
  it('collapses visible payment aliases to canonical method ids', () => {
    expect(normalizePaymentMethodForDisplay(' alipay_direct ')).toBe('alipay')
    expect(normalizePaymentMethodForDisplay('wxpay_direct')).toBe('wxpay')
    expect(normalizePaymentMethodForDisplay('wechat_pay')).toBe('wxpay')
  })

  // A payment code has no built-in display alias. The helper must keep that code identifiable
  // rather than assigning it to an unrelated wallet label.
  it('leaves non-aliased methods untouched', () => {
    expect(normalizePaymentMethodForDisplay('stripe')).toBe('stripe')
  })
})

describe('describePaymentScenarioError', () => {
  // WeChat H5 checkout reports an authorization error on a mobile device. Error description must
  // select the specific message and instructions for completing payment in WeChat.
  it('maps WeChat H5 authorization errors to explicit in-app guidance', () => {
    expect(
      describePaymentScenarioError(
        { reason: 'WECHAT_H5_NOT_AUTHORIZED' },
        { paymentMethod: 'wxpay', isMobile: true, isWechatBrowser: false },
      ),
    ).toEqual({
      messageKey: 'payment.errors.wechatH5NotAuthorized',
      hintKey: 'payment.errors.wechatOpenInWeChatHint',
    })
  })

  // The same H5 authorization failure uses the direct-provider wallet alias. Alias normalization
  // must select the same WeChat message and recovery instructions as the canonical method.
  it('maps WeChat H5 authorization errors when provider aliases use wxpay_direct', () => {
    expect(
      describePaymentScenarioError(
        { reason: 'WECHAT_H5_NOT_AUTHORIZED' },
        { paymentMethod: 'wxpay_direct', isMobile: true, isWechatBrowser: false },
      ),
    ).toEqual({
      messageKey: 'payment.errors.wechatH5NotAuthorized',
      hintKey: 'payment.errors.wechatOpenInWeChatHint',
    })
  })

  // The browser reports that WeixinJSBridge is unavailable. Error description must identify the
  // JSAPI environment failure and direct the user toward the supported WeChat context.
  it('maps missing WeixinJSBridge to a JSAPI-specific prompt', () => {
    expect(
      describePaymentScenarioError(new Error('WeixinJSBridge is unavailable'), {
        paymentMethod: 'wxpay',
        isMobile: true,
        isWechatBrowser: true,
      }),
    ).toEqual({
      messageKey: 'payment.errors.wechatJsapiUnavailable',
      hintKey: 'payment.errors.wechatOpenInWeChatHint',
    })
  })

  // The checkout controller throws its internal unavailable-bridge marker. Error description
  // must produce the same user-facing prompt as the equivalent browser bridge error text.
  it('maps the internal JSAPI unavailable marker to the same prompt', () => {
    expect(
      describePaymentScenarioError(new Error('WECHAT_JSAPI_UNAVAILABLE'), {
        paymentMethod: 'wxpay',
        isMobile: true,
        isWechatBrowser: true,
      }),
    ).toEqual({
      messageKey: 'payment.errors.wechatJsapiUnavailable',
      hintKey: 'payment.errors.wechatOpenInWeChatHint',
    })
  })

  // Desktop Alipay checkout returns a generic gateway failure. Error description must select the
  // desktop-specific message and QR recovery instruction for that wallet.
  it('maps generic desktop Alipay failures to QR guidance', () => {
    expect(
      describePaymentScenarioError(
        { reason: 'PAYMENT_GATEWAY_ERROR' },
        { paymentMethod: 'alipay', isMobile: false, isWechatBrowser: false },
      ),
    ).toEqual({
      messageKey: 'payment.errors.alipayDesktopUnavailable',
      hintKey: 'payment.errors.alipayDesktopQrHint',
    })
  })
})

describe('buildPaymentErrorToastMessage', () => {
  // A checkout error has a primary message but no recovery hint. Toast composition must return
  // that message directly so the user sees no added placeholder text.
  it('returns the main message when no hint is present', () => {
    expect(buildPaymentErrorToastMessage('Payment failed')).toBe('Payment failed')
  })

  // A recognized checkout failure has both message and hint text. Toast composition must include
  // both parts in their intended order with readable spacing.
  it('appends the hint to the toast body when present', () => {
    expect(buildPaymentErrorToastMessage('Payment failed', 'Open WeChat to continue.')).toBe(
      'Payment failed Open WeChat to continue.',
    )
  })
})
