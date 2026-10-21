import type { ReasoningEffortMapping, ReasoningEffortMatchType } from '@/api/groups.types'

// groupsReasoningEffort.ts converts group reasoning controls between API mappings and editable
// groups of effort pairs. Draft rows and pairs receive local identifiers so validation messages
// stay attached to the edited item. Supported effort values differ between ceiling controls and
// mappings, where none is also accepted.
//
// Mappings with the same normalized model scope are grouped together during hydration, and
// serialization emits first-seen scopes with the original pair order within each scope.
// Interleaved scopes therefore become contiguous. Scope comparison ignores surrounding
// whitespace and letter case, while a nonempty model with no valid match type uses exact
// matching. Validation reports duplicate scopes and duplicate sources against every affected
// row or pair.

// openAIReasoningEffortValues defines ceiling choices, while mapping choices additionally
// include none for explicit source and destination mappings.
const openAIReasoningEffortValues = ['minimal', 'low', 'medium', 'high', 'xhigh', 'max'] as const

// Mapping choices include the shared effort vocabulary; individual provider executors still
// validate whether they support the selected effort.
const openAIReasoningEffortMappingValues = ['none', ...openAIReasoningEffortValues] as const

// reasoningEffortOverLimitDowngrade and reasoningEffortOverLimitDeny identify the normalized
// actions offered when a request exceeds the group ceiling.
export const reasoningEffortOverLimitDowngrade = 'downgrade'
export const reasoningEffortOverLimitDeny = 'deny'

// reasoningEffortMatchTypes enumerates supported model-scope comparisons used by normalization
// and validation of saved mapping rules.
const reasoningEffortMatchTypes: readonly ReasoningEffortMatchType[] = ['exact', 'prefix', 'suffix']

// reasoningEffortOptionsForPlatform returns the supported reasoning ceiling values as selector
// options.
export function reasoningEffortOptionsForPlatform() {
  return openAIReasoningEffortValues.map((value) => ({ value, label: value }))
}
// reasoningEffortMappingOptionsForPlatform returns effort mapping choices including the none
// value.
export function reasoningEffortMappingOptionsForPlatform() {
  return openAIReasoningEffortMappingValues.map((value) => ({ value, label: value }))
}
// normalizeReasoningEffortForPlatform trims and lowercases a supported reasoning ceiling value.
// Missing or unsupported values become an empty string so the form can display its unconfigured
// state.
export function normalizeReasoningEffortForPlatform(value: string | null | undefined): string {
  const normalized = value?.trim().toLowerCase() ?? ''
  return openAIReasoningEffortValues.some((allowed) => allowed === normalized) ? normalized : ''
}
// normalizeReasoningEffortMappingForPlatform trims and lowercases a supported mapping effort
// value. Mapping choices include none, while missing or unsupported values return an empty
// string for validation and hydration.
export function normalizeReasoningEffortMappingForPlatform(
  value: string | null | undefined,
): string {
  const normalized = value?.trim().toLowerCase() ?? ''
  return openAIReasoningEffortMappingValues.some((allowed) => allowed === normalized)
    ? normalized
    : ''
}

// normalizeReasoningEffortOverLimit selects the action used when a request exceeds the ceiling.
// A normalized deny value remains deny; every other input uses the downgrade default consumed
// by the form.
export function normalizeReasoningEffortOverLimit(value: string | null | undefined): string {
  return value?.trim().toLowerCase() === reasoningEffortOverLimitDeny
    ? reasoningEffortOverLimitDeny
    : reasoningEffortOverLimitDowngrade
}

// normalizeReasoningEffortMatchType returns a supported normalized model matching mode. Exact,
// prefix, and suffix are accepted; missing or unknown values become an empty string for
// subsequent defaulting or validation.
export function normalizeReasoningEffortMatchType(
  value: string | null | undefined,
): ReasoningEffortMatchType | '' {
  const normalized = value?.trim().toLowerCase() ?? ''
  return reasoningEffortMatchTypes.some((allowed) => allowed === normalized)
    ? (normalized as ReasoningEffortMatchType)
    : ''
}

// ReasoningEffortMappingPair identifies one editable source-to-destination reasoning effort
// mapping.
export interface ReasoningEffortMappingPair {
  id: string // stable record identifier
  from: string // requested reasoning effort
  to: string // replacement reasoning effort
}

