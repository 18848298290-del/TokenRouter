import { describe, expect, it } from 'vitest'

import {
  createReasoningEffortMappingPair,
  createReasoningEffortMappingRow,
  normalizeReasoningEffortForPlatform,
  normalizeReasoningEffortMappingForPlatform,
  normalizeReasoningEffortMatchType,
  normalizeReasoningEffortOverLimit,
  reasoningEffortMappingOptionsForPlatform,
  reasoningEffortMappingsToAPI,
  reasoningEffortMappingsToRows,
  reasoningEffortOptionsForPlatform,
  validateReasoningEffortMappings,
} from '../groupsReasoningEffort'

// groupsReasoningEffort.spec.ts checks reasoning ceiling choices, scoped mapping conversion,
// and field errors through pure helpers. Draft groups retain model matching scope and local
// identities, while validation distinguishes duplicate scopes, repeated source efforts, and
// unsupported mapping values.

describe('groupsReasoningEffort', () => {
  // Ceiling and mapping selector options are requested from the shared helpers. The none effort
  // remains available for mappings while the maximum-effort control excludes that value.
  it('keeps none out of the max-effort choices while allowing it in mappings', () => {
    expect(reasoningEffortOptionsForPlatform().map((option) => option.value)).toEqual([
      'minimal',
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ])
    expect(reasoningEffortMappingOptionsForPlatform().map((option) => option.value)).toEqual([
      'none',
      'minimal',
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ])
  })

  // Model-scoped API mappings are hydrated and serialized through the draft representation.
  // Their scopes and effort pairs survive the round trip across the supplied provider examples.
  it('round-trips model-scoped mappings across providers', () => {
    expect(reasoningEffortOptionsForPlatform().map((option) => option.value)).toEqual([
      'minimal',
      'low',
      'medium',
      'high',
      'xhigh',
      'max',
    ])
    const rows = reasoningEffortMappingsToRows([
      { from: 'max', to: 'xhigh', match_type: 'prefix', model: 'claude-' },
      { from: 'minimal', to: 'low' },
      { from: 'none', to: 'low' },
    ])
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: 'max', to: 'xhigh', match_type: 'prefix', model: 'claude-' },
      { from: 'minimal', to: 'low' },
      { from: 'none', to: 'low' },
    ])
    expect(validateReasoningEffortMappings(rows)).toEqual({})
  })

  // Saved mappings contain supported effort pairs alongside stale custom values. Hydration
  // retains recognized pairs and drops invalid entries so the resulting draft uses the
  // supported choice set.
  it('hydrates supported rows and drops stale custom values', () => {
    const rows = reasoningEffortMappingsToRows([
      { from: ' max ', to: ' xhigh ' },
      { from: 'ultra', to: 'high' },
    ])

    expect(rows).toHaveLength(1)
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([{ from: 'max', to: 'xhigh' }])
  })

  // A stored mapping names a model and matching mode. Hydration creates a scope row whose model
  // conditions and effort pair match the API entry.
  it('hydrates model scoped mappings', () => {
    const rows = reasoningEffortMappingsToRows([
      {
        from: 'max',
        to: 'low',
        match_type: 'prefix',
        model: ' gpt ',
      },
      {
        from: 'max',
        to: 'medium',
        match_type: 'exact',
        model: 'gpt-5.4',
      },
    ])

    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: 'max', to: 'low', match_type: 'prefix', model: 'gpt' },
      { from: 'max', to: 'medium', match_type: 'exact', model: 'gpt-5.4' },
    ])
  })

  // Several API mappings use the same normalized scope and different source efforts. Hydration
  // groups them under one row while keeping each effort pair independently editable.
  it('groups multiple request mappings under the same type and model', () => {
    const rows = reasoningEffortMappingsToRows([
      { from: 'high', to: 'medium', match_type: 'prefix', model: 'gpt' },
      { from: 'xhigh', to: 'medium', match_type: 'prefix', model: 'GPT' },
    ])

    expect(rows).toHaveLength(1)
    expect(rows[0].match_type).toBe('prefix')
    expect(rows[0].model).toBe('gpt')
    expect(rows[0].pairs.map((pair) => pair.from)).toEqual(['high', 'xhigh'])
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: 'high', to: 'medium', match_type: 'prefix', model: 'gpt' },
      { from: 'xhigh', to: 'medium', match_type: 'prefix', model: 'gpt' },
    ])
  })

  // A draft mapping has no model scope when serialized. The API output omits model and
  // match-type fields so the mapping applies globally instead of carrying empty scope values.
  it('omits empty model scopes from the API payload', () => {
    const row = createReasoningEffortMappingRow({
      from: 'max',
      to: 'low',
      match_type: 'prefix',
    })
    expect(reasoningEffortMappingsToAPI([row])).toEqual([{ from: 'max', to: 'low' }])
  })

  // A saved suffix mapping is hydrated into an editable row. Its matching mode and model text
  // remain available for editing and subsequent serialization of that scope.
  it('hydrates suffix mappings', () => {
    const rows = reasoningEffortMappingsToRows([
      { from: 'max', to: 'low', match_type: 'suffix', model: ' mini ' },
    ])
    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: 'max', to: 'low', match_type: 'suffix', model: 'mini' },
    ])
  })

  // Ceiling and mapping normalizers receive none and unsupported values. None is accepted for
  // mappings, while invalid ceiling or mapping values become empty draft selections.
  it('keeps none limited to mappings and clears unsupported values', () => {
    expect(normalizeReasoningEffortForPlatform(' MAX ')).toBe('max')
    expect(normalizeReasoningEffortForPlatform('unsupported')).toBe('')
    expect(normalizeReasoningEffortForPlatform('none')).toBe('')
    expect(normalizeReasoningEffortMappingForPlatform(' none ')).toBe('none')
    expect(normalizeReasoningEffortMappingForPlatform('unsupported')).toBe('')
  })

  // Over-limit actions include deny, downgrade, and missing or unknown input. Normalization
  // retains deny and uses downgrade as the default for every unsupported action.
  it('normalizes the over-limit action with downgrade as the default', () => {
    expect(normalizeReasoningEffortOverLimit(' DENY ')).toBe('deny')
    expect(normalizeReasoningEffortOverLimit('block')).toBe('downgrade')
    expect(normalizeReasoningEffortOverLimit(' ')).toBe('downgrade')
  })

  // Matching-mode input varies in case, whitespace, and validity. Normalization recognizes
  // exact, prefix, and suffix and returns an empty string for unsupported scope modes.
  it('normalizes match type to exact, prefix, suffix, or empty', () => {
    expect(normalizeReasoningEffortMatchType('PREFIX')).toBe('prefix')
    expect(normalizeReasoningEffortMatchType('suffix')).toBe('suffix')
    expect(normalizeReasoningEffortMatchType('exact')).toBe('exact')
    expect(normalizeReasoningEffortMatchType('')).toBe('')
    expect(normalizeReasoningEffortMatchType('wildcard')).toBe('')
  })

  // A draft pair is missing one or both effort endpoints. Validation reports required-field
  // errors against the pair identifier so the matching controls can display them.
  it('requires both sides of every mapping', () => {
    const first = createReasoningEffortMappingRow({ to: 'low' })
    const second = createReasoningEffortMappingRow({ from: 'max' })

    expect(validateReasoningEffortMappings([first, second])).toEqual({
      [first.id]: { duplicateScope: 'duplicateScope' },
      [second.id]: { duplicateScope: 'duplicateScope' },
      [first.pairs[0].id]: { from: 'fromRequired' },
      [second.pairs[0].id]: { to: 'toRequired' },
    })
  })

  // Source efforts differ only in letter case within the same scope. Validation marks the
  // conflicting pair sources instead of allowing ambiguous case-insensitive mappings to be
  // saved.
  it('rejects duplicate source values case insensitively', () => {
    const first = createReasoningEffortMappingRow({ from: 'MAX', to: 'xhigh' })
    const second = createReasoningEffortMappingRow({ from: ' max ', to: 'high' })

    expect(validateReasoningEffortMappings([first, second])).toEqual({
      [first.id]: { duplicateScope: 'duplicateScope' },
      [second.id]: { duplicateScope: 'duplicateScope' },
      [first.pairs[0].id]: { from: 'duplicateFrom' },
      [second.pairs[0].id]: { from: 'duplicateFrom' },
    })
  })

  // The same source effort appears under different model scopes. Validation accepts those pairs
  // because their model conditions distinguish the requests to which each mapping applies.
  it('allows the same source across different model scopes', () => {
    const prefix = createReasoningEffortMappingRow({
      from: 'max',
      to: 'low',
      match_type: 'prefix',
      model: 'gpt',
    })
    const exact = createReasoningEffortMappingRow({
      from: 'max',
      to: 'medium',
      match_type: 'exact',
      model: 'gpt-5.4',
    })
    const global = createReasoningEffortMappingRow({ from: 'max', to: 'high' })

    expect(validateReasoningEffortMappings([prefix, exact, global])).toEqual({})
  })

  // One model scope contains several distinct source effort values. Validation accepts the
  // shared group and keeps all of its pairs available for independent destination choices.
  it('allows multiple request values in one model scope', () => {
    const row = createReasoningEffortMappingRow({
      from: 'high',
      to: 'medium',
      match_type: 'prefix',
      model: 'gpt',
    })
    row.pairs.push(createReasoningEffortMappingPair({ from: 'xhigh', to: 'medium' }))

    expect(validateReasoningEffortMappings([row])).toEqual({})
    expect(reasoningEffortMappingsToAPI([row])).toEqual([
      { from: 'high', to: 'medium', match_type: 'prefix', model: 'gpt' },
      { from: 'xhigh', to: 'medium', match_type: 'prefix', model: 'gpt' },
    ])
  })

  // A model scope repeats a source effort across its pairs. Validation assigns duplicate-source
  // errors to the affected pair identifiers so every conflict is visible in the editor.
  it('rejects duplicate source values within the same model scope', () => {
    const first = createReasoningEffortMappingRow({
      from: 'max',
      to: 'low',
      match_type: 'prefix',
      model: 'GPT',
    })
    const second = createReasoningEffortMappingRow({
      from: 'MAX',
      to: 'high',
      match_type: 'prefix',
      model: ' gpt ',
    })

    expect(validateReasoningEffortMappings([first, second])).toEqual({
      [first.id]: { duplicateScope: 'duplicateScope' },
      [second.id]: { duplicateScope: 'duplicateScope' },
      [first.pairs[0].id]: { from: 'duplicateFrom' },
      [second.pairs[0].id]: { from: 'duplicateFrom' },
    })
  })

  // A mapping leaves both type and model empty. Validation accepts the global scope, allowing
  // requests to use the mapping without a model-specific condition.
  it('allows empty type and model as a global mapping', () => {
    const row = createReasoningEffortMappingRow({
      from: 'max',
      to: 'low',
    })
    expect(row.match_type).toBe('')
    expect(row.model).toBe('')
    expect(validateReasoningEffortMappings([row])).toEqual({})
  })

  // A prefix matching mode is supplied with an empty model. Scope normalization treats that
  // entry as global so duplicate and validity checks use the effective matching behavior.
  it('treats prefix without a model as a global mapping', () => {
    const row = createReasoningEffortMappingRow({
      from: 'max',
      to: 'low',
      match_type: 'prefix',
    })
    expect(validateReasoningEffortMappings([row])).toEqual({})
    expect(reasoningEffortMappingsToAPI([row])).toEqual([{ from: 'max', to: 'low' }])
  })

  // A draft includes unsupported custom effort values. Validation returns the relevant
  // unsupported-field errors so the form cannot submit choices absent from the shared supported
  // list.
  it('rejects custom mappings', () => {
    const row = createReasoningEffortMappingRow({ from: 'ultra', to: 'high' })
    expect(validateReasoningEffortMappings([row])).toEqual({
      [row.pairs[0].id]: { from: 'unsupportedFrom' },
    })
  })

  // Saved mappings use none as an effort endpoint. Hydration retains that supported mapping
  // value even though the maximum-effort selector does not offer it.
  it('hydrates none mapping values', () => {
    const rows = reasoningEffortMappingsToRows([
      { from: 'none', to: 'low', match_type: 'exact', model: 'gpt-6-astra' },
    ])

    expect(reasoningEffortMappingsToAPI(rows)).toEqual([
      { from: 'none', to: 'low', match_type: 'exact', model: 'gpt-6-astra' },
    ])
  })
})
