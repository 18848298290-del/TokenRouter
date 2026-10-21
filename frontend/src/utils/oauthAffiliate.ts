/**
 * oauthAffiliate.ts carries referral codes through registration and OAuth
 * navigation using separate persistent and tab-scoped storage. Persistent
 * entries contain a normalized code and an expiry timestamp, while the OAuth
 * handoff stores only a transient code in sessionStorage.
 *
 * Explicit nonblank values take precedence over the persistent fallback and
 * refresh its thirty-day lifetime. Reads remove expired or malformed
 * persistent entries on a best-effort basis. Browser storage failures return
 * empty values or leave the flow running without persistence. Clearing one
 * storage channel does not clear the other unless the caller invokes the
 * combined cleanup after referral processing.
 */

/**
 * The OAuth key stores tab-local handoff state, while the persistent affiliate
 * key stores a code with the thirty-day lifetime.
 */
const OAUTH_AFFILIATE_CODE_KEY = 'oauth_aff_code'
const AFFILIATE_CODE_KEY = 'affiliate_aff_code'
const AFFILIATE_CODE_TTL_MS = 30 * 24 * 60 * 60 * 1000

/**
 * StoredAffiliateCode pairs a normalized persistent referral code with its
 * absolute expiry time.
 */
interface StoredAffiliateCode {
  code: string // normalized referral code
  expiresAt: number // expiry epoch milliseconds
}

/**
 * normalizeOAuthAffiliateCode trims a string referral value or the first array
 * element. Other input types produce an empty string, allowing route query
 * values to share one normalization rule.
 */
export function normalizeOAuthAffiliateCode(value?: unknown): string {
  const raw = Array.isArray(value) ? value[0] : value
  return typeof raw === 'string' ? raw.trim() : ''
}

/**
 * pickOAuthAffiliateCode selects the first nonblank normalized referral code
 * in argument order. An entirely empty candidate list produces an empty
 * string, leaving fallback selection to the caller.
 */
export function pickOAuthAffiliateCode(...values: unknown[]): string {
  for (const value of values) {
    const code = normalizeOAuthAffiliateCode(value)
    if (code) {
      return code
    }
  }
  return ''
}

/**
 * storeAffiliateCode saves a normalized referral code with a thirty-day expiry
 * from now. Blank input and server rendering leave storage unchanged; browser
 * storage errors are suppressed so registration can continue.
 */
export function storeAffiliateCode(value?: unknown, now = Date.now()): void {
  if (typeof window === 'undefined') {
    return
  }
  const code = normalizeOAuthAffiliateCode(value)
  if (!code) {
    return
  }
  try {
    /**
     * Record the normalized code and absolute expiry together in one serialized
     * entry so later pages can evaluate the same referral lifetime.
     */
    const payload: StoredAffiliateCode = {
      code,
      expiresAt: now + AFFILIATE_CODE_TTL_MS,
    }
    window.localStorage.setItem(AFFILIATE_CODE_KEY, JSON.stringify(payload))
  } catch {
    /**
     * Storage may reject the persistent write; keep registration usable even
     * though this referral will not survive a later navigation.
     */
  }
}

/**
 * loadAffiliateCode returns a nonexpired persistent referral code using now
 * for expiry checks. Malformed or expired entries are cleared best-effort and
 * return empty; unavailable browser storage also returns empty.
 */
export function loadAffiliateCode(now = Date.now()): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    const raw = window.localStorage.getItem(AFFILIATE_CODE_KEY)
    if (!raw) {
      return ''
    }
    const parsed = JSON.parse(raw) as Partial<StoredAffiliateCode>
    const code = normalizeOAuthAffiliateCode(parsed.code)
    const expiresAt = Number(parsed.expiresAt) || 0
    if (!code || expiresAt <= now) {
      /**
       * Remove unusable or expired persistent entries before returning empty,
       * preventing later reads from repeatedly treating stale referral data as a
       * candidate.
       */
      clearAffiliateCode()
      return ''
    }
    return code
  } catch {
    /**
     * Malformed or inaccessible saved data cannot supply a referral; attempt
     * cleanup and return absence without propagating browser storage errors.
     */
    clearAffiliateCode()
    return ''
  }
}

