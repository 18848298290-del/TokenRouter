import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import type { SettingsForm } from './settingsState'

/**
 * gatewaySettingsValidation.ts validates and normalizes the gateway fields used during
 * bulk settings saves. The page coordinator invokes each returned operation at
 * its established position in the validation sequence, so the first reported
 * error and any preceding draft normalization remain observable to the administrator.
 */

/**
 * createGatewaySettingsValidation binds field validation to one page draft and its domain actions.
 * Create it during setup to capture translated feedback, then invoke its
 * returned operations from the ordered bulk payload builder when saving.
 */
export function createGatewaySettingsValidation(
  form: Pick<SettingsForm, 'openai_provider_quota_auto_pause'>,
) {
  // Validation reports through the current setup translation and toast services so the
  // page coordinator can stop the save on the first invalid quota threshold.
  const { t } = useI18n()
  const appStore = useAppStore()

  /**
   * validateQuotaThresholds reports whether both OpenAI quota thresholds are within
   * the accepted zero-to-one interval. Out-of-range input reports the quota field
   * error and returns false, leaving the editable thresholds available for correction.
   */
  function validateQuotaThresholds(): boolean {
    if (
      form.openai_provider_quota_auto_pause.default_threshold_5h < 0 ||
      form.openai_provider_quota_auto_pause.default_threshold_5h > 1 ||
      form.openai_provider_quota_auto_pause.default_threshold_7d < 0 ||
      form.openai_provider_quota_auto_pause.default_threshold_7d > 1
    ) {
      appStore.showError(t('admin.settings.openaiQuotaAutoPause.rangeError'))
      return false
    }
    return true
  }
  return { validateQuotaThresholds }
}
