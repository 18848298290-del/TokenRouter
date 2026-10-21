import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores'

/**
 * useAdminApiKeySettings.ts manages administrator API key status and immediate
 * credential operations. Creating or rotating a key receives its full value for
 * display and copying, while status loads expose a mask. Deletion clears displayed
 * credential values after the server confirms success, and failures retain the
 * current display with translated feedback.
 */

/**
 * useAdminApiKeySettings creates page-lifetime state and immediate administrator
 * credential actions during component setup. The page starts status loading, and
 * buttons invoke mutations directly rather than registering them in the bulk
 * settings save target.
 */
export function useAdminApiKeySettings() {
  /**
   * t and appStore provide translated confirmation prompts and operation feedback
   * for the credential controls in this page.
   */
  const { t } = useI18n()
  const appStore = useAppStore()

  /**
   * adminApiKeyLoading selects the status skeleton until the current status request
   * finishes, including read failures.
   */
  const adminApiKeyLoading = ref(true)

  /**
   * adminApiKeyExists reflects the last successful status or mutation and selects
   * creation versus rotation and deletion controls.
   */
  const adminApiKeyExists = ref(false)

  /**
   * adminApiKeyMasked holds the server mask or a locally abbreviated generated
   * credential for the current-key display.
   */
  const adminApiKeyMasked = ref('')

  /**
   * adminApiKeyOperating disables mutation buttons while a creation or deletion
   * request is pending in the panel.
   */
  const adminApiKeyOperating = ref(false)

  /**
   * newAdminApiKey retains the latest generated full credential for copying until
   * generation replaces it, deletion succeeds, or page teardown.
   */
  const newAdminApiKey = ref('')

  /**
   * loadAdminApiKey refreshes whether a credential exists and its masked display
   * value. Read failures are silent and retain the current status; the loading flag
   * clears on either outcome, and a generated full value remains available.
   */
  async function loadAdminApiKey() {
    adminApiKeyLoading.value = true
    try {
      const status = await adminAPI.settings.getAdminApiKey()
      adminApiKeyExists.value = status.exists
      adminApiKeyMasked.value = status.masked_key
    } catch (_error: unknown) {
      // Keep the last displayed status when this optional read fails; other settings
      // remain usable and the loading skeleton ends.
    } finally {
      adminApiKeyLoading.value = false
    }
  }

  /**
   * createAdminApiKey immediately creates or replaces the server credential and
   * displays its returned full value. The caller supplies any confirmation; failed
   * requests show an error and retain existing credential values while clearing the
   * operating flag.
   */
  async function createAdminApiKey() {
    adminApiKeyOperating.value = true
    try {
      const result = await adminAPI.settings.regenerateAdminApiKey()
      // Publish the returned credential after server success so copy and status
      // controls refer to the credential just created.
      newAdminApiKey.value = result.key
      adminApiKeyExists.value = true
      // The current-key label shows ten leading and four trailing characters; the
      // separate copy field retains the complete returned credential.
      adminApiKeyMasked.value = result.key.substring(0, 10) + '...' + result.key.slice(-4)
      appStore.showSuccess(t('admin.settings.adminApiKey.keyGenerated'))
    } catch (error: unknown) {
      appStore.showError(extractApiErrorMessage(error, t('common.error')))
    } finally {
      adminApiKeyOperating.value = false
    }
  }

  /**
   * regenerateAdminApiKey asks for confirmation before delegating immediate rotation
   * to createAdminApiKey. Cancelling the native prompt leaves credential state
   * untouched, while a confirmed action shares the creation path and its feedback.
   */
  async function regenerateAdminApiKey() {
    if (!confirm(t('admin.settings.adminApiKey.regenerateConfirm'))) return
    await createAdminApiKey()
  }

  /**
   * deleteAdminApiKey confirms and immediately deletes the server credential,
   * clearing displayed values after success. Cancellation does nothing; request
   * failures retain the prior mask and full value and show translated error
   * feedback.
   */
  async function deleteAdminApiKey() {
    if (!confirm(t('admin.settings.adminApiKey.deleteConfirm'))) return
    adminApiKeyOperating.value = true
    try {
      await adminAPI.settings.deleteAdminApiKey()
      // Clear both displayed credential forms after deletion succeeds; a rejected
      // deletion keeps them available for administrator feedback and retry.
      adminApiKeyExists.value = false
      adminApiKeyMasked.value = ''
      newAdminApiKey.value = ''
      appStore.showSuccess(t('admin.settings.adminApiKey.keyDeleted'))
    } catch (error: unknown) {
      appStore.showError(extractApiErrorMessage(error, t('common.error')))
    } finally {
      adminApiKeyOperating.value = false
    }
  }

  /**
   * copyNewKey starts copying the retained generated credential through the browser
   * clipboard API. It reports asynchronous success or failure through translated
   * toasts and returns before the clipboard promise settles.
   */
  function copyNewKey() {
    navigator.clipboard
      .writeText(newAdminApiKey.value)
      .then(() => {
        appStore.showSuccess(t('admin.settings.adminApiKey.keyCopied'))
      })
      .catch(() => {
        appStore.showError(t('common.copyFailed'))
      })
  }
  /**
   * The returned credential state remains with the page controller, and its actions
   * are invoked directly by AdminApiKeySettings buttons.
   */
  return {
    adminApiKeyLoading,
    adminApiKeyExists,
    adminApiKeyMasked,
    adminApiKeyOperating,
    newAdminApiKey,
    loadAdminApiKey,
    createAdminApiKey,
    regenerateAdminApiKey,
    deleteAdminApiKey,
    copyNewKey,
  }
}
