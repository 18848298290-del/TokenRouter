import type { SettingsForm } from './settingsState'
import type { UpdateSettingsRequest, DefaultSubscriptionSetting } from '@/api/admin/settings'

/**
 * usersSettingsPayload.ts builds global user-default fields for the administrator
 * bulk update. The page supplies subscriptions after ordered validation, and this
 * serializer reuses that normalized array while copying balance, concurrency, email
 * requirements, and account limits from the shared draft.
 */

/**
 * buildUsersSettingsPayload returns global defaults with the caller-normalized
 * subscription array. The array is reused by reference, and per-authentication-
 * source grants are appended separately by the page payload builder after all domain
 * serializers finish.
 */
export function buildUsersSettingsPayload(
  form: Pick<
    SettingsForm,
    | 'default_balance'
    | 'default_concurrency'
    | 'force_email_on_third_party_signup'
    | 'default_user_rpm_limit'
    | 'default_user_api_key_limit'
  >,
  normalizedDefaultSubscriptions: DefaultSubscriptionSetting[],
): Pick<
  UpdateSettingsRequest,
  | 'default_balance'
  | 'default_concurrency'
  | 'default_subscriptions'
  | 'force_email_on_third_party_signup'
  | 'default_user_rpm_limit'
  | 'default_user_api_key_limit'
> {
  return {
    default_balance: form.default_balance,

    default_concurrency: form.default_concurrency,

    default_subscriptions: normalizedDefaultSubscriptions,

    force_email_on_third_party_signup: form.force_email_on_third_party_signup,

    default_user_rpm_limit: form.default_user_rpm_limit,

    default_user_api_key_limit: form.default_user_api_key_limit,
  }
}
