import type { Provider } from '@/api/admin/providers.types'

/**
 * providerUsageRefresh.ts builds comparison strings for provider usage widgets
 * from the fields that affect their displayed snapshots. OpenAI OAuth keys
 * include provider identity, timestamps, pause state, and selected Codex
 * counters. Other OpenAI credential types return an empty key.
 *
 * Grok snapshots require structural normalization because equivalent objects
 * can arrive with different property order. Recursive sorting omits undefined
 * object entries while retaining array order. A legacy quota snapshot
 * participates only when the canonical usage snapshot has no nonblank
 * subscription tier. These strings detect presentation changes; they do not
 * authenticate a provider, perform requests, or replace the separate upstream
 * usage cache identity.
 */

/**
 * normalizeUsageRefreshValue converts a comparison field to text, treating
 * null and undefined as empty. Other values retain their ordinary String
 * representation, including false and numeric zero.
 */
const normalizeUsageRefreshValue = (value: unknown): string => {
  if (value == null) return ''
  return String(value)
}

/**
 * normalizeSnapshotRefreshValue produces a recursively normalized snapshot for
 * deterministic JSON serialization. Arrays retain element order, while object
 * properties are sorted and undefined object entries are omitted.
 *
 * The result separates presentation changes from insertion-order differences
 * in server snapshots. Objects and arrays receive new containers, but
 * primitive values retain their values. Inputs are expected to be acyclic
 * JSON-like snapshots; this routine does not preserve prototypes or support
 * circular object graphs.
 */
const normalizeSnapshotRefreshValue = (value: unknown): unknown => {
  if (Array.isArray(value)) {
    return value.map(normalizeSnapshotRefreshValue)
  }
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      /**
       * Normalize object key order recursively before serialization so equivalent
       * snapshot payloads do not refresh widgets merely because the server reordered
       * fields.
       */
      Object.entries(value as Record<string, unknown>)
        .filter(([, entry]) => entry !== undefined)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, entry]) => [key, normalizeSnapshotRefreshValue(entry)]),
    )
  }
  return value
}

/**
 * serializeSnapshotRefreshValue serializes normalized snapshot data for
 * comparison keys. Null and undefined become empty strings; other unsupported
 * JSON values follow JSON.stringify behavior, and serialization errors
 * propagate to the caller.
 */
const serializeSnapshotRefreshValue = (value: unknown): string => {
  if (value == null) return ''
  /**
   * Normalize object representation before JSON serialization so object
   * insertion order does not become a false usage-change signal for the
   * comparing widget.
   */
  return JSON.stringify(normalizeSnapshotRefreshValue(value)) ?? ''
}

/**
 * isNonBlankString reports whether a value contains non-whitespace string
 * content.
 */
const isNonBlankString = (value: unknown): value is string =>
  typeof value === 'string' && value.trim().length > 0

/**
 * buildOpenAIUsageRefreshKey combines the OpenAI OAuth fields that trigger
 * usage display refreshes. Other platform or credential combinations return an
 * empty string; nullish field values contribute empty segments in the fixed
 * comparison order.
 */
export const buildOpenAIUsageRefreshKey = (
  provider: Pick<
    Provider,
    | 'id'
    | 'platform'
    | 'type'
    | 'updated_at'
    | 'last_used_at'
    | 'rate_limit_reset_at'
    | 'quota_auto_paused'
    | 'extra'
  >,
): string => {
  if (provider.platform !== 'openai' || provider.type !== 'oauth') {
    return ''
  }

  const extra = provider.extra ?? {}
  return [
    provider.id,
    provider.updated_at,
    provider.last_used_at,
    provider.rate_limit_reset_at,
    provider.quota_auto_paused,
    extra.codex_usage_updated_at,
    extra.codex_5h_used_percent,
    extra.codex_5h_reset_at,
    extra.codex_5h_reset_after_seconds,
    extra.codex_5h_window_minutes,
    extra.codex_7d_used_percent,
    extra.codex_7d_reset_at,
    extra.codex_7d_reset_after_seconds,
    extra.codex_7d_window_minutes,
  ]
    .map(normalizeUsageRefreshValue)
    .join('|')
}

/**
 * buildGrokUsageRefreshKey combines the billing and usage snapshots that
 * determine Grok presentation. Non-Grok providers return an empty string.
 *
 * A nonblank canonical subscription tier makes the legacy quota snapshot
 * irrelevant to this comparison. When that tier is absent or blank, legacy
 * changes still trigger refreshes. Each included snapshot is normalized
 * independently before joining, so property insertion order does not cause
 * extra refreshes while array order and actual snapshot values remain
 * observable.
 */
export const buildGrokUsageRefreshKey = (
  provider: Pick<Provider, 'platform' | 'extra'>,
): string => {
  if (provider.platform !== 'grok') {
    return ''
  }

  const extra = provider.extra ?? {}
  const usageSnapshot = extra.grok_usage_snapshot
  const canonicalTier = (usageSnapshot as Record<string, unknown> | null | undefined)
    ?.subscription_tier
  /**
   * Only a usable canonical subscription tier shadows the legacy tier; partial
   * canonical snapshots still need legacy changes to refresh the displayed plan.
   */
  const legacyQuotaFallback = isNonBlankString(canonicalTier)
    ? undefined
    : extra.grok_quota_snapshot
  return [
    serializeSnapshotRefreshValue(extra.grok_billing_snapshot),
    serializeSnapshotRefreshValue(usageSnapshot),
    serializeSnapshotRefreshValue(legacyQuotaFallback),
  ].join('|')
}