// ReasoningEffortMappingRow groups editable effort pairs under one model matching scope.
export interface ReasoningEffortMappingRow {
  id: string // stable record identifier
  // model and match_type jointly select the mapping scope. An empty trimmed model is global;
  // for a nonempty model, scope comparison and serialization use exact when match_type is
  // absent or invalid.
  match_type: ReasoningEffortMatchType | '' // model scope matching mode
  model: string // reference model identifier
  pairs: ReasoningEffortMappingPair[] // ordered reasoning effort pairs
}

// ReasoningEffortMappingErrorCode identifies localized validation failures for mapping rows and
// pairs.
export type ReasoningEffortMappingErrorCode =
  | 'fromRequired'
  | 'toRequired'
  | 'duplicateFrom'
  | 'duplicateScope'
  | 'unsupportedFrom'
  | 'unsupportedTo'
  | 'unsupportedMatchType'

// ReasoningEffortMappingErrors indexes field-specific validation codes by local row or pair
// identifier.
export type ReasoningEffortMappingErrors = Record<
  string,
  Partial<Record<'from' | 'to' | 'match_type' | 'duplicateScope', ReasoningEffortMappingErrorCode>>
>

// nextMappingRowID allocates local identifiers shared by mapping groups and pairs so validation
// errors can address individual drafts.
let nextMappingRowID = 0

// createReasoningEffortMappingPair allocates an identified editable effort pair. Missing source
// or destination values become empty strings, and the module counter gives each new draft item
// a distinct local identifier.
export function createReasoningEffortMappingPair(
  pair: Partial<Pick<ReasoningEffortMapping, 'from' | 'to'>> = {},
): ReasoningEffortMappingPair {
  nextMappingRowID += 1
  return {
    id: `reasoning-effort-pair-${nextMappingRowID}`,
    from: pair.from ?? '',
    to: pair.to ?? '',
  }
}

// createReasoningEffortMappingRow allocates an identified model scope containing one effort
// pair. It trims the model, normalizes its matching mode, and fills omitted effort values
// through the pair constructor.
export function createReasoningEffortMappingRow(
  mapping: Partial<Omit<ReasoningEffortMapping, 'match_type'>> & {
    match_type?: ReasoningEffortMatchType | '' // model scope matching mode
  } = {},
): ReasoningEffortMappingRow {
  nextMappingRowID += 1
  return {
    id: `reasoning-effort-group-${nextMappingRowID}`,
    match_type: normalizeReasoningEffortMatchType(mapping.match_type),
    model: mapping.model?.trim() ?? '',
    pairs: [
      createReasoningEffortMappingPair({
        from: mapping.from,
        to: mapping.to,
      }),
    ],
  }
}

// mappingScopeKey combines a normalized matching mode and model into a comparison key. Empty
// models share the global scope; nonempty models use exact matching when their supplied mode is
// unsupported or absent.
function mappingScopeKey(
  matchType: string | null | undefined,
  model: string | null | undefined,
): string {
  const trimmedModel = model?.trim().toLowerCase() ?? ''
  // A model-less row is global regardless of its selector, while a concrete model with no
  // recognized mode uses exact comparison for scope identity.
  const normalizedType = trimmedModel ? normalizeReasoningEffortMatchType(matchType) || 'exact' : ''
  return `${normalizedType}\0${trimmedModel}`
}

// reasoningEffortMappingsToRows groups API mappings into editable model scopes. Missing input
// yields an empty list, and entries with unsupported source or destination efforts are skipped.
// Scope comparison normalizes model case and uses the default exact mode for nonempty models.
//
// The first mapping creates the row and retains its trimmed model spelling; later mappings with
// the same scope append fresh identified pairs. This keeps API ordering inside each group and
// gives validation stable local keys for every editable item.
export function reasoningEffortMappingsToRows(
  mappings?: ReasoningEffortMapping[] | null,
): ReasoningEffortMappingRow[] {
  const groups: ReasoningEffortMappingRow[] = []
  const indexByScope = new Map<string, number>()

  ;(mappings ?? []).forEach((mapping) => {
    const from = normalizeReasoningEffortMappingForPlatform(mapping.from)
    const to = normalizeReasoningEffortMappingForPlatform(mapping.to)
    // Stored unsupported effort values are omitted during hydration so the editor builds
    // choices from the supported mapping vocabulary used by validation.
    if (!from || !to) return

    const matchType = normalizeReasoningEffortMatchType(mapping.match_type)
    const model = mapping.model?.trim() ?? ''
    const scope = mappingScopeKey(matchType, model)
    const existingIndex = indexByScope.get(scope)
    if (existingIndex !== undefined) {
      groups[existingIndex].pairs.push(createReasoningEffortMappingPair({ from, to }))
      return
    }

    const group = createReasoningEffortMappingRow({
      from,
      to,
      match_type: matchType,
      model,
    })
    indexByScope.set(scope, groups.length)
    groups.push(group)
  })

  return groups
}

