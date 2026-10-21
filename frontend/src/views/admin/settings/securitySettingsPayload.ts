import type { SettingsForm } from './settingsState'
import type { UpdateSettingsRequest } from '@/api/admin/settings'
import type { useSecuritySettings } from './useSecuritySettings'

/**
 * securitySettingsPayload.ts serializes registration, account security, CAPTCHA, and
 * forwarded IP settings for the bulk administrator update. Most values come directly
 * from the shared form, while whitelist tags use a separate editor representation
 * that needs the API domain prefix restored. The caller validates and normalizes the
 * drafts before invoking this serializer.
 *
 * Secret inputs represent replacement credentials. An empty secret becomes
 * undefined, allowing JSON serialization to omit that property and the server to
 * keep its stored value. Audit retention also distinguishes a cleared numeric
 * control from zero: nonfinite inputs use the default retention period, while zero
 * remains the permanent-retention value.
 */

/**
 * buildSecuritySettingsPayload returns security fields for a validated bulk settings
 * update. The caller supplies committed whitelist tags and the shared form; the
 * returned forwarded-header array still references the form array.
 *
 * Domain tags become API whitelist entries, with wildcard prefixes retained and
 * other entries receiving an at-sign. Empty secret drafts are omitted through
 * undefined values so existing stored credentials survive a settings round trip. A
 * cleared or nonfinite audit-retention input becomes 180 days, while finite zero is
 * sent unchanged.
 */
export function buildSecuritySettingsPayload(
  form: Pick<
    SettingsForm,
    | 'registration_enabled'
    | 'email_verify_enabled'
    | 'registration_email_normalization'
    | 'registration_email_domain_quota_enabled'
    | 'user_email_change_enabled'
    | 'promo_code_enabled'
    | 'invitation_code_enabled'
    | 'password_reset_enabled'
    | 'totp_enabled'
    | 'session_binding_enabled'
    | 'step_up_enabled'
    | 'audit_log_retention_days'
    | 'turnstile_enabled'
    | 'turnstile_site_key'
    | 'turnstile_secret_key'
    | 'tencent_captcha_enabled'
    | 'tencent_captcha_app_id'
    | 'tencent_captcha_app_secret_key'
    | 'tencent_captcha_cloud_secret_id'
    | 'tencent_captcha_cloud_secret_key'
    | 'tencent_captcha_region'
    | 'aliyun_captcha_enabled'
    | 'aliyun_captcha_access_key_id'
    | 'aliyun_captcha_access_key_secret'
    | 'aliyun_captcha_scene_id'
    | 'aliyun_captcha_prefix'
    | 'aliyun_captcha_region'
    | 'api_key_acl_trust_forwarded_ip'
    | 'forwarded_client_ip_headers'
  >,
  actions: Pick<ReturnType<typeof useSecuritySettings>, 'registrationEmailSuffixWhitelistTags'>,
): Pick<
  UpdateSettingsRequest,
  | 'registration_enabled'
  | 'email_verify_enabled'
  | 'registration_email_suffix_whitelist'
  | 'registration_email_normalization'
  | 'registration_email_domain_quota_enabled'
  | 'user_email_change_enabled'
  | 'promo_code_enabled'
  | 'invitation_code_enabled'
  | 'password_reset_enabled'
  | 'totp_enabled'
  | 'session_binding_enabled'
  | 'step_up_enabled'
  | 'audit_log_retention_days'
  | 'turnstile_enabled'
  | 'turnstile_site_key'
  | 'turnstile_secret_key'
  | 'tencent_captcha_enabled'
  | 'tencent_captcha_app_id'
  | 'tencent_captcha_app_secret_key'
  | 'tencent_captcha_cloud_secret_id'
  | 'tencent_captcha_cloud_secret_key'
  | 'tencent_captcha_region'
  | 'aliyun_captcha_enabled'
  | 'aliyun_captcha_access_key_id'
  | 'aliyun_captcha_access_key_secret'
  | 'aliyun_captcha_scene_id'
  | 'aliyun_captcha_prefix'
  | 'aliyun_captcha_region'
  | 'api_key_acl_trust_forwarded_ip'
  | 'forwarded_client_ip_headers'
> {
  const { registrationEmailSuffixWhitelistTags } = actions
  // JSON serialization removes undefined properties. Apply that representation to
  // each secret below so a blank replacement draft cannot erase its stored
  // credential.
  return {
    registration_enabled: form.registration_enabled,

    email_verify_enabled: form.email_verify_enabled,

    // The tag editor removes API prefixes; restore at-signs for exact domains while
    // retaining wildcard entries for backend matching.
    registration_email_suffix_whitelist: registrationEmailSuffixWhitelistTags.value.map((suffix) =>
      suffix.startsWith('*.') ? suffix : `@${suffix}`,
    ),

    registration_email_normalization: form.registration_email_normalization,

    registration_email_domain_quota_enabled: form.registration_email_domain_quota_enabled,

    user_email_change_enabled: form.user_email_change_enabled,

    promo_code_enabled: form.promo_code_enabled,

    invitation_code_enabled: form.invitation_code_enabled,

    password_reset_enabled: form.password_reset_enabled,

    totp_enabled: form.totp_enabled,

    session_binding_enabled: form.session_binding_enabled,

    step_up_enabled: form.step_up_enabled,

    // Cleared numeric inputs cannot bind to the backend integer field. Use its
    // 180-day default while retaining finite zero for permanent retention.
    audit_log_retention_days: Number.isFinite(form.audit_log_retention_days)
      ? form.audit_log_retention_days
      : 180,

    turnstile_enabled: form.turnstile_enabled,

    turnstile_site_key: form.turnstile_site_key,

    turnstile_secret_key: form.turnstile_secret_key || undefined,

    tencent_captcha_enabled: form.tencent_captcha_enabled,

    tencent_captcha_app_id: form.tencent_captcha_app_id,

    tencent_captcha_app_secret_key: form.tencent_captcha_app_secret_key || undefined,

    tencent_captcha_cloud_secret_id: form.tencent_captcha_cloud_secret_id || undefined,

    tencent_captcha_cloud_secret_key: form.tencent_captcha_cloud_secret_key || undefined,

    tencent_captcha_region: form.tencent_captcha_region,

    aliyun_captcha_enabled: form.aliyun_captcha_enabled,

    aliyun_captcha_access_key_id: form.aliyun_captcha_access_key_id,

    aliyun_captcha_access_key_secret: form.aliyun_captcha_access_key_secret || undefined,

    aliyun_captcha_scene_id: form.aliyun_captcha_scene_id,

    aliyun_captcha_prefix: form.aliyun_captcha_prefix,

    aliyun_captcha_region: form.aliyun_captcha_region,

    api_key_acl_trust_forwarded_ip: form.api_key_acl_trust_forwarded_ip,

    forwarded_client_ip_headers: form.forwarded_client_ip_headers,
  }
}
