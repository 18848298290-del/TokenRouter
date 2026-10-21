import type { LocationQuery, LocationQueryRaw } from 'vue-router'
import type { SubscriptionPlan } from '@/api/payment.types'
import { normalizeVisibleMethod } from '@/utils/payment/paymentFlow'

/**
 * paymentWechatResume.ts parses WeChat authorization return parameters into the purchase
 * selection needed by checkout. It accepts backend resume tokens or older OpenID returns
 * and removes consumed authorization fields before the page starts another order request.
 *
 * Resume tokens keep order amount at zero because the backend resolves the saved intent.
 * OpenID returns need a positive amount from the query, selected plan, or existing
 * recharge form. A positive plan identifier implies subscription intent even when the
 * order-type parameter is absent. Query cleanup copies the route data so unrelated
 * parameters survive while authorization metadata cannot be replayed by another
 * initialization.
 */

/**
 * ParsedWechatResumeRoute carries authorization identity and purchase context for resumed
 * checkout. A present wechatResumeToken takes precedence over openid, so token results omit
 * openid and set orderAmount to zero for the backend to resolve the saved purchase intent.
 */
export interface ParsedWechatResumeRoute {
  orderAmount: number // resolved purchase amount
  orderType: 'balance' | 'subscription' // balance or subscription purchase
  paymentType: string // visible payment method
  planId?: number // optional selected plan identifier
  openid?: string // WeChat authorized user identifier
  wechatResumeToken?: string // backend purchase resume token
}

/**
 * readQueryString reads a scalar route value or the first array entry. Non-string values
 * become empty strings, giving authorization parsing a consistent representation for
 * missing or malformed query fields.
 */
function readQueryString(query: LocationQuery, key: string): string {
  const value = query[key]
  if (Array.isArray(value)) {
    return typeof value[0] === 'string' ? value[0] : ''
  }
  return typeof value === 'string' ? value : ''
}

/**
 * hasWechatResumeQuery reports whether the route contains a resume marker or credential.
 */
export function hasWechatResumeQuery(query: LocationQuery): boolean {
  if (readQueryString(query, 'wechat_resume') === '1') {
    return true
  }
  return (
    readQueryString(query, 'wechat_resume_token') !== '' || readQueryString(query, 'openid') !== ''
  )
}

/**
 * parseWechatResumeRoute resolves authorization identity and purchase selection from route
 * fields. A backend resume token takes precedence over OpenID and returns zero amount for
 * backend intent lookup. Without that token, OpenID is required and amount comes from a
 * positive query value, a matching subscription plan, or the existing balance amount. The
 * visible method defaults to WeChat, and a positive plan ID implies subscription so older
 * return URLs retain their intended purchase.
 */
export function parseWechatResumeRoute(
  query: LocationQuery,
  plans: SubscriptionPlan[],
  fallbackBalanceAmount: number,
): ParsedWechatResumeRoute | null {
  if (!hasWechatResumeQuery(query)) {
    return null
  }

  const wechatResumeToken = readQueryString(query, 'wechat_resume_token')
  const paymentType = normalizeVisibleMethod(readQueryString(query, 'payment_type')) || 'wxpay'
  const planId = Number.parseInt(readQueryString(query, 'plan_id'), 10)
  const hasPlanId = Number.isFinite(planId) && planId > 0
  const orderType =
    readQueryString(query, 'order_type') === 'subscription' || hasPlanId
      ? 'subscription'
      : 'balance'

  if (wechatResumeToken) {
    return {
      wechatResumeToken,
      paymentType,
      orderType,
      orderAmount: 0,
      planId: hasPlanId ? planId : undefined,
    }
  }

  const openid = readQueryString(query, 'openid')
  if (!openid) {
    return null
  }

  const rawAmount = Number.parseFloat(readQueryString(query, 'amount'))
  const orderAmount =
    Number.isFinite(rawAmount) && rawAmount > 0
      ? rawAmount
      : orderType === 'subscription'
        ? (plans.find((plan) => plan.id === planId)?.price ?? 0)
        : fallbackBalanceAmount

  return {
    openid,
    paymentType,
    orderType,
    orderAmount,
    planId: hasPlanId ? planId : undefined,
  }
}

/**
 * stripWechatResumeQuery copies route query fields and removes consumed authorization and
 * purchase context. Unrelated parameters remain in the result, allowing the checkout page
 * to replace its route before creating the resumed order.
 */
export function stripWechatResumeQuery(query: LocationQuery): LocationQueryRaw {
  const nextQuery: LocationQueryRaw = { ...query }
  delete nextQuery.wechat_resume
  delete nextQuery.wechat_resume_token
  delete nextQuery.openid
  delete nextQuery.state
  delete nextQuery.scope
  delete nextQuery.payment_type
  delete nextQuery.amount
  delete nextQuery.order_type
  delete nextQuery.plan_id
  return nextQuery
}
