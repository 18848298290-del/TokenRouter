import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import type { useAgreementSettings } from './useAgreementSettings'
import type { SettingsForm } from './settingsState'

/**
 * agreementSettingsValidation.ts checks normalized login agreements before replacing the
 * shared document draft. An enabled confirmation requires a document, every retained
 * document requires a title, and normalized IDs must be unique even when confirmation is
 * disabled. The validator reports the first failed condition through the page toast store.
 *
 * Document normalization constructs candidate records first, allowing rejection to leave
 * the editable collection available for correction. Once every check succeeds, the
 * accepted mode and complete normalized collection replace their draft values together in
 * synchronous order. The page coordinator invokes this step before later domain checks, so
 * a later failure can still leave this accepted normalization visible.
 */

/**
 * createAgreementSettingsValidation connects document normalization to translated error
 * feedback for the shared draft. Supply actions bound to that same form so the returned
 * validation step checks the document collection that its serializer will consume.
 */
export function createAgreementSettingsValidation(
  form: Pick<
    SettingsForm,
    'login_agreement_enabled' | 'login_agreement_mode' | 'login_agreement_documents'
  >,
  actions: Pick<
    ReturnType<typeof useAgreementSettings>,
    'normalizeLoginAgreementDocumentsForSave' | 'findDuplicateLoginAgreementDocumentId'
  >,
) {
  // Validation feedback uses the page locale and toast store, while controller
  // actions operate on the same draft captured by this factory.
  const { t } = useI18n()
  const appStore = useAppStore()
  const { normalizeLoginAgreementDocumentsForSave, findDuplicateLoginAgreementDocumentId } = actions

  /**
   * normalizeDocuments accepts normalized agreement records and publishes them to the
   * draft after all document checks succeed. Enabled confirmation requires at least one
   * retained document; every retained document requires a title and a unique normalized
   * slug. The first failed check emits its specific error and returns false while the
   * editable collection remains available.
   *
   * After acceptance, checkbox remains checkbox and every other mode becomes modal,
   * followed by replacement of the document array. Later bulk-validation failures do not
   * undo these accepted draft changes, so the coordinator can retry using the normalized
   * values.
   */
  function normalizeDocuments(): boolean {
    const normalizedLoginAgreementDocuments = normalizeLoginAgreementDocumentsForSave()

    if (form.login_agreement_enabled && normalizedLoginAgreementDocuments.length === 0) {
      appStore.showError(t('admin.settings.loginAgreement.documentsRequired'))
      return false
    }

    const emptyTitleDocument = normalizedLoginAgreementDocuments.find((doc) => !doc.title)

    if (emptyTitleDocument) {
      appStore.showError(t('admin.settings.loginAgreement.documentTitleRequired'))
      return false
    }

    const duplicateLoginAgreementDocumentId = findDuplicateLoginAgreementDocumentId(
      normalizedLoginAgreementDocuments,
    )

    if (duplicateLoginAgreementDocumentId) {
      appStore.showError(
        t('admin.settings.loginAgreement.documentSlugDuplicate', {
          id: duplicateLoginAgreementDocumentId,
        }),
      )
      return false
    }

    // Agreement mode accepts the checkbox variant and otherwise uses modal
    // confirmation, matching the value applied when initial settings are hydrated.
    form.login_agreement_mode = form.login_agreement_mode === 'checkbox' ? 'checkbox' : 'modal'

    form.login_agreement_documents = normalizedLoginAgreementDocuments
    return true
  }
  return { normalizeDocuments }
}
