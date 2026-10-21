import type {
  ContentModerationAPIKeyEntryInput,
  ContentModerationAPIKeyStatus,
  ContentModerationModelFilter,
  ContentModerationModelFilterType,
  KeywordBlockingMode,
  ModerationMode,
  UpdateContentModerationConfig,
} from '@/api/admin/riskControl'

/**
 * configDraft.ts converts risk-control editor values into moderation API requests.
 * The editor keeps credentials as multiline text, thresholds as percentages, and
 * a nullable proxy selection. The request converts those representations while
 * keeping missing, cleared, and replaced credentials distinct for the server.
 * Model validation precedes credential validation so the page reports one
 * predictable error when several settings are incomplete.
 *
 * Credential entries override the clear flag. Replacement suppresses individual
 * removals and metadata updates, while append mode can combine all three.
 * Metadata for deleted hashes is omitted. The conversion reads its inputs and
 * allocates request collections, letting the page retain drafts until the API
 * returns a saved configuration that can be applied to the form.
 */

/** APIKeysWriteMode selects whether submitted credentials extend or replace storage. */
export type APIKeysWriteMode = 'append' | 'replace'
/** APIKeyMetadataDraft holds editable attributes for one stored credential hash. */
export type APIKeyMetadataDraft = {
  // priority is normalized to an integer from one to one thousand; zero and
  // nonfinite values use one hundred.
  priority: number // Credential selection priority.
  // note keeps the editable text; request construction trims it and retains at most
  // two hundred characters.
  note: string // Administrator credential annotation.
}
/** RiskControlDraft represents the editable settings with UI-specific credential and percentage values. */
export type RiskControlDraft = ReturnType<typeof createRiskControlDraft>
/** RiskControlConfigResult carries a request or the first localized validation error. */
export type RiskControlConfigResult =
  // payload is available after both model and credential validation succeed; callers
  // can send it directly to updateConfig.
  | { payload: UpdateContentModerationConfig }
  // error identifies the first failed validation; callers translate this key and
  // leave the editor open for correction.
  | {
      error:
        'admin.riskControl.modelFilterModelsRequired' | 'admin.riskControl.apiKeysReplaceNoInput'
    }

// Keyword storage and threshold controls use these limits when converting drafts for
// moderation requests.
export const blockedKeywordMax = 10000
// Default category percentages populate untouched controls and reset actions in the
// risk-control editor.
export const riskThresholdDefaults: Record<string, number> = {
  harassment: 98,
  'harassment/threatening': 90,
  hate: 65,
  'hate/threatening': 65,
  illicit: 95,
  'illicit/violent': 95,
  'self-harm': 65,
  'self-harm/intent': 85,
  'self-harm/instructions': 65,
  sexual: 65,
  'sexual/minors': 65,
  violence: 95,
  'violence/graphic': 95,
}
// Category insertion order determines both editor rows and the serialized threshold
// property order.
export const riskThresholdCategories = Object.keys(riskThresholdDefaults)

/**
 * createRiskControlDraft allocates the initial settings and credential editor state.
 * Each invocation owns fresh arrays and a fresh threshold map, so page instances
 * can edit their collections independently before loading saved configuration.
 */
