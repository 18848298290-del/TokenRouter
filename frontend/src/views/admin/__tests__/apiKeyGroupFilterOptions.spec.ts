import { describe, it, expect } from 'vitest'
import { buildApiKeyGroupFilterOptions } from '../apiKeyGroupFilterOptions'
import type { AdminGroup } from '@/api/groups.types'

// apiKeyGroupFilterOptions.spec.ts checks grouping and identifier rules for API-key
// group filter options. Minimal group fixtures isolate status and exclusivity
// decisions, and assertions protect the all-groups clearing option, populated section
// headers, and distinct negative header values consumed by Select.

// Fixed section labels make grouping and sentinel assertions independent of translation
// loading in this focused helper suite.
const labels = {
  all: 'All',
  exclusive: 'Exclusive',
  public: 'Public',
  disabled: 'Disabled',
}

// g creates a minimal active public-group fixture with supplied overrides. Tests
// provide only fields relevant to option grouping, while the cast permits this focused
// fixture to satisfy the broader API record type.
function g(partial: Partial<AdminGroup>): AdminGroup {
  return {
    id: 0,
    name: '',
    status: 'active',
    is_exclusive: false,
    ...partial,
  } as AdminGroup
}

describe('buildApiKeyGroupFilterOptions', () => {
  // Active exclusive and public groups share one input list. The resulting choices must
  // contain the clearing option and correctly ordered section headers with their
  // respective group rows.
  it('partitions active groups into exclusive/public with headers', () => {
    const groups = [
      g({ id: 1, name: 'Excl', is_exclusive: true }),
      g({ id: 2, name: 'Pub', is_exclusive: false }),
    ]
    expect(buildApiKeyGroupFilterOptions(groups, labels)).toEqual([
      { value: null, label: 'All' },
      { value: -1, label: 'Exclusive', kind: 'group', disabled: true },
      { value: 1, label: 'Excl' },
      { value: -2, label: 'Public', kind: 'group', disabled: true },
      { value: 2, label: 'Pub' },
    ])
  })

  // A group fixture omits subscription-specific fields while remaining exclusive. Its
  // option must still appear in the exclusive section, with no subscription section
  // introduced by the helper.
  it('does not depend on upstream-only subscription_type fields', () => {
    const groups = [g({ id: 9, name: 'X', is_exclusive: true })]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    expect(opts).toContainEqual({ value: 9, label: 'X' })
    expect(opts.find((o) => o.label === 'Exclusive')).toBeDefined()
    expect(opts.find((o) => o.label === 'Subscription')).toBeUndefined()
  })

  // The input contains a public group and no exclusive groups. The result must include
  // the public header and omit headers for sections that have no rows.
  it('skips empty section headers', () => {
    const groups = [g({ id: 2, name: 'Pub', is_exclusive: false })]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    expect(opts.find((o) => o.label === 'Exclusive')).toBeUndefined()
    expect(opts.find((o) => o.label === 'Subscription')).toBeUndefined()
    expect(opts).toContainEqual({ value: -2, label: 'Public', kind: 'group', disabled: true })
  })

  // An inactive exclusive group appears beside an active exclusive group. The inactive
  // row must remain selectable in the disabled section after the active exclusive
  // section.
  it('places non-active groups in a separate disabled section (not omitted)', () => {
    const groups = [
      g({ id: 1, name: 'Active', is_exclusive: true }),
      g({ id: 2, name: 'Inactive', is_exclusive: true, status: 'inactive' }),
    ]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    expect(opts).toContainEqual({ value: 1, label: 'Active' })
    expect(opts).toContainEqual({ value: 2, label: 'Inactive' })
    expect(opts).toContainEqual({ value: -3, label: 'Disabled', kind: 'group', disabled: true })
    const exclIdx = opts.findIndex((o) => o.value === -1)
    const disabledItemIdx = opts.findIndex((o) => o.value === 2)
    expect(exclIdx).toBeLessThan(disabledItemIdx)
  })

  // All three group sections are populated in one result. Their headers must use unique
  // negative identifiers so Select can assign distinct keys to each heading.
  it('section headers use distinct negative values (no duplicate Vue :key)', () => {
    const groups = [
      g({ id: 1, name: 'E', is_exclusive: true }),
      g({ id: 2, name: 'P', is_exclusive: false }),
      g({ id: 4, name: 'D', status: 'inactive' }),
    ]
    const opts = buildApiKeyGroupFilterOptions(groups, labels)
    const headerValues = opts.filter((o) => o.kind === 'group').map((o) => o.value)
    const unique = new Set(headerValues)
    expect(unique.size).toBe(headerValues.length)
    headerValues.forEach((v) => expect(v).toBeLessThan(0))
  })

  // The endpoint supplies no groups for the filter control. The result must still offer
  // the null-valued all-groups option so the existing filter can be cleared.
  it('returns only the all-option when there are no groups', () => {
    expect(buildApiKeyGroupFilterOptions([], labels)).toEqual([{ value: null, label: 'All' }])
  })
})
