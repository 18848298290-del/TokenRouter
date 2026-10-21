import { readStorageWithLegacyKey } from './storage'

/**
 * loginAgreement.ts reads revision-specific login consent and records its
 * withdrawal under the current storage key. Legacy consent is inherited only
 * when the current key is missing. Withdrawal stores an empty object so an
 * older consent entry cannot become active again through fallback.
 */

/**
 * LOGIN_AGREEMENT_STORAGE_KEY names current-brand consent storage, whose
 * presence takes precedence over the legacy-brand consent entry.
 */
export const LOGIN_AGREEMENT_STORAGE_KEY = 'tokenrouter_login_agreement_consent'

/**
 * revokeLoginAgreement writes an empty current-key consent object to override
 * legacy acceptance. It returns outside the browser, but storage write
 * failures propagate so callers cannot assume a failed write withdrew consent.
 */
export function revokeLoginAgreement(): void {
  if (typeof window === 'undefined') return
  /**
   * Write a present empty consent object rather than removing the key, because
   * removing it would permit the legacy accepted revision to be inherited again.
   */
  window.localStorage.setItem(LOGIN_AGREEMENT_STORAGE_KEY, '{}')
}

/**
 * hasAcceptedLoginAgreement compares persisted consent with the required
 * nonempty revision. It can inherit a legacy entry through the storage helper;
 * malformed JSON, missing browser state, and read failures return false.
 */
export function hasAcceptedLoginAgreement(revision: string): boolean {
  if (!revision || typeof window === 'undefined') return false
  try {
    const raw = readStorageWithLegacyKey(
      window.localStorage,
      LOGIN_AGREEMENT_STORAGE_KEY,
      'sub2api_login_agreement_consent',
    )
    if (!raw) return false
    const parsed = JSON.parse(raw) as {
      revision?: string // accepted agreement revision
    }
    return parsed.revision === revision
  } catch {
    /**
     * Unusable storage or consent JSON cannot prove acceptance of the current
     * revision, so the quick-login eligibility check remains false.
     */
    return false
  }
}