export function createRiskControlDraft() {
  return {
    enabled: false, // Moderation configuration enabled flag.
    mode: 'pre_block' as ModerationMode, // Selected moderation execution mode.
    base_url: 'https://api.openai.com', // Moderation provider base URL.
    model: 'omni-moderation-latest', // Moderation provider model identifier.
    // proxy_id is null for the direct connection choice; saving converts null to the
    // API clearing sentinel zero.
    proxy_id: null as number | null,
    // api_keys_text contains unsaved secrets and optional metadata; loading a
    // configuration clears it after reading only masked stored credentials.
    api_keys_text: '',
    api_key_configured: false, // Stored credential presence indicator.
    api_key_masked: '', // Primary stored credential mask.
    api_key_count: 0, // Stored moderation credential count.
    api_key_masks: [] as string[], // Stored credential display masks.
    api_key_statuses: [] as ContentModerationAPIKeyStatus[], // Stored credential health rows.
    // api_keys_mode selects append or replace; replacement omits per-hash deletions
    // and stored metadata from the request.
    api_keys_mode: 'append' as APIKeysWriteMode,
    // clear_api_key requests removal of stored credentials; nonempty parsed input
    // overrides this flag during request construction.
    clear_api_key: false,
    timeout_ms: 3000, // Moderation request timeout milliseconds.
    retry_count: 2, // Moderation request retry count.
    sample_rate: 100, // Request sampling percentage value.
    // all_groups and group_ids jointly describe the scope. Saving all_groups clears
    // group_ids in the request, while a restricted scope copies the selected IDs.
    all_groups: true, // Global group scope toggle.
    // group_ids belongs to this draft; the request receives a copied list when the
    // selected scope is restricted.
    group_ids: [] as number[], // Restricted group selection identifiers.
    record_non_hits: false, // Retain successful audit records.
    worker_count: 4, // Asynchronous audit worker count.
    queue_size: 32768, // Asynchronous audit queue capacity.
    block_status: 403, // Blocked response HTTP status.
    // Blocked response public message.
    block_message:
      "This request was blocked by the site's content policy. Revise your input and try again.",
    email_on_hit: true, // Send risk notification emails.
    auto_ban_enabled: true, // Enable moderation automatic bans.
    ban_threshold: 10, // Moderation violations before banning.
    violation_window_hours: 720, // Moderation violation window hours.
    cyber_warning_enabled: true, // Record upstream cyber warnings.
    cyber_auto_ban_enabled: false, // Enable cyber automatic bans.
    cyber_ban_threshold: 10, // Cyber warnings before banning.
    cyber_violation_window_hours: 720, // Cyber warning window hours.
    hit_retention_days: 180, // Risk hit retention days.
    non_hit_retention_days: 3, // Non-hit record retention days.
    pre_hash_check_enabled: false, // Enable blocked hash lookup.
    // thresholds owns category percentages for number inputs; request conversion
    // uses the known category order and emits bounded ratios.
    thresholds: { ...riskThresholdDefaults } as Record<string, number>,
    blocked_keywords_text: '', // Multiline blocked keyword draft.
    keyword_blocking_mode: 'keyword_and_api' as KeywordBlockingMode, // Keyword and API strategy.
    // model_filter_type and model_filter_models jointly describe the model scope.
    // All mode emits an empty list, while include and exclude require at least one
    // normalized model.
    model_filter_type: 'all' as ContentModerationModelFilterType, // Selected model matching mode.
    // model_filter_models contains editable names; request conversion trims them and
    // deduplicates by lowercase spelling while preserving first occurrence.
    model_filter_models: [] as string[],
    audit_user_text_max_chars: 1000000, // User audit character limit.
    audit_images: true, // Include images in audits.
    audit_tool_outputs: true, // Include tool output audits.
    audit_tool_output_max_chars: 1000000, // Tool audit character limit.
  }
}

/**
 * buildRiskControlConfigRequest validates a settings draft and constructs its API
 * request. Inputs remain owned by the caller, and model selection errors win
 * before empty replacement errors. A successful result contains copied group and
 * deletion lists, normalized model names, and newly constructed credential entries.
 *
 * Credential input overrides clear_api_key, including when clear was previously
 * selected. Replacement mode suppresses hash deletions and metadata edits because
 * the server receives a complete credential replacement. Append mode retains
 * those operations but excludes metadata for hashes being deleted. Percentages,
 * proxy clearing, numeric defaults, and retention limits are converted here before
 * any request reaches the API client.
 */
