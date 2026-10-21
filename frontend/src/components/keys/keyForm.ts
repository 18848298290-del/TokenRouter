import { mappingRowsToRecord, type ModelMappingRow } from '@/utils/modelMappingRules'

// keyForm.ts converts editable API-key restrictions into values used by the
// create and update requests. The form carries switches and text inputs, while
// requests need address arrays, numeric quota windows, and operation-specific
// expiry values. Model mapping conversion retains the shared editor's trimming
// and duplicate-key behavior, so the caller still performs validation first.
//
// Creation computes a rounded-up number of elapsed days at submission time;
// updating an existing key sends an ISO timestamp or an empty clearing value.
// An omitted creation expiry remains distinct from clearing an existing expiry.
// The current clock is sampled only when creation has an enabled date, preserving
// the form's existing timing and invalid-date behavior during conversion.

// KeyRestrictionForm contains the editable restriction fields shared by key forms.
export interface KeyRestrictionForm {
  // When disabled, both address inputs are ignored and emitted as empty lists,
  // allowing an existing key's previously configured restrictions to be cleared.
  enable_ip_restriction: boolean
  ip_whitelist: string // Newline-separated allowed addresses.
  ip_blacklist: string // Newline-separated blocked addresses.
  // Positive values set the balance-unit quota; null and nonpositive values emit
  // the zero value used by the key API for an unlimited quota.
  quota: number | null
  // enable_expiration and expiration_date jointly select an expiry conversion.
  // A disabled or empty input omits creation expiry and clears an existing key's
  // expiry; an enabled nonempty date follows the operation's date representation.
  enable_expiration: boolean // Expiration control enabled.
  expiration_date: string // Browser date input.
  // enable_rate_limit controls all three spending windows. Disabled windows emit
  // zero even when a previous positive value remains in the corresponding input.
  enable_rate_limit: boolean // Spending windows enabled.
  rate_limit_5h: number | null // Five-hour spending limit.
  rate_limit_1d: number | null // One-day spending limit.
  rate_limit_7d: number | null // Seven-day spending limit.
  model_mapping_rows: ModelMappingRow[] // Editable model redirects.
}

// KeyRestrictionPayload supplies normalized values for create and update requests.
export interface KeyRestrictionPayload {
  ipWhitelist: string[] // Allowed address entries.
  ipBlacklist: string[] // Blocked address entries.
  quota: number // Balance-unit quota limit.
  // Creation sets this value only for an enabled, nonempty date; past or invalid
  // dates retain the form's minimum of one day rather than sending a zero expiry.
  expiresInDays: number | undefined
  // Updates use an ISO date or an empty string to clear expiry; creation leaves
  // this value undefined because it submits remaining days through expiresInDays.
  expiresAt: string | undefined
  // rateLimitData contains all three spending windows; disabled controls and nonpositive
  // amounts become zero, which the key API uses to remove a window's spending cap.
  rateLimitData: {
    rate_limit_5h: number // Five-hour spending limit.
    rate_limit_1d: number // One-day spending limit.
    rate_limit_7d: number // Seven-day spending limit.
  }
  modelMapping: Record<string, string> // Normalized model redirects.
}

// parseIPList trims address lines while retaining their order and repeated entries.
function parseIPList(text: string): string[] {
  return text
    .split('\n')
    .map((ip) => ip.trim())
    .filter((ip) => ip.length > 0)
}

/**
 * buildKeyFormPayload normalizes restriction inputs for the selected key operation.
 * Callers validate the form first and pass editing when updating an existing key.
 * This function does not mutate inputs or perform requests. Creation samples the
 * clock only when converting an enabled date into remaining days, while editing
 * uses the selected date directly and propagates invalid-date conversion errors.
 * Disabled address and rate controls emit clearing arrays or zero windows. The
 * caller adds identity, group, and billing fields to the returned restriction data.
 */
export function buildKeyFormPayload(
  form: KeyRestrictionForm,
  editing: boolean,
): KeyRestrictionPayload {
  const ipWhitelist = form.enable_ip_restriction ? parseIPList(form.ip_whitelist) : []
  const ipBlacklist = form.enable_ip_restriction ? parseIPList(form.ip_blacklist) : []
  const quota = form.quota && form.quota > 0 ? form.quota : 0
  let expiresInDays: number | undefined
  let expiresAt: string | undefined
  if (form.enable_expiration && form.expiration_date) {
    if (!editing) {
      // Remaining milliseconds become whole 24-hour periods rounded upward;
      // past or invalid creation dates retain the existing minimum one-day value.
      const expDate = new Date(form.expiration_date)
      const now = new Date()
      const diffDays = Math.ceil((expDate.getTime() - now.getTime()) / (1000 * 60 * 60 * 24))
      expiresInDays = diffDays > 0 ? diffDays : 1
    } else {
      expiresAt = new Date(form.expiration_date).toISOString()
    }
  } else if (editing) {
    expiresAt = ''
  }

  // Disabling rate controls clears every stored spending window. Nonpositive
  // individual inputs also use zero, matching the key API's unlimited representation.
  const rateLimitData = form.enable_rate_limit
    ? {
        rate_limit_5h: form.rate_limit_5h && form.rate_limit_5h > 0 ? form.rate_limit_5h : 0,
        rate_limit_1d: form.rate_limit_1d && form.rate_limit_1d > 0 ? form.rate_limit_1d : 0,
        rate_limit_7d: form.rate_limit_7d && form.rate_limit_7d > 0 ? form.rate_limit_7d : 0,
      }
    : { rate_limit_5h: 0, rate_limit_1d: 0, rate_limit_7d: 0 }

  return {
    ipWhitelist,
    ipBlacklist,
    quota,
    expiresInDays,
    expiresAt,
    rateLimitData,
    modelMapping: mappingRowsToRecord(form.model_mapping_rows),
  }
}
