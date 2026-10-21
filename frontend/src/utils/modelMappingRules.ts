/**
 * modelMappingRules.ts validates editable model mappings and converts between row and
 * record representations. Validation trims names for comparison while leaving draft
 * strings intact. Optional rules select required fields, code-point length limits,
 * wildcard restrictions, self-mapping checks, and duplicate-source detection.
 *
 * Duplicate sources depend on the complete row set, so the validator counts trimmed
 * sources before producing per-row issues. Each field retains its first issue in a
 * fixed precedence order, and the overall first issue follows row order with source
 * errors before target errors. Record conversion is separate from validation and lets
 * later duplicate keys replace earlier values.
 */

/**
 * ModelMappingRow retains the original source and target text in an editor.
 */
export interface ModelMappingRow {
  from: string // source model text
  to: string // target model text
}

/**
 * ModelMappingIssue names a model-redirect translation suffix for a validation failure.
 */
export type ModelMappingIssue =
  | 'sourceRequired' // source text is missing
  | 'targetRequired' // target text is missing
  | 'nameTooLong' // name exceeds code-point limit
  | 'sourceWildcardInvalid' // source wildcard placement invalid
  | 'targetWildcardInvalid' // target contains forbidden wildcard
  | 'selfMapping' // trimmed names are identical
  | 'duplicateSource' // trimmed source occurs repeatedly

/**
 * ModelMappingRowIssues holds the first source and target problem for one row.
 */
export interface ModelMappingRowIssues {
  /**
   * from and to contain each field's earliest enabled validation failure. A field
   * is omitted when its checks pass, and a valid row therefore has an empty object.
   */
  from?: ModelMappingIssue // first source issue
  to?: ModelMappingIssue // first target issue
}

/**
 * ModelMappingRuleOptions selects which checks the row validator should apply.
 */
export interface ModelMappingRuleOptions {
  /**
   * required, maxLength, sourceWildcard, forbidTargetWildcard, forbidSelfMapping,
   * and uniqueSource enable independent checks. Omitted options leave their checks
   * disabled, and each field reports its earliest enabled failure.
   */
  required?: boolean // require both names
  /**
   * maxLength counts Unicode code points after trimming each name; an omitted
   * value disables the length comparison for both fields.
   */
  maxLength?: number // maximum Unicode code points
  sourceWildcard?: 'trailing' // trailing wildcard policy
  forbidTargetWildcard?: boolean // reject target asterisks
  forbidSelfMapping?: boolean // reject identical names
  uniqueSource?: boolean // reject repeated sources
}

/**
 * KEY_REDIRECT_RULES enables required names, one hundred code points, source wildcard
 * placement, target restrictions, and unique sources for Key editors.
 */
export const KEY_REDIRECT_RULES: ModelMappingRuleOptions = {
  required: true,
  maxLength: 100,
  sourceWildcard: 'trailing',
  forbidTargetWildcard: true,
  forbidSelfMapping: true,
  uniqueSource: true,
}

/**
 * WILDCARD_ONLY_RULES checks source wildcard placement and forbids target wildcards
 * while leaving other validation options disabled for its callers.
 */
export const WILDCARD_ONLY_RULES: ModelMappingRuleOptions = {
  sourceWildcard: 'trailing',
  forbidTargetWildcard: true,
}

/**
 * isValidWildcardPattern accepts names with no asterisk or one trailing asterisk. Empty
 * text passes this structural check, so callers use the required-field option when
 * blank names are disallowed.
 */
export function isValidWildcardPattern(pattern: string): boolean {
  const starIndex = pattern.indexOf('*')
  if (starIndex === -1) return true
  return starIndex === pattern.length - 1 && pattern.lastIndexOf('*') === starIndex
}

/**
 * exceedsLength reports whether a name exceeds the optional code-point limit. An
 * omitted limit disables this check, and spreading the string counts Unicode code
 * points rather than UTF-16 units.
 */
const exceedsLength = (value: string, maxLength?: number) =>
  /**
   * Count Unicode code points so surrogate pairs contribute one character.
   */
  maxLength !== undefined && [...value].length > maxLength