// reasoningEffortMappingsToAPI flattens draft groups into ordered API mappings. Source and
// destination values are trimmed, and nonempty models include normalized matching modes with
// exact as the default. Global mappings omit model and match_type fields.
//
// Local row and pair identifiers serve the editor and are excluded from the API representation.
// Serialization leaves validation to the caller, allowing the form to report invalid or
// duplicate entries against their draft identifiers before submission.
export function reasoningEffortMappingsToAPI(
  rows: ReasoningEffortMappingRow[],
): ReasoningEffortMapping[] {
  return rows.flatMap((row) => {
    const model = row.model.trim()
    return row.pairs.map((pair) => {
      const mapping: ReasoningEffortMapping = {
        from: pair.from.trim(),
        to: pair.to.trim(),
      }
      if (model) {
        mapping.match_type = normalizeReasoningEffortMatchType(row.match_type) || 'exact'
        mapping.model = model
      }
      return mapping
    })
  })
}

// validateReasoningEffortMappings returns field errors indexed by draft row and pair IDs. It
// checks required and supported effort values, unsupported matching modes, repeated scopes, and
// repeated source efforts within a normalized scope. Empty error records indicate that these
// checks passed.
//
// Scope and source indexes collect every participant before duplicate errors are assigned, so
// both sides of a duplicate are marked. Model scopes and source comparisons ignore case, while
// the returned errors keep the original local identifiers used by the fields.
export function validateReasoningEffortMappings(
  rows: ReasoningEffortMappingRow[],
): ReasoningEffortMappingErrors {
  const errors: ReasoningEffortMappingErrors = {}
  const scopeGroups = new Map<string, ReasoningEffortMappingRow[]>()
  const sourcePairs = new Map<string, ReasoningEffortMappingPair[]>()

  rows.forEach((row) => {
    const rawMatchType = row.match_type.trim().toLowerCase()
    const matchType = normalizeReasoningEffortMatchType(row.match_type)
    if (rawMatchType && !matchType) {
      errors[row.id] = { ...errors[row.id], match_type: 'unsupportedMatchType' }
    }

    const scope = mappingScopeKey(row.match_type, row.model)
    scopeGroups.set(scope, [...(scopeGroups.get(scope) ?? []), row])

    row.pairs.forEach((pair) => {
      const from = pair.from.trim()
      const to = pair.to.trim()
      if (!from) {
        errors[pair.id] = { ...errors[pair.id], from: 'fromRequired' }
      } else if (!normalizeReasoningEffortMappingForPlatform(from)) {
        errors[pair.id] = { ...errors[pair.id], from: 'unsupportedFrom' }
      } else {
        const key = `${scope}\0${from.toLowerCase()}`
        sourcePairs.set(key, [...(sourcePairs.get(key) ?? []), pair])
      }
      if (!to) {
        errors[pair.id] = { ...errors[pair.id], to: 'toRequired' }
      } else if (!normalizeReasoningEffortMappingForPlatform(to)) {
        errors[pair.id] = { ...errors[pair.id], to: 'unsupportedTo' }
      }
    })
  })

  scopeGroups.forEach((duplicateGroups) => {
    if (duplicateGroups.length < 2) return
    duplicateGroups.forEach((row) => {
      errors[row.id] = { ...errors[row.id], duplicateScope: 'duplicateScope' }
    })
  })

  sourcePairs.forEach((duplicatePairs) => {
    if (duplicatePairs.length < 2) return
    duplicatePairs.forEach((pair) => {
      errors[pair.id] = { ...errors[pair.id], from: 'duplicateFrom' }
    })
  })

  return errors
}
