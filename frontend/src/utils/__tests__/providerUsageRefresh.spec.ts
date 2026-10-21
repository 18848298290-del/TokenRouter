import { describe, expect, it } from 'vitest'
import { buildGrokUsageRefreshKey, buildOpenAIUsageRefreshKey } from '../providerUsageRefresh'

/**
 * providerUsageRefresh.spec.ts checks the comparison keys used to refresh
 * provider usage presentation. OpenAI cases cover counter and state changes,
 * while Grok cases compare canonical snapshots, object ordering, and legacy
 * tier fallbacks so irrelevant differences do not trigger refresh.
 */

describe('buildOpenAIUsageRefreshKey', () => {
  /**
   * An OpenAI OAuth snapshot changes its observation timestamp and usage
   * percentage. The comparison key must change so the usage display can observe
   * the new snapshot.
   */
  it('会在 codex 快照变化时生成不同 key', () => {
    const base = {
      id: 1,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T09:59:00Z',
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 0,
        codex_7d_used_percent: 0,
      },
    } as any

    const next = {
      ...base,
      extra: {
        ...base.extra,
        codex_usage_updated_at: '2026-03-07T10:01:00Z',
        codex_5h_used_percent: 100,
      },
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  /**
   * An OpenAI OAuth provider receives a new last-used timestamp without changing
   * its snapshot. The comparison key must change because activity affects usage
   * refresh decisions.
   */
  it('会在 last_used_at 变化时生成不同 key', () => {
    const base = {
      id: 3,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T10:00:00Z',
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 12,
        codex_7d_used_percent: 24,
      },
    } as any

    const next = {
      ...base,
      last_used_at: '2026-03-07T10:02:00Z',
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  /**
   * An OpenAI OAuth provider changes its derived automatic-pause state only. The
   * comparison key must reflect that state transition even while the underlying
   * snapshot remains unchanged.
   */
  it('会在自动暂停派生状态变化时生成不同 key', () => {
    const base = {
      id: 4,
      platform: 'openai',
      type: 'oauth',
      updated_at: '2026-03-07T10:00:00Z',
      last_used_at: '2026-03-07T10:00:00Z',
      quota_auto_paused: true,
      extra: {
        codex_usage_updated_at: '2026-03-07T10:00:00Z',
        codex_5h_used_percent: 99,
        codex_7d_used_percent: 20,
      },
    } as any

    const next = {
      ...base,
      quota_auto_paused: false,
    }

    expect(buildOpenAIUsageRefreshKey(base)).not.toBe(buildOpenAIUsageRefreshKey(next))
  })

  /**
   * A provider uses Anthropic OAuth instead of OpenAI OAuth credentials. The
   * OpenAI refresh helper must return an empty key rather than tracking
   * unrelated provider state.
   */
  it('非 OpenAI OAuth 提供商返回空 key', () => {
    expect(
      buildOpenAIUsageRefreshKey({
        id: 2,
        platform: 'anthropic',
        type: 'oauth',
        updated_at: '2026-03-07T10:00:00Z',
        last_used_at: '2026-03-07T10:00:00Z',
        extra: {},
      } as any),
    ).toBe('')
  })
})

describe('buildGrokUsageRefreshKey', () => {
  /**
   * Canonical Grok billing and usage snapshots change in separate comparisons.
   * Either change must produce a different key so both displayed information
   * sources can trigger refresh.
   */
  it('changes when a canonical Grok billing or usage snapshot changes', () => {
    const base = {
      platform: 'grok',
      extra: {
        grok_billing_snapshot: { plan: 'Free', usage_percent: 0 },
        grok_usage_snapshot: { subscription_tier: 'Free', status_code: 200 },
      },
    } as any

    expect(buildGrokUsageRefreshKey(base)).not.toBe(
      buildGrokUsageRefreshKey({
        ...base,
        extra: {
          ...base.extra,
          grok_billing_snapshot: { plan: 'SuperGrok', usage_percent: 0 },
        },
      }),
    )
    expect(buildGrokUsageRefreshKey(base)).not.toBe(
      buildGrokUsageRefreshKey({
        ...base,
        extra: {
          ...base.extra,
          grok_usage_snapshot: { subscription_tier: 'SuperGrok', status_code: 200 },
        },
      }),
    )
  })

  /**
   * Equivalent Grok snapshots reorder object properties while changing a
   * shadowed legacy tier. Their keys must remain equal because canonical tier
   * data already determines presentation.
   */
  it('ignores object key order and a legacy alias shadowed by canonical usage', () => {
    const first = {
      platform: 'grok',
      extra: {
        grok_billing_snapshot: {
          plan: 'SuperGrok',
          limits: { monthly: 100, weekly: 25 },
        },
        grok_usage_snapshot: { status_code: 200, subscription_tier: 'SuperGrok' },
        grok_quota_snapshot: { subscription_tier: 'Free' },
      },
    } as any
    const reordered = {
      platform: 'grok',
      extra: {
        grok_quota_snapshot: { subscription_tier: 'SuperGrok Heavy' },
        grok_usage_snapshot: { subscription_tier: 'SuperGrok', status_code: 200 },
        grok_billing_snapshot: {
          limits: { weekly: 25, monthly: 100 },
          plan: 'SuperGrok',
        },
      },
    } as any

    expect(buildGrokUsageRefreshKey(first)).toBe(buildGrokUsageRefreshKey(reordered))
  })

  /**
   * Grok has no canonical usage snapshot and its legacy quota tier changes. The
   * comparison key must change so the fallback tier remains observable by the
   * widget.
   */
  it('uses the legacy quota alias only when the canonical snapshot is absent', () => {
    const base = {
      platform: 'grok',
      extra: { grok_quota_snapshot: { subscription_tier: 'Free' } },
    } as any
    const next = {
      platform: 'grok',
      extra: { grok_quota_snapshot: { subscription_tier: 'SuperGrok' } },
    } as any

    expect(buildGrokUsageRefreshKey(base)).not.toBe(buildGrokUsageRefreshKey(next))
  })

  /**
   * Canonical Grok snapshots exist but have missing or blank tier fields. Legacy
   * quota tier changes must still alter the key because canonical data cannot
   * supply a usable tier.
   */
  it('tracks the legacy tier when the canonical snapshot has no usable tier', () => {
    for (const canonicalSnapshot of [
      { status_code: 200 },
      { status_code: 200, subscription_tier: '   ' },
    ]) {
      const base = {
        platform: 'grok',
        extra: {
          grok_usage_snapshot: canonicalSnapshot,
          grok_quota_snapshot: { subscription_tier: 'Free' },
        },
      } as any
      const next = {
        ...base,
        extra: {
          ...base.extra,
          grok_quota_snapshot: { subscription_tier: 'SuperGrok' },
        },
      }

      expect(buildGrokUsageRefreshKey(base)).not.toBe(buildGrokUsageRefreshKey(next))
    }
  })

  /**
   * An OpenAI provider contains Grok-like extra data in the fixture. The Grok
   * helper must return an empty key because the platform does not qualify.
   */
  it('returns an empty key for non-Grok providers', () => {
    expect(
      buildGrokUsageRefreshKey({
        platform: 'openai',
        extra: { grok_usage_snapshot: { subscription_tier: 'SuperGrok' } },
      } as any),
    ).toBe('')
  })
})