export function buildRiskControlConfigRequest(
  configForm: RiskControlDraft,
  pendingDeleteApiKeyHashes: readonly string[],
  apiKeyMetadataDrafts: Readonly<Record<string, APIKeyMetadataDraft>>,
): RiskControlConfigResult {
  const modelFilterPayload = buildModelFilterPayload(configForm)
  if (modelFilterPayload.type !== 'all' && modelFilterPayload.models.length === 0) {
    return { error: 'admin.riskControl.modelFilterModelsRequired' }
  }
  const payload: UpdateContentModerationConfig = {
    enabled: configForm.enabled,
    mode: configForm.mode,
    base_url: configForm.base_url,
    model: configForm.model,
    // A null editor selection becomes zero because the API uses zero to remove the
    // saved proxy reference.
    proxy_id: configForm.proxy_id ?? 0,
    timeout_ms: Number(configForm.timeout_ms) || 3000,
    retry_count: Number(configForm.retry_count) || 0,
    sample_rate: Number(configForm.sample_rate) || 0,
    all_groups: configForm.all_groups,
    group_ids: configForm.all_groups ? [] : [...configForm.group_ids],
    record_non_hits: configForm.record_non_hits,
    clear_api_key: configForm.clear_api_key,
    worker_count: Number(configForm.worker_count) || 4,
    queue_size: Number(configForm.queue_size) || 32768,
    block_status: Number(configForm.block_status) || 403,
    block_message:
      configForm.block_message ||
      "This request was blocked by the site's content policy. Revise your input and try again.",
    email_on_hit: configForm.email_on_hit,
    auto_ban_enabled: configForm.auto_ban_enabled,
    ban_threshold: Number(configForm.ban_threshold) || 10,
    violation_window_hours: Number(configForm.violation_window_hours) || 720,
    cyber_warning_enabled: configForm.cyber_warning_enabled,
    cyber_auto_ban_enabled: configForm.cyber_auto_ban_enabled,
    cyber_ban_threshold: Number(configForm.cyber_ban_threshold) || 10,
    cyber_violation_window_hours: Number(configForm.cyber_violation_window_hours) || 720,
    hit_retention_days: Number(configForm.hit_retention_days) || 180,
    // Non-hit history retains at most three days.
    non_hit_retention_days: Math.min(
      Math.max(Number(configForm.non_hit_retention_days) || 3, 1),
      3,
    ),
    pre_hash_check_enabled: configForm.pre_hash_check_enabled,
    thresholds: buildRiskThresholdPayload(configForm.thresholds),
    blocked_keywords: parseBlockedKeywords(configForm.blocked_keywords_text),
    keyword_blocking_mode: configForm.keyword_blocking_mode,
    model_filter: modelFilterPayload,
    // Audit text limits count characters, capped at one million.
    audit_user_text_max_chars: Math.min(
      Math.max(Number(configForm.audit_user_text_max_chars) || 1000000, 1),
      1000000,
    ),
    audit_images: configForm.audit_images,
    audit_tool_outputs: configForm.audit_tool_outputs,
    // Tool output uses the same character ceiling as user text.
    audit_tool_output_max_chars: Math.min(
      Math.max(Number(configForm.audit_tool_output_max_chars) || 1000000, 1),
      1000000,
    ),
  }
  const keyEntries = parseApiKeyEntries(configForm.api_keys_text)
  if (!payload.clear_api_key && configForm.api_keys_mode === 'replace' && keyEntries.length === 0) {
    return { error: 'admin.riskControl.apiKeysReplaceNoInput' }
  }
  // Newly entered credentials take precedence over a pending clear action, so the
  // request can save those supplied secrets.
  if (keyEntries.length > 0) {
    payload.api_key_entries = keyEntries
    payload.api_keys_mode = configForm.api_keys_mode
    payload.clear_api_key = false
  }
  if (
    !payload.clear_api_key &&
    configForm.api_keys_mode !== 'replace' &&
    pendingDeleteApiKeyHashes.length > 0
  ) {
    payload.delete_api_key_hashes = [...pendingDeleteApiKeyHashes]
  }
  // Metadata follows individual removals: omitting deleted hashes prevents a
  // simultaneous edit from targeting a credential being removed.
  if (!payload.clear_api_key && configForm.api_keys_mode !== 'replace') {
    const deleted = new Set(pendingDeleteApiKeyHashes)
    payload.api_key_updates = Object.entries(apiKeyMetadataDrafts)
      .filter(([keyHash]) => !deleted.has(keyHash))
      .map(([keyHash, metadata]) => ({
        key_hash: keyHash,
        priority: normalizeAPIKeyPriority(metadata.priority),
        // Stored key annotations contain at most two hundred characters.
        note: metadata.note.trim().slice(0, 200),
      }))
  }
  return { payload }
}

