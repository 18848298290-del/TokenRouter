import type { AdminGroup } from '@/api/groups.types'

// apiKeyGroupFilterOptions.ts constructs grouped options for filtering users by their
// API keys' group identifiers. Active groups are separated into exclusive and public
// sections, while nonactive groups remain selectable under a disabled-group section so
// existing key bindings can still be located.
//
// Section headers use distinct negative sentinel identifiers because Select derives row
// keys from value type and value. Reusing null for every header would create duplicate
// keys; null is reserved for the leading option that clears filtering. Empty sections
// are omitted, and group rows retain their server identifiers and input order within
// each section.

// ApiKeyGroupFilterOption represents a selectable group or disabled section heading.
export interface ApiKeyGroupFilterOption {
  // value is null for clearing, a distinct negative sentinel for a heading, or the
  // server identifier for a group row.
  value: number | null // selected option value
  label: string // displayed option label
  // kind marks section headers so Select can render them as group labels; ordinary
  // selectable rows omit this presentation hint.
  kind?: 'group' // option presentation kind
  // disabled prevents selecting section headers; disabled-group records themselves
  // remain selectable so administrators can locate existing key bindings.
  disabled?: boolean // prevent option selection
}

// ApiKeyGroupFilterLabels supplies translated labels for the clearing option and group
// sections.
export interface ApiKeyGroupFilterLabels {
  all: string // clear-filter option label
  exclusive: string // exclusive section label
  public: string // public section label
  disabled: string // disabled section label
}

// Header sentinels distinguish section rows in Select keys; null remains reserved for
// the all-groups option that clears filtering.
const HEADER_EXCLUSIVE = -1 // exclusive section header
const HEADER_PUBLIC = -2 // public section header
const HEADER_DISABLED = -3 // disabled section header

// buildApiKeyGroupFilterOptions returns an all-groups option followed by populated
// exclusive, public, and disabled sections. Status takes precedence over exclusivity,
// so every nonactive group belongs to the final section. Each section header is
// disabled and receives a distinct negative value for Select's keyed rendering. Group
// options retain their numeric server identifiers, while the leading null option clears
// the filter and empty sections contribute no header.
export function buildApiKeyGroupFilterOptions(
  groups: AdminGroup[],
  labels: ApiKeyGroupFilterLabels,
): ApiKeyGroupFilterOption[] {
  const exclusive: ApiKeyGroupFilterOption[] = []
  const publicGroups: ApiKeyGroupFilterOption[] = []
  const disabledGroups: ApiKeyGroupFilterOption[] = []

  for (const grp of groups) {
    const item: ApiKeyGroupFilterOption = { value: grp.id, label: grp.name }
    // Classify nonactive groups before exclusivity so keys referencing disabled
    // groups remain findable in their dedicated final section regardless of group
    // access type.
    if (grp.status !== 'active') {
      disabledGroups.push(item)
    } else if (grp.is_exclusive) {
      exclusive.push(item)
    } else {
      publicGroups.push(item)
    }
  }

  const options: ApiKeyGroupFilterOption[] = [{ value: null, label: labels.all }]

  const sections: Array<[string, number, ApiKeyGroupFilterOption[]]> = [
    [labels.exclusive, HEADER_EXCLUSIVE, exclusive],
    [labels.public, HEADER_PUBLIC, publicGroups],
    [labels.disabled, HEADER_DISABLED, disabledGroups],
  ]
  for (const [label, headerValue, items] of sections) {
    if (items.length === 0) continue
    // Distinct negative header values keep Select row keys unique, while real groups
    // retain their server identifiers and the clearing option retains null.
    options.push({ value: headerValue, label, kind: 'group', disabled: true })
    options.push(...items)
  }
  return options
}
