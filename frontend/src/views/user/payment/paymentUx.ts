import { normalizeVisibleMethod } from '@/utils/payment/paymentFlow'
import { extractApiErrorCode } from '@/utils/apiError'

/**
 * paymentUx.ts maps payment method aliases and gateway failures to user-facing translation
 * keys. Checkout and result pages use these labels and hints while retaining control of
 * when messages are displayed or combined into a toast.
 *
 * Scenario descriptions distinguish wallet identity, mobile device context, and the WeChat
 * browser because the available recovery instructions differ. Known API error codes take
 * precedence, with selected JSAPI text signatures also recognized for bridge failures.
 * Unrecognized methods or errors return null so the caller can use its ordinary API-error
 * display. Display aliases remain separate from provider credentials and do not determine
 * how an order executes.
 */

// Display aliases map external WeChat names to the wallet label used by checkout and result-
// page messages.
const DISPLAY_METHOD_ALIASES: Record<string, string> = {
  wechat: 'wxpay',
  wechat_pay: 'wxpay',
}

/**
 * PaymentScenarioContext describes the selected wallet and browser presenting an error.
 */
export interface PaymentScenarioContext {
  paymentMethod: string // selected visible payment method
  isMobile: boolean // mobile device detection
  isWechatBrowser: boolean // WeChat browser detection
}

/**
 * PaymentScenarioErrorDescriptor names the localized error message and optional recovery
 * hint.
 */
export interface PaymentScenarioErrorDescriptor {
  messageKey: string // translated primary error key
  hintKey?: string // optional recovery instruction key
}

/**
 * normalizePaymentMethodForDisplay trims and normalizes wallet aliases for labels and
 * hints. Unknown codes remain lowercased so custom payment methods can retain an
 * identifiable display key alongside the built-in methods.
 */
export function normalizePaymentMethodForDisplay(paymentType: string): string {
  const trimmed = paymentType.trim().toLowerCase()
  const visibleMethod = normalizeVisibleMethod(trimmed)
  if (visibleMethod) return visibleMethod
  return DISPLAY_METHOD_ALIASES[trimmed] ?? trimmed
}

/**
 * paymentMethodI18nKey returns the translation key for a normalized payment method.
 */
export function paymentMethodI18nKey(paymentType: string): string {
  return `payment.methods.${normalizePaymentMethodForDisplay(paymentType)}`
}

/**
 * buildPaymentErrorToastMessage combines a primary message with an optional recovery hint.
 * An absent hint returns the original message, while a supplied hint joins with one space
 * and trims outside whitespace.
 */
export function buildPaymentErrorToastMessage(message: string, hint?: string): string {
  if (!hint) return message
  return `${message} ${hint}`.trim()
}

/**
 * defaultWechatHint chooses desktop scanning or opening the payment inside WeChat.
 */
function defaultWechatHint(context: PaymentScenarioContext): string {
  if (!context.isMobile) return 'payment.errors.wechatScanOnDesktopHint'
  return 'payment.errors.wechatOpenInWeChatHint'
}

/**
 * defaultAlipayHint chooses mobile application or desktop QR recovery instructions.
 */
function defaultAlipayHint(context: PaymentScenarioContext): string {
  if (context.isMobile) return 'payment.errors.alipayMobileOpenHint'
  return 'payment.errors.alipayDesktopQrHint'
}

/**
 * describePaymentScenarioError selects localized message and hint keys for recognized
 * wallet failures. It normalizes the displayed method, extracts the API code, and also
 * recognizes specified JSAPI failure text. WeChat browser configuration errors can suggest
 * switching browser, while unavailable bridge errors suggest opening WeChat. Other WeChat
 * and Alipay failures choose device-specific instructions. A null result leaves generic
 * API error handling to the caller when the failure has no recognized recovery
 * description.
 */
export function describePaymentScenarioError(
  error: unknown,
  context: PaymentScenarioContext,
): PaymentScenarioErrorDescriptor | null {
  const method = normalizePaymentMethodForDisplay(context.paymentMethod)
  const code = extractApiErrorCode(error)
  const message =
    error instanceof Error
      ? error.message
      : typeof error === 'object' &&
          error &&
          'message' in error &&
          typeof error.message === 'string'
        ? error.message
        : String(error || '')
  const normalizedMessage = message.toLowerCase()

  if (method === 'wxpay') {
    if (code === 'WECHAT_H5_NOT_AUTHORIZED') {
      return {
        messageKey: 'payment.errors.wechatH5NotAuthorized',
        hintKey: defaultWechatHint(context),
      }
    }
    if (code === 'WECHAT_PAYMENT_MP_NOT_CONFIGURED') {
      return {
        messageKey: 'payment.errors.wechatPaymentMpNotConfigured',
        hintKey: context.isWechatBrowser
          ? 'payment.errors.wechatSwitchBrowserHint'
          : defaultWechatHint(context),
      }
    }
    if (code === 'NO_AVAILABLE_INSTANCE') {
      return {
        messageKey: 'payment.errors.wechatUnavailable',
        hintKey: defaultWechatHint(context),
      }
    }
    if (
      code === 'WECHAT_JSAPI_FAILED' ||
      normalizedMessage.includes('get_brand_wcpay_request:fail')
    ) {
      return {
        messageKey: 'payment.errors.wechatJsapiFailed',
        hintKey: defaultWechatHint(context),
      }
    }
    if (
      normalizedMessage.includes('weixinjsbridge is unavailable') ||
      normalizedMessage.includes('wechat_jsapi_unavailable')
    ) {
      return {
        messageKey: 'payment.errors.wechatJsapiUnavailable',
        hintKey: 'payment.errors.wechatOpenInWeChatHint',
      }
    }
    if (code === 'PAYMENT_GATEWAY_ERROR' || code === 'UNHANDLED_PAYMENT_SCENARIO') {
      return {
        messageKey: 'payment.errors.wechatUnavailable',
        hintKey: defaultWechatHint(context),
      }
    }
  }

  if (
    method === 'alipay' &&
    (code === 'PAYMENT_GATEWAY_ERROR' || code === 'UNHANDLED_PAYMENT_SCENARIO')
  ) {
    return {
      messageKey: context.isMobile
        ? 'payment.errors.alipayMobileUnavailable'
        : 'payment.errors.alipayDesktopUnavailable',
      hintKey: defaultAlipayHint(context),
    }
  }

  return null
}