/**
 * normalizeAPIKeyPriority returns an integer credential priority from one to one
 * thousand. Nonfinite values and zero use one hundred, matching the editor's
 * initial priority for an unspecified credential.
 */
export function normalizeAPIKeyPriority(value: number | undefined): number {
  if (!Number.isFinite(value)) return 100
  // Credential priorities use whole numbers within the configured range.
  return Math.min(1000, Math.max(1, Math.round(value || 100)))
}

/**
 * parseApiKeyEntries parses each nonempty line as a credential with optional
 * priority and note columns. Repeated credential text keeps its first position
 * and its last metadata, matching Map insertion order. The first two separators
 * identify the credential and priority; further separators remain inside the note.
 *
 * Blank lines are ignored. Priority normalization supplies the same defaults as
 * stored metadata editing, and notes are trimmed and limited to two hundred
 * characters before the entries are used for testing or persistence.
 */
export function parseApiKeyEntries(value: string): ContentModerationAPIKeyEntryInput[] {
  // Splitting the first two columns keeps separators inside the remaining note,
  // while plain credential lines retain default metadata.
  const byKey = new Map<string, ContentModerationAPIKeyEntryInput>()
  for (const line of value.split(/\r?\n/)) {
    const parts = line.split('|')
    const apiKey = parts.shift()?.trim() || ''
    if (!apiKey) continue
    const rawPriority = parts.shift()?.trim() || ''
    const parsedPriority = rawPriority === '' ? 100 : Number(rawPriority)
    byKey.set(apiKey, {
      api_key: apiKey,
      priority: normalizeAPIKeyPriority(parsedPriority),
      // Further separators are note content within the annotation length limit.
      note: parts.join('|').trim().slice(0, 200),
    })
  }
  return [...byKey.values()]
}

/**
 * parseApiKeys returns unique credential strings from the multiline editor input.
 */
export function parseApiKeys(value: string): string[] {
  return parseApiKeyEntries(value).map((item) => item.api_key)
}

/**
 * normalizeKeywordBlockingMode accepts the three supported keyword strategies.
 * Unknown configuration values use combined keyword and API checks, which is the
 * initial strategy displayed by the settings editor.
 */
export function normalizeKeywordBlockingMode(value: unknown): KeywordBlockingMode {
  if (value === 'keyword_only' || value === 'api_only' || value === 'keyword_and_api') {
    return value
  }
  return 'keyword_and_api'
}

/**
 * normalizeModelFilter converts unknown stored data into an editable model rule.
 * Missing objects use all models; include and exclude modes keep normalized names,
 * while all mode clears the list.
 */
export function normalizeModelFilter(value: unknown): ContentModerationModelFilter {
  if (!value || typeof value !== 'object') {
    return { type: 'all', models: [] }
  }
  const raw = value as Partial<ContentModerationModelFilter>
  const type = normalizeModelFilterType(raw.type)
  const models = type === 'all' ? [] : normalizeModelNames(raw.models)
  return { type, models }
}

/**
 * normalizeModelFilterType returns a recognized model scope or all models.
 * The all value is also used for missing and unknown stored values when loading
 * a configuration into the editor.
 */