/**
 * clearAffiliateCode removes the persistent referral entry when a browser is
 * available. Storage removal failures are suppressed, so callers cannot treat
 * completion as proof that the entry was deleted.
 */
export function clearAffiliateCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.localStorage.removeItem(AFFILIATE_CODE_KEY)
  } catch {
    /**
     * A rejected removal leaves browser storage unchanged; suppress that failure
     * so referral cleanup does not interrupt the caller registration flow.
     */
  }
}

/**
 * resolveAffiliateCode prefers the first nonblank explicit candidate and
 * attempts to persist it. With no explicit code it reads the persistent
 * fallback; a failed write does not change the selected return value.
 */
export function resolveAffiliateCode(...values: unknown[]): string {
  const code = pickOAuthAffiliateCode(...values)
  if (code) {
    storeAffiliateCode(code)
    return code
  }
  return loadAffiliateCode()
}

/**
 * storeOAuthAffiliateCode writes a normalized code into the current tab
 * handoff entry. Blank input removes that entry, and unavailable browser
 * storage is tolerated without changing persistent referral data.
 */
export function storeOAuthAffiliateCode(value?: unknown): void {
  if (typeof window === 'undefined') {
    return
  }
  const code = normalizeOAuthAffiliateCode(value)
  try {
    if (code) {
      /**
       * The tab handoff stores a plain code separately from the expiring persistent
       * record, so clearing OAuth state does not erase the registration fallback.
       */
      window.sessionStorage.setItem(OAUTH_AFFILIATE_CODE_KEY, code)
    } else {
      window.sessionStorage.removeItem(OAUTH_AFFILIATE_CODE_KEY)
    }
  } catch {
    /**
     * A rejected tab-storage update cannot complete the handoff persistence, but
     * authentication can continue using any code already held by its caller.
     */
  }
}

/**
 * loadOAuthAffiliateCode returns the normalized code stored for this tab OAuth
 * handoff. Missing values, server rendering, and storage errors return empty
 * without consulting the persistent referral entry.
 */
export function loadOAuthAffiliateCode(): string {
  if (typeof window === 'undefined') {
    return ''
  }
  try {
    return normalizeOAuthAffiliateCode(window.sessionStorage.getItem(OAUTH_AFFILIATE_CODE_KEY))
  } catch {
    /**
     * An inaccessible tab entry cannot supply a handoff code; return empty without
     * consulting the separately managed persistent referral record.
     */
    return ''
  }
}

/**
 * clearOAuthAffiliateCode removes the current tab OAuth handoff entry when
 * possible. Storage failures are suppressed, and the separate persistent
 * referral entry remains available for subsequent registration flows.
 */
export function clearOAuthAffiliateCode(): void {
  if (typeof window === 'undefined') {
    return
  }
  try {
    window.sessionStorage.removeItem(OAUTH_AFFILIATE_CODE_KEY)
  } catch {
    /**
     * A failed tab-entry removal is tolerated independently of persistent storage,
     * allowing combined cleanup to attempt both channels even when one is blocked.
     */
  }
}

/**
 * oauthAffiliatePayload builds the optional referral field for an
 * authentication request. Blank normalized codes produce an empty object so
 * callers omit the field instead of submitting an empty value.
 */
export function oauthAffiliatePayload(code: string): {
  aff_code?: string // normalized referral code
} {
  const normalized = normalizeOAuthAffiliateCode(code)
  return normalized ? { aff_code: normalized } : {}
}

/**
 * clearAllAffiliateCodes attempts to clear the OAuth handoff before the
 * persistent referral entry. Each operation tolerates storage errors
 * independently, so failure in one storage channel does not stop the other
 * cleanup.
 */
export function clearAllAffiliateCodes(): void {
  /**
   * Attempt the transient cleanup first and persistent cleanup second; each
   * helper suppresses its own storage errors so both channels receive a removal
   * attempt.
   */
  clearOAuthAffiliateCode()
  clearAffiliateCode()
}
