import type { SettingsForm } from './settingsState'
import type { UpdateSettingsRequest } from '@/api/admin/settings'
import { currentOrigin } from './settingsDefaults'

/**
 * emailSettingsPayload.ts builds SMTP and notification fields for the bulk
 * settings request. Empty SMTP passwords are omitted so the server can retain
 * its configured credential, and blank notification recipients are filtered
 * before submission. An absent recharge URL is filled from the captured
 * browser origin in both the draft and request.
 */

/**
 * buildEmailSettingsPayload returns the email request fields and fills an
 * absent recharge URL in the shared draft. Empty passwords become undefined,
 * balance thresholds are numerically coerced, and recipients with whitespace-only addresses are omitted while retained recipient objects remain shared.
 */
export function buildEmailSettingsPayload(
  form: Pick<
    SettingsForm,
    | 'smtp_host'
    | 'smtp_port'
    | 'smtp_username'
    | 'smtp_password'
    | 'smtp_from_email'
    | 'smtp_use_tls'
    | 'balance_low_notify_enabled'
    | 'balance_low_notify_threshold'
    | 'balance_low_notify_recharge_url'
    | 'subscription_expiry_notify_enabled'
    | 'provider_quota_notify_enabled'
    | 'provider_quota_notify_emails'
  >,
): Pick<
  UpdateSettingsRequest,
  | 'smtp_host'
  | 'smtp_port'
  | 'smtp_username'
  | 'smtp_password'
  | 'smtp_from_email'
  | 'smtp_use_tls'
  | 'balance_low_notify_enabled'
  | 'balance_low_notify_threshold'
  | 'balance_low_notify_recharge_url'
  | 'subscription_expiry_notify_enabled'
  | 'provider_quota_notify_enabled'
  | 'provider_quota_notify_emails'
> {
  return {
    smtp_host: form.smtp_host,

    smtp_port: form.smtp_port,

    smtp_username: form.smtp_username,

    // Omitting an empty password lets the update endpoint retain its stored
    // secret when the password input remains untouched.
    smtp_password: form.smtp_password || undefined,

    smtp_from_email: form.smtp_from_email,

    smtp_use_tls: form.smtp_use_tls,

    // Balance, subscription, and provider notifications share the bulk
    // request but retain their independently configured enablement flags.
    balance_low_notify_enabled: form.balance_low_notify_enabled,

    balance_low_notify_threshold: Number(form.balance_low_notify_threshold) || 0,

    // Filling the draft alongside the request makes the recharge URL used by
    // the save visible to subsequent settings reads.
    balance_low_notify_recharge_url: (form.balance_low_notify_recharge_url =
      form.balance_low_notify_recharge_url || currentOrigin),

    subscription_expiry_notify_enabled: form.subscription_expiry_notify_enabled,

    provider_quota_notify_enabled: form.provider_quota_notify_enabled,

    // Recipient rows begin with empty addresses; discard blank rows while
    // retaining enabled and verified flags on populated entries.
    provider_quota_notify_emails: (form.provider_quota_notify_emails || []).filter(
      (e) => e.email.trim() !== '',
    ),
  }
}
