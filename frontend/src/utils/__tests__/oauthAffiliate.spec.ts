import { beforeEach, describe, expect, it, vi } from 'vitest'
import {
  clearAffiliateCode,
  clearOAuthAffiliateCode,
  loadAffiliateCode,
  loadOAuthAffiliateCode,
  resolveAffiliateCode,
  storeAffiliateCode,
  storeOAuthAffiliateCode,
} from '@/utils/oauthAffiliate'

/**
 * oauthAffiliate.spec.ts checks referral persistence across registration and
 * OAuth handoff storage. Controlled timestamps verify expiry cleanup, and
 * separate keys demonstrate that clearing the tab-local handoff does not
 * remove the persistent referral used by later navigation.
 */

describe('oauthAffiliate', () => {
  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.useRealTimers()
  })

  /**
   * An explicit referral contains surrounding whitespace and is resolved before
   * later reads. The normalized code must persist and remain the fallback when
   * no new value is supplied.
   */
  it('persists affiliate code across pages', () => {
    expect(resolveAffiliateCode(' 5579J7CFG9PF ')).toBe('5579J7CFG9PF')
    expect(loadAffiliateCode()).toBe('5579J7CFG9PF')
    expect(resolveAffiliateCode()).toBe('5579J7CFG9PF')
  })

  /**
   * A persistent referral is read immediately before and after its thirty-day
   * expiry. The expired read must return empty and remove the old localStorage
   * entry.
   */
  it('expires stale affiliate code', () => {
    const now = Date.UTC(2026, 0, 1)
    storeAffiliateCode('AFF123', now)

    expect(loadAffiliateCode(now + 30 * 24 * 60 * 60 * 1000 - 1)).toBe('AFF123')
    expect(loadAffiliateCode(now + 30 * 24 * 60 * 60 * 1000 + 1)).toBe('')
    expect(localStorage.getItem('affiliate_aff_code')).toBeNull()
  })

  /**
   * Persistent and OAuth handoff codes contain different values in separate
   * stores. Clearing either channel must preserve the other until its own
   * cleanup is requested.
   */
  it('keeps oauth transient code separate from persistent affiliate code', () => {
    storeAffiliateCode('PERSISTED')
    storeOAuthAffiliateCode('OAUTH')

    expect(loadAffiliateCode()).toBe('PERSISTED')
    expect(loadOAuthAffiliateCode()).toBe('OAUTH')

    clearOAuthAffiliateCode()
    expect(loadOAuthAffiliateCode()).toBe('')
    expect(loadAffiliateCode()).toBe('PERSISTED')

    clearAffiliateCode()
    expect(loadAffiliateCode()).toBe('')
  })
})
