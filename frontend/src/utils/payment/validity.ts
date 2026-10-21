import type { SubscriptionPlan } from '@/api/payment.types'

/**
 * validity.ts renders the subscription duration labels shown on plan cards and checkout.
 * Singular and plural week or month identifiers share a label, while other stored units
 * follow the backend interpretation as days.
 */

/**
 * TranslateFn resolves payment duration labels from the active locale.
 */
type TranslateFn = (key: string) => string

/**
 * planValiditySuffix returns the localized plan duration using its stored count. Month and
 * week values accept singular or plural units; other values render as days, matching the
 * units interpreted by subscription billing.
 */
export function planValiditySuffix(
  plan: Pick<SubscriptionPlan, 'validity_days' | 'validity_unit'>,
  t: TranslateFn,
): string {
  const unit = String(plan.validity_unit || 'day')
    .trim()
    .toLowerCase()
  const base = unit.endsWith('s') ? unit.slice(0, -1) : unit
  const days = plan.validity_days
  if (base === 'month') {
    return days === 1 ? t('payment.perMonth') : `${days}${t('payment.months')}`
  }
  if (base === 'week') {
    return `${days}${t('payment.weeks')}`
  }
  // Unknown units follow the backend day interpretation, so historical values cannot display a
  // duration the purchase does not grant.
  return `${days}${t('payment.days')}`
}
