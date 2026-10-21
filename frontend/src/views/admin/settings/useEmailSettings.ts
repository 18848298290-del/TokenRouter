import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores'
import type { SettingsForm } from './settingsState'

/**
 * useEmailSettings.ts edits notification recipients and tests SMTP drafts for
 * the settings page. The page retains the controller across tab changes,
 * including the password-edited flag that decides whether a test sends the
 * entered password or asks the server to reuse its stored credential.
 */

/**
 * EmailSettingsForm selects the shared draft fields used by email controls.
 */
export type EmailSettingsForm = Pick<
  SettingsForm,
  | 'balance_low_notify_enabled'
  | 'balance_low_notify_threshold'
  | 'email_verify_enabled'
  | 'localized_settings'
  | 'provider_quota_notify_emails'
  | 'provider_quota_notify_enabled'
  | 'smtp_from_email'
  | 'smtp_host'
  | 'smtp_password'
  | 'smtp_password_configured'
  | 'smtp_port'
  | 'smtp_use_tls'
  | 'smtp_username'
  | 'subscription_expiry_notify_enabled'
>

/**
 * useEmailSettings creates email test actions against the page-owned mutable
 * draft. Call it during page setup and retain its returned state, particularly
 * the password-edited flag, while users move between settings tabs.
 */
export function useEmailSettings(form: EmailSettingsForm) {
  const { t } = useI18n()
  const appStore = useAppStore()

  // Test progress flags and recipient input belong to this controller so
  // hiding the email panel retains ongoing action state.
  const testingSmtp = ref(false)

  const sendingTestEmail = ref(false)

  // smtpPasswordManuallyEdited selects entered credentials for tests; the
  // page resets it after loading or successfully saving SMTP settings.
  const smtpPasswordManuallyEdited = ref(false)

  const testEmailAddress = ref('')

  /**
   * addQuotaNotifyEmail appends an enabled, verified recipient draft with an
   * empty address. It initializes a missing recipient list; bulk payload
   * construction later removes blank addresses before submitting these
   * administrator-managed recipients.
   */
  const addQuotaNotifyEmail = () => {
    if (!form.provider_quota_notify_emails) {
      form.provider_quota_notify_emails = []
    }
    form.provider_quota_notify_emails.push({
      email: '',
      disabled: false,
      verified: true,
    })
  }

  /**
   * testSmtpConnection tests current SMTP fields and reports the API result
   * through notifications. It sends the entered password only after a manual
   * edit; otherwise an empty password requests reuse of the configured
   * credential.
   */
  async function testSmtpConnection() {
    testingSmtp.value = true
    try {
      // Empty test passwords request the stored credential, keeping a masked
      // or untouched draft from being sent as a replacement secret.
      const smtpPasswordForTest = smtpPasswordManuallyEdited.value ? form.smtp_password : ''
      const result = await adminAPI.settings.testSmtpConnection({
        smtp_host: form.smtp_host,
        smtp_port: form.smtp_port,
        smtp_username: form.smtp_username,
        smtp_password: smtpPasswordForTest,
        smtp_use_tls: form.smtp_use_tls,
      })
      // A successful response may omit its message, so use the localized
      // success text while API failures follow the catch path.
      appStore.showSuccess(result.message || t('admin.settings.smtpConnectionSuccess'))
    } catch (error: unknown) {
      appStore.showError(extractApiErrorMessage(error, t('admin.settings.failedToTestSmtp')))
    } finally {
      testingSmtp.value = false
    }
  }

  /**
   * sendTestEmail sends a message to the controller recipient using the
   * current SMTP draft. A missing recipient produces a validation error, and
   * the sender name uses localized_settings.smtp_from_name.source while
   * password selection follows the manual-edit flag.
   */
  async function sendTestEmail() {
    if (!testEmailAddress.value) {
      appStore.showError(t('admin.settings.testEmail.enterRecipientHint'))
      return
    }

    sendingTestEmail.value = true
    try {
      // The manual-edit flag distinguishes a replacement secret from a loaded
      // draft when the server chooses credentials for this test message.
      const smtpPasswordForSend = smtpPasswordManuallyEdited.value ? form.smtp_password : ''
      const result = await adminAPI.settings.sendTestEmail({
        email: testEmailAddress.value,
        smtp_host: form.smtp_host,
        smtp_port: form.smtp_port,
        smtp_username: form.smtp_username,
        smtp_password: smtpPasswordForSend,
        smtp_from_name: form.localized_settings.smtp_from_name.source,
        smtp_from_email: form.smtp_from_email,
        smtp_use_tls: form.smtp_use_tls,
      })
      // A successful response may omit its message, so use the localized
      // success text while API failures follow the catch path.
      appStore.showSuccess(result.message || t('admin.settings.testEmailSent'))
    } catch (error: unknown) {
      appStore.showError(extractApiErrorMessage(error, t('admin.settings.failedToSendTestEmail')))
    } finally {
      sendingTestEmail.value = false
    }
  }
  return {
    testingSmtp,
    sendingTestEmail,
    smtpPasswordManuallyEdited,
    testEmailAddress,
    addQuotaNotifyEmail,
    testSmtpConnection,
    sendTestEmail,
  }
}