/**
 * sourceIssue returns the first enabled source validation failure or undefined. The
 * precedence is required, length, wildcard placement, then duplicate count; callers
 * supply a trimmed source and counts for the complete row set.
 */
function sourceIssue(
  source: string,
  options: ModelMappingRuleOptions,
  sourceCounts: Map<string, number>,
): ModelMappingIssue | undefined {
  if (!source) return options.required ? 'sourceRequired' : undefined
  if (exceedsLength(source, options.maxLength)) return 'nameTooLong'
  if (options.sourceWildcard === 'trailing' && !isValidWildcardPattern(source)) {
    return 'sourceWildcardInvalid'
  }
  if (options.uniqueSource && (sourceCounts.get(source) ?? 0) > 1) {
    return 'duplicateSource'
  }
  return undefined
}

/**
 * targetIssue returns the first enabled target validation failure or undefined. It
 * checks required text, length, target wildcards, then equality with a nonempty source,
 * using the caller's trimmed strings.
 */
function targetIssue(
  source: string,
  target: string,
  options: ModelMappingRuleOptions,
): ModelMappingIssue | undefined {
  if (!target) return options.required ? 'targetRequired' : undefined
  if (exceedsLength(target, options.maxLength)) return 'nameTooLong'
  if (options.forbidTargetWildcard && target.includes('*')) {
    return 'targetWildcardInvalid'
  }
  if (options.forbidSelfMapping && source && source === target) {
    return 'selfMapping'
  }
  return undefined
}

/**
 * validateModelMappingRows produces issues aligned with each input row while retaining
 * the draft strings unchanged. Options default to no enabled checks, and comparisons
 * use trimmed case-sensitive names.
 *
 * Source counts are built across all nonempty trimmed sources before either field is
 * validated, allowing every duplicate row to receive the same duplicate-source issue.
 * Field checks retain their documented precedence, so an earlier length or wildcard
 * issue can mask duplicate or self-mapping detection. Rows with no failures receive an
 * empty issue object.
 */
export function validateModelMappingRows(
  rows: ModelMappingRow[],
  options: ModelMappingRuleOptions = {},
): ModelMappingRowIssues[] {
  /**
   * Count the complete trimmed source set before checking rows so duplicate errors are
   * independent of which duplicate appears first.
   */
  const sourceCounts = new Map<string, number>()
  for (const row of rows) {
    const source = row.from.trim()
    if (source) sourceCounts.set(source, (sourceCounts.get(source) ?? 0) + 1)
  }

  return rows.map((row) => {
    const source = row.from.trim()
    const target = row.to.trim()
    const issues: ModelMappingRowIssues = {}
    const fromIssue = sourceIssue(source, options, sourceCounts)
    const toIssue = targetIssue(source, target, options)
    if (fromIssue) issues.from = fromIssue
    if (toIssue) issues.to = toIssue
    return issues
  })
}

/**
 * firstModelMappingIssue returns the earliest issue in row order, checking source
 * before target within each row. It returns null when every row is clear, including
 * when the input list is empty.
 */
export function firstModelMappingIssue(issues: ModelMappingRowIssues[]): ModelMappingIssue | null {
  for (const issue of issues) {
    if (issue.from) return issue.from
    if (issue.to) return issue.to
  }
  return null
}

/**
 * mappingRowsToRecord trims source and target strings into a submission record. It
 * performs no validation, and repeated trimmed source keys take the last row's value
 * through Object.fromEntries.
 */
export function mappingRowsToRecord(rows: ModelMappingRow[]): Record<string, string> {
  return Object.fromEntries(rows.map((row) => [row.from.trim(), row.to.trim()]))
}

/**
 * recordToMappingRows expands mapping entries into editable source and target rows in
 * Object.entries order. Missing mappings produce an empty list, and stored strings are
 * retained exactly for editing.
 */
export function recordToMappingRows(
  mapping: Record<string, string> | null | undefined,
): ModelMappingRow[] {
  return Object.entries(mapping ?? {}).map(([from, to]) => ({ from, to }))
}
