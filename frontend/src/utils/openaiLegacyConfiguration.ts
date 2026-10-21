import type { OpenAICompactMode } from '@/api/admin/providers.types'

/**
 * openaiLegacyConfiguration.ts normalizes retired OpenAI provider settings at
 * import and editing boundaries. Obsolete probe fields are removed from a
 * shallow copy, while existing compact-mode keys become explicit on or off
 * values. Missing switches remain absent and unrelated nested configuration
 * retains its original references.
 */

/**
 * normalizeOpenAICompactMode preserves a normalized force_off string and
 * otherwise returns force_on. Whitespace and case are ignored for the
 * recognized off value; missing or legacy automatic values select the on mode.
 */
export function normalizeOpenAICompactMode(value: unknown): OpenAICompactMode {
  return typeof value === 'string' && value.trim().toLowerCase() === 'force_off'
    ? 'force_off'
    : 'force_on'
}

/**
 * normalizeLegacyOpenAIExtra shallow-copies provider extra and removes retired
 * probe state before submission. Only present compact-mode fields are
 * normalized, leaving missing switches absent; unrelated nested objects remain
 * shared with the input.
 */
export function normalizeLegacyOpenAIExtra(
  extra: Record<string, unknown>,
): Record<string, unknown> {
  /**
   * Copy the outer extra object before deleting retired fields so callers can
   * compare or retain their original draft while using normalized submission
   * data.
   */
  const normalized = { ...extra }
  for (const key of [
    'openai_responses_probe_status',
    'openai_responses_supported',
    'openai_compact_supported',
    'openai_compact_checked_at',
    'openai_compact_last_status',
    'openai_compact_last_error',
    'openai_native_compaction_v2_supported',
    'openai_native_compaction_v2_checked_at',
    'openai_native_compaction_v2_last_status',
    'openai_native_compaction_v2_last_error',
  ]) {
    delete normalized[key]
  }
  for (const key of ['openai_compact_mode', 'openai_native_compaction_v2_mode']) {
    if (Object.prototype.hasOwnProperty.call(normalized, key)) {
      normalized[key] = normalizeOpenAICompactMode(normalized[key])
    }
  }
  return normalized
}
