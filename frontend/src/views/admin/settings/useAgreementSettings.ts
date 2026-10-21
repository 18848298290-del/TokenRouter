import { computed } from 'vue'
import { type SettingsSegmentedOption } from '@/components/common/settings/SettingsSegmented.vue'
import { newContentID, originalContent } from '@/i18n/content'
import { getLocale } from '@/i18n'
import { useI18n } from 'vue-i18n'
import type { LoginAgreementDocument } from '@/api/settings.types'
import type { SettingsForm } from './settingsState'

/**
 * useAgreementSettings.ts manages login agreement drafts and their public legal paths. New
 * documents receive content IDs and localized source fields; the editor can later change
 * the ID independently from title and Markdown. Route previews and save normalization use
 * the same slug conversion so their document identity rules agree.
 *
 * A missing usable slug becomes doc-N using the row position before empty rows are
 * removed. Normalization copies each document while retaining its localization envelope
 * and leaves duplicate rejection to the validator. This separation lets validation examine
 * the complete normalized list before replacing the shared draft, while additions and
 * removals remain immediately visible in the mounted panel.
 */

/**
 * AgreementSettingsForm selects agreement switches, revision date, and document drafts
 * from shared settings.
 */
export type AgreementSettingsForm = Pick<
  SettingsForm,
  | 'login_agreement_documents'
  | 'login_agreement_enabled'
  | 'login_agreement_mode'
  | 'login_agreement_updated_at'
>

/**
 * useAgreementSettings binds agreement editing and slug normalization to the supplied
 * reactive draft. Create it during page setup for translated mode choices; normalization
 * returns new document records whose localization envelopes remain shared.
 */
export function useAgreementSettings(form: AgreementSettingsForm) {
  // Mode choice labels follow the current interface language while the values
  // remain the modal and checkbox strings submitted by the validator.
  const { t } = useI18n()

  /**
   * normalizeLoginAgreementDocumentId converts supplied text into a lowercase ASCII legal
   * slug. It trims outer separators and collapses repeated hyphens or underscores;
   * unusable input returns an empty string for the caller's positional default.
   */
  function normalizeLoginAgreementDocumentId(raw: string): string {
    return raw
      .trim()
      .toLowerCase()
      .replace(/[^a-z0-9_-]+/g, '-')
      .replace(/[-_]{2,}/g, '-')
      .replace(/^[-_]+|[-_]+$/g, '')
  }

  /**
   * loginAgreementRoutePath returns the public legal route preview for a draft document.
   * It normalizes the ID or, when absent, its title, then uses the one-based row position
   * when the chosen text produces no slug.
   */
  function loginAgreementRoutePath(doc: LoginAgreementDocument, index: number): string {
    // A positional slug keeps incomplete rows previewable, and save normalization
    // uses the same pre-filter position so the preview identifies that document.
    const id = normalizeLoginAgreementDocumentId(doc.id || doc.title) || `doc-${index + 1}`
    return `/legal/${id}`
  }

  // The segmented control displays the two confirmation modes supported by
  // agreement validation, with translated labels and their identifying icons.
  const loginAgreementModeOptions = computed<SettingsSegmentedOption<string>[]>(() => [
    { value: 'modal', label: t('admin.settings.loginAgreement.modeModal'), icon: 'shield' },
    {
      value: 'checkbox',
      label: t('admin.settings.loginAgreement.modeCheckbox'),
      icon: 'checkCircle',
    },
  ])

  /**
   * addLoginAgreementDocument appends an empty document with a fresh content ID. Its title
   * and Markdown share a localized source envelope in the current locale, ready for the
   * panel editor.
   */
  function addLoginAgreementDocument() {
    form.login_agreement_documents.push({
      id: newContentID(),
      localization: originalContent({ title: '', content_md: '' }, getLocale()),
      title: '',
      content_md: '',
    })
  }

  /**
   * removeLoginAgreementDocument removes the agreement draft at the supplied list index.
   */
  function removeLoginAgreementDocument(index: number) {
    form.login_agreement_documents.splice(index, 1)
  }

  /**
   * normalizeLoginAgreementDocumentsForSave builds trimmed document records while carrying
   * each localization envelope with its source fields. It derives each slug from ID or
   * title and uses the original row position when normalization yields no slug. Rows with
   * neither title nor Markdown are removed after that derivation.
   *
   * The returned records share localization references with the draft, so callers treat
   * those envelopes as shared data. Duplicate slugs remain in the result for the validator
   * to report before committing the normalized collection to the form.
   */
  function normalizeLoginAgreementDocumentsForSave(): LoginAgreementDocument[] {
    return form.login_agreement_documents
      .map((doc, index) => ({
        id: normalizeLoginAgreementDocumentId(doc.id || doc.title) || `doc-${index + 1}`,
        title: doc.title.trim(),
        content_md: doc.content_md.trim(),
        localization: doc.localization,
      }))
      .filter((doc) => doc.title || doc.content_md)
  }

  /**
   * findDuplicateLoginAgreementDocumentId returns the first repeated ID in document order.
   * Call it after slug normalization to detect IDs that become equal; null means every
   * supplied ID is unique.
   */
  function findDuplicateLoginAgreementDocumentId(
    documents: LoginAgreementDocument[],
  ): string | null {
    const seen = new Set<string>()
    for (const doc of documents) {
      if (seen.has(doc.id)) {
        return doc.id
      }
      seen.add(doc.id)
    }
    return null
  }
  return {
    normalizeLoginAgreementDocumentId,
    loginAgreementRoutePath,
    loginAgreementModeOptions,
    addLoginAgreementDocument,
    removeLoginAgreementDocument,
    normalizeLoginAgreementDocumentsForSave,
    findDuplicateLoginAgreementDocumentId,
  }
}
