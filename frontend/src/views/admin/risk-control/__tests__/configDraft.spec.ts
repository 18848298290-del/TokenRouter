import { describe, expect, it } from 'vitest'
import {
  buildRiskControlConfigRequest,
  createRiskControlDraft,
  parseApiKeyEntries,
  parseBlockedKeywords,
  riskThresholdsFromConfig,
} from '../configDraft'

/**
 * configDraft.spec.ts checks the editor-to-request conversions used by risk
 * settings. The cases focus on values whose representation differs between
 * text controls, sliders, and API fields, including credential precedence,
 * collection ownership, and bounded request values.
 */

describe('risk-control configuration drafts', () => {
  // A user can enter the same credential multiple times with changing metadata.
  // Its first position survives while its last priority and full note are submitted.
  it('keeps first credential order and last metadata', () => {
    expect(parseApiKeyEntries('first | 2 | old\nsecond\nfirst | 0 | new | note')).toEqual([
      { api_key: 'first', priority: 100, note: 'new | note' },
      { api_key: 'second', priority: 100, note: '' },
    ])
  })

  // A saved request must own the editable collections that the administrator can change.
  // This case also checks model spelling, ratio precision, numeric limits, and proxy clearing.
  it('converts fields and copies mutable request lists', () => {
    const draft = createRiskControlDraft()
    Object.assign(draft, {
      all_groups: false,
      group_ids: [7],
      model_filter_type: 'include',
      model_filter_models: [' GPT-5 ', 'gpt-5', 'gpt-4'],
      thresholds: { sexual: 72.1234, harassment: Number.NaN },
      non_hit_retention_days: 9,
      audit_user_text_max_chars: -5,
      audit_tool_output_max_chars: 2000000,
    })
    const deleted = ['old']
    const result = buildRiskControlConfigRequest(draft, deleted, {})
    expect('payload' in result).toBe(true)
    if (!('payload' in result)) throw new Error('Expected valid request')
    expect(result.payload).toMatchObject({
      proxy_id: 0,
      group_ids: [7],
      delete_api_key_hashes: ['old'],
      model_filter: { type: 'include', models: ['GPT-5', 'gpt-4'] },
      thresholds: { sexual: 0.7212, harassment: 0 },
      non_hit_retention_days: 3,
      audit_user_text_max_chars: 1,
      audit_tool_output_max_chars: 1000000,
    })
    draft.group_ids.push(8)
    draft.model_filter_models.push('later')
    deleted.push('later')
    expect(result.payload.group_ids).toEqual([7])
    expect(result.payload.model_filter?.models).toEqual(['GPT-5', 'gpt-4'])
    expect(result.payload.delete_api_key_hashes).toEqual(['old'])
  })

  // Stored threshold responses can omit categories or include values outside slider bounds.
  // Loading retains category defaults, clamps finite ratios, and ignores unknown categories.
  it('loads finite known thresholds over defaults', () => {
    expect(
      riskThresholdsFromConfig({ sexual: 2, harassment: Number.NaN, unknown: 0.5 }),
    ).toMatchObject({
      sexual: 100,
      harassment: 98,
    })
    expect(riskThresholdsFromConfig({ unknown: 0.5 })).not.toHaveProperty('unknown')
  })

  // Keyword identity is evaluated before the stored text is shortened to its limit.
  // Distinct long lines may therefore produce matching truncated strings in the request.
  it('deduplicates complete keywords before truncating them', () => {
    const prefix = 'x'.repeat(200)
    expect(parseBlockedKeywords(` Word \nword\n${prefix}a\n${prefix}b`)).toEqual([
      'Word',
      prefix,
      prefix,
    ])
  })

  // Each page starts with a separate mutable threshold map and group selection.
  // Editing one draft cannot change the defaults supplied to a later page instance.
  it('allocates independent initial drafts', () => {
    const first = createRiskControlDraft()
    const second = createRiskControlDraft()
    first.thresholds.sexual = 10
    first.group_ids.push(1)
    expect(second.thresholds.sexual).toBe(65)
    expect(second.group_ids).toEqual([])
  })
})