function normalizeModelFilterType(value: unknown): ContentModerationModelFilterType {
  if (value === 'include' || value === 'exclude' || value === 'all') {
    return value
  }
  return 'all'
}

/**
 * normalizeModelNames returns trimmed model strings in their first occurrence
 * order. Matching ignores case, keeps the first spelling, skips empty entries,
 * and returns an empty list for nonarray input.
 */
function normalizeModelNames(models: unknown): string[] {
  if (!Array.isArray(models)) return []
  const seen = new Set<string>()
  const out: string[] = []
  for (const item of models) {
    const model = String(item ?? '').trim()
    if (!model) continue
    const key = model.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    out.push(model)
  }
  return out
}

/**
 * buildModelFilterPayload constructs the request rule from editable model fields.
 * All mode clears model names; include and exclude modes normalize their lists,
 * leaving empty-list validation to the request builder.
 */
function buildModelFilterPayload(configForm: RiskControlDraft): ContentModerationModelFilter {
  const type = normalizeModelFilterType(configForm.model_filter_type)
  if (type === 'all') {
    return { type: 'all', models: [] }
  }
  return {
    type,
    models: normalizeModelNames(configForm.model_filter_models),
  }
}

/**
 * riskThresholdsFromConfig overlays finite stored ratios onto default editor
 * percentages. Unknown categories are ignored, absent values keep their defaults,
 * and finite percentages are constrained to the percentage input's inclusive
 * zero-to-one-hundred range.
 */
export function riskThresholdsFromConfig(
  thresholds: Record<string, number> | null | undefined,
): Record<string, number> {
  const out: Record<string, number> = { ...riskThresholdDefaults }
  for (const category of riskThresholdCategories) {
    const value = thresholds?.[category]
    if (Number.isFinite(value)) {
      // Stored ratios become percentages for the editable percentage inputs.
      out[category] = clampPercent(Number(value) * 100)
    }
  }
  return out
}

/**
 * buildRiskThresholdPayload converts known editor percentages into request ratios.
 * Each category is clamped independently and rounded to four decimal places, with
 * missing or nonfinite values represented as zero.
 */
function buildRiskThresholdPayload(thresholds: Record<string, number>): Record<string, number> {
  const payload: Record<string, number> = {}
  for (const category of riskThresholdCategories) {
    // Four decimal places retain hundredth-percent precision in request ratios.
    payload[category] = Number((clampPercent(thresholds[category]) / 100).toFixed(4))
  }
  return payload
}

/**
 * clampPercent converts an unknown input to the editor's allowed percentage range.
 * Nonfinite numeric conversions return zero, while finite numbers are constrained
 * to the inclusive zero-to-one-hundred bounds used by percentage inputs.
 */
export function clampPercent(value: unknown): number {
  const numeric = Number(value)
  if (!Number.isFinite(numeric)) {
    return 0
  }
  return Math.min(100, Math.max(0, numeric))
}

/**
 * parseBlockedKeywords builds the ordered keyword list from multiline editor text.
 * Blank lines are ignored, matching ignores case, and each retained keyword is
 * truncated to two hundred characters after duplicate detection. This means
 * longer distinct inputs may share the same truncated output.
 *
 * The first spelling of a repeated full keyword wins. Processing stops at ten
 * thousand entries, matching the limit shown beside the editor; later lines do
 * not participate in duplicate detection or the resulting request.
 */
export function parseBlockedKeywords(value: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const line of value.split(/\r?\n/)) {
    const kw = line.trim()
    if (!kw) continue
    const key = kw.toLowerCase()
    if (seen.has(key)) continue
    seen.add(key)
    // Limit each retained keyword to the backend field length.
    out.push(kw.slice(0, 200))
    // The editor reports the same maximum retained keyword count.
    if (out.length >= blockedKeywordMax) break
  }
  return out
}
