import type { SettingsForm } from './settingsState'
import type { UpdateSettingsRequest } from '@/api/admin/settings'

/**
 * paymentSettingsPayload.ts builds the payment fields in the bulk settings
 * request from the shared draft. Numeric controls are coerced with field-specific defaults, while the method-fee map and enabled-type array are
 * passed through for the page request to serialize.
 */

/**
 * buildPaymentSettingsPayload returns payment request fields with numeric
 * defaults applied. Falsy or nonnumeric amounts become zero, recharge
 * multiplier and cancellation window become one, and cancellation count
 * becomes ten; method fees and enabled types remain shared references.
 */
export function buildPaymentSettingsPayload(
  form: Pick<
    SettingsForm,
    | 'payment_enabled'
    | 'payment_min_amount'
    | 'payment_max_amount'
    | 'payment_daily_limit'
    | 'payment_max_pending_orders'
    | 'payment_order_timeout_minutes'
    | 'payment_balance_disabled'
    | 'payment_balance_recharge_multiplier'
    | 'payment_subscription_usd_to_cny_rate'
    | 'payment_recharge_fee_rate'
    | 'payment_method_fees'
    | 'payment_enabled_types'
    | 'payment_load_balance_strategy'
    | 'payment_cancel_rate_limit_enabled'
    | 'payment_cancel_rate_limit_max'
    | 'payment_cancel_rate_limit_window'
    | 'payment_cancel_rate_limit_unit'
    | 'payment_cancel_rate_limit_window_mode'
    | 'payment_alipay_force_qrcode'
    | 'payment_alipay_mobile_precreate_deep_link'
  >,
): Pick<
  UpdateSettingsRequest,
  | 'payment_enabled'
  | 'payment_min_amount'
  | 'payment_max_amount'
  | 'payment_daily_limit'
  | 'payment_max_pending_orders'
  | 'payment_order_timeout_minutes'
  | 'payment_balance_disabled'
  | 'payment_balance_recharge_multiplier'
  | 'payment_subscription_usd_to_cny_rate'
  | 'payment_recharge_fee_rate'
  | 'payment_method_fees'
  | 'payment_enabled_types'
  | 'payment_load_balance_strategy'
  | 'payment_cancel_rate_limit_enabled'
  | 'payment_cancel_rate_limit_max'
  | 'payment_cancel_rate_limit_window'
  | 'payment_cancel_rate_limit_unit'
  | 'payment_cancel_rate_limit_window_mode'
  | 'payment_alipay_force_qrcode'
  | 'payment_alipay_mobile_precreate_deep_link'
> {
  return {
    // Numeric controls can contain input strings; each coercion applies the
    // request default for that field when conversion is falsy.
    payment_enabled: form.payment_enabled,

    payment_min_amount: Number(form.payment_min_amount) || 0,

    payment_max_amount: Number(form.payment_max_amount) || 0,

    payment_daily_limit: Number(form.payment_daily_limit) || 0,

    payment_max_pending_orders: Number(form.payment_max_pending_orders) || 0,

    payment_order_timeout_minutes: Number(form.payment_order_timeout_minutes) || 0,

    payment_balance_disabled: form.payment_balance_disabled,

    payment_balance_recharge_multiplier: Number(form.payment_balance_recharge_multiplier) || 1,

    payment_subscription_usd_to_cny_rate: Number(form.payment_subscription_usd_to_cny_rate) || 0,

    payment_recharge_fee_rate: Number(form.payment_recharge_fee_rate) || 0,

    payment_method_fees: form.payment_method_fees,

    payment_enabled_types: form.payment_enabled_types,

    payment_load_balance_strategy: form.payment_load_balance_strategy,

    payment_cancel_rate_limit_enabled: form.payment_cancel_rate_limit_enabled,

    payment_cancel_rate_limit_max: Number(form.payment_cancel_rate_limit_max) || 10,

    payment_cancel_rate_limit_window: Number(form.payment_cancel_rate_limit_window) || 1,

    payment_cancel_rate_limit_unit: form.payment_cancel_rate_limit_unit,

    payment_cancel_rate_limit_window_mode: form.payment_cancel_rate_limit_window_mode,

    payment_alipay_force_qrcode: form.payment_alipay_force_qrcode,

    payment_alipay_mobile_precreate_deep_link: form.payment_alipay_mobile_precreate_deep_link,
  }
}
