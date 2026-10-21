import type { SettingsForm } from './settingsState'
import type { UpdateSettingsRequest } from '@/api/admin/settings'

/**
 * agreementSettingsPayload.ts builds the login agreement portion of the bulk settings
 * request. The coordinator validates document titles, normalized slugs, and confirmation
 * mode before calling this serializer, which forwards the accepted document collection and
 * revision date from the shared draft.
 */

/**
 * buildAgreementSettingsPayload returns agreement flags, mode, revision date, and the
 * current document array. Call it after agreement validation; the array and its localized
 * envelopes retain their shared draft references until request encoding.
 */
export function buildAgreementSettingsPayload(
  form: Pick<
    SettingsForm,
    | 'login_agreement_enabled'
    | 'login_agreement_mode'
    | 'login_agreement_updated_at'
    | 'login_agreement_documents'
  >,
): Pick<
  UpdateSettingsRequest,
  | 'login_agreement_enabled'
  | 'login_agreement_mode'
  | 'login_agreement_updated_at'
  | 'login_agreement_documents'
> {
  return {
    login_agreement_enabled: form.login_agreement_enabled,

    login_agreement_mode: form.login_agreement_mode,

    login_agreement_updated_at: form.login_agreement_updated_at,

    login_agreement_documents: form.login_agreement_documents,
  }
}
