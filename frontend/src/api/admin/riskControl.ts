import { apiClient } from '../client'

/**
 * riskControl.ts sends content-moderation configuration and diagnostic requests
 * for administrators. Credential updates preserve the separate append, replace,
 * metadata and deletion fields, while log, warning and media endpoints expose
 * the server-recorded observations used during review.
 */

/**
 * ModerationMode selects disabled moderation, observation or blocking before
 * upstream execution.
 */
export type ModerationMode = 'off' | 'observe' | 'pre_block'

/**
 * KeywordBlockingMode selects keyword checks, moderation API checks or their
 * combined blocking path.
 */
export type KeywordBlockingMode = 'keyword_only' | 'keyword_and_api' | 'api_only'

/**
 * ContentModerationModelFilterType selects all models or an included or excluded
 * model list for moderation.
 */
export type ContentModerationModelFilterType = 'all' | 'include' | 'exclude'

/**
 * ContentModerationModelFilter pairs the model-selection mode with the list
 * interpreted by that mode.
 */
export interface ContentModerationModelFilter {
  type: ContentModerationModelFilterType // Represented record category.
  models: string[] // Model identifier list.
}

/**
 * ContentModerationConfig contains current moderation policy, credential
 * summaries, execution limits and retention settings.
 */
export interface ContentModerationConfig {
  enabled: boolean // Feature activation flag.
  mode: ModerationMode // Selected operating mode.
  base_url: string // Configured base url.
  model: string // Model identifier value.
  proxy_id: number | null // Selected proxy identifier.
  api_key_configured: boolean // API key configured.
  api_key_masked: string // API key masked.
  api_key_count: number // Stored credential count.
  api_key_masks: string[] // Masked credential displays.
  api_key_statuses: ContentModerationAPIKeyStatus[] // API key statuses.
  timeout_ms: number // Configured timeout milliseconds.
  sample_rate: number // Moderation sampling fraction.
  all_groups: boolean // Configured all groups.
  group_ids: number[] // Configured group ids.
  record_non_hits: boolean // Record non hits.
  thresholds: Record<string, number> // Configured category thresholds.
  worker_count: number // Configured worker count.
  queue_size: number // Configured queue size.
  block_status: number // Configured block status.
  block_message: string // Configured block message.
  email_on_hit: boolean // Email on hit.
  auto_ban_enabled: boolean // Auto ban enabled.
  ban_threshold: number // Configured ban threshold.
  violation_window_hours: number // Violation window hours.
  retry_count: number // Configured retry count.
  hit_retention_days: number // Hit retention days.
  non_hit_retention_days: number // Non hit retention days.
  pre_hash_check_enabled: boolean // Pre hash check enabled.
  cyber_warning_enabled: boolean // Cyber warning enabled.
  cyber_auto_ban_enabled: boolean // Cyber auto ban enabled.
  cyber_ban_threshold: number // Cyber ban threshold.
  cyber_violation_window_hours: number // Cyber violation window hours.
  blocked_keywords: string[] // Configured blocked keywords.
  keyword_blocking_mode: KeywordBlockingMode // Keyword blocking mode.
  model_filter: ContentModerationModelFilter // Configured model filter.
  audit_user_text_max_chars: number // Audit user text max chars.
  audit_images: boolean // Configured audit images.
  audit_tool_outputs: boolean // Audit tool outputs.
  audit_tool_output_max_chars: number // Audit tool output max chars.
}

/**
 * ContentModerationAPIKeyStatusValue distinguishes unknown, working, errored and
 * temporarily frozen moderation credentials.
 */
export type ContentModerationAPIKeyStatusValue = 'unknown' | 'ok' | 'error' | 'frozen'

/**
 * ContentModerationAPIKeyStatus reports a masked credential's health,
 * observations, priority and administrator note.
 */
export interface ContentModerationAPIKeyStatus {
  index: number // Zero-based entry index.
  key_hash: string // Stored credential hash.
  masked: string // Masked credential display.
  status: ContentModerationAPIKeyStatusValue // Current server-reported status.
  failure_count: number // Recorded failure count.
  success_count: number // Recorded success count.
  last_error: string // Recorded last error.
  last_checked_at?: string // Last checked at.
  frozen_until?: string // Recorded frozen until.
  last_latency_ms: number // Last latency milliseconds.
  last_http_status: number // Last http status.
  last_tested: boolean // Recorded last tested.
  configured: boolean // Stored configuration flag.
  priority: number // Configured selection priority.
  note: string // Administrator supplied note.
}

/**
 * ContentModerationAPIKeyEntryInput couples submitted secret text with its
 * requested priority and administrator note.
 */
export interface ContentModerationAPIKeyEntryInput {
  api_key: string // Moderation service credential.
  priority: number // Configured selection priority.
  note: string // Administrator supplied note.
}

/**
 * ContentModerationAPIKeyMetadataUpdate identifies a stored credential by hash
 * and edits its priority and note.
 */
export interface ContentModerationAPIKeyMetadataUpdate {
  key_hash: string // Stored credential hash.
  priority: number // Configured selection priority.
  note: string // Administrator supplied note.
}

/**
 * TestContentModerationAPIKeysPayload carries temporary moderation test input
 * and optional connection overrides. Omitted proxy selection reuses saved
 * configuration, while zero requests direct access for this test.
 */
export interface TestContentModerationAPIKeysPayload {
  api_keys?: string[] // Moderation service credentials.
  base_url?: string // Recorded base url.
  model?: string // Model identifier value.
  timeout_ms?: number // Recorded timeout milliseconds.
  /**
   * Omission reuses the saved proxy configuration; zero requests a direct
   * connection, and a positive value selects a proxy.
   */
  proxy_id?: number // Selected proxy identifier.
  prompt?: string // Submitted generation prompt.
  images?: string[] // Submitted image inputs.
}

/**
 * TestContentModerationAPIKeysResponse returns per-credential test status,
 * optional audit observations and the tested image count.
 */
export interface TestContentModerationAPIKeysResponse {
  items: ContentModerationAPIKeyStatus[] // Returned collection entries.
  audit_result?: ContentModerationTestAuditResult // Recorded audit result.
  image_count: number // Recorded image count.
}

/**
 * ContentModerationTestAuditResult combines a test's flagged decision with
 * category scores and the thresholds applied.
 */
export interface ContentModerationTestAuditResult {
  flagged: boolean // Moderation hit flag.
  highest_category: string // Recorded highest category.
  highest_score: number // Recorded highest score.
  composite_score: number // Recorded composite score.
  category_scores: Record<string, number> // Recorded category scores.
  thresholds: Record<string, number> // Configured category thresholds.
}

/**
 * UpdateContentModerationConfig carries selected moderation settings and
 * credential actions for a save. Credential inputs, write mode, per-hash edits
 * and deletions remain separate so the form can express append, replacement or
 * clearing.
 */
export interface UpdateContentModerationConfig {
  enabled?: boolean // Feature activation flag.
  mode?: ModerationMode // Selected operating mode.
  base_url?: string // Configured base url.
  model?: string // Model identifier value.
  /**
   * Omission leaves the proxy unchanged; zero clears the proxy, and a positive
   * value selects a proxy.
   */
  proxy_id?: number // Selected proxy identifier.
  api_key?: string // Moderation service credential.
  api_keys?: string[] // Moderation service credentials.
  /**
   * Parsed new credential entries carry their own priority and note; the form
   * chooses append or replacement through api_keys_mode.
   */
  api_key_entries?: ContentModerationAPIKeyEntryInput[] // API key entries.
  /**
   * These edits target existing credentials by key hash; the form omits them
   * while clearing or replacing the credential set.
   */
  api_key_updates?: ContentModerationAPIKeyMetadataUpdate[] // API key updates.
  /**
   * Append adds submitted credentials, while replace selects replacement of the
   * set; the form blocks empty replacement unless clearing was selected.
   */
  api_keys_mode?: 'append' | 'replace' // API keys mode.
  /**
   * Per-hash deletions apply to existing credentials; the form omits these when
   * replacing or clearing the entire credential set.
   */
  delete_api_key_hashes?: string[] // Delete API key hashes.
  /**
   * The form requests clearing stored credentials through this flag, but
   * nonempty parsed new credential input takes precedence during payload
   * construction.
   */
  clear_api_key?: boolean // Clear API key.
  timeout_ms?: number // Configured timeout milliseconds.
  sample_rate?: number // Moderation sampling fraction.
  all_groups?: boolean // Configured all groups.
  group_ids?: number[] // Configured group ids.
  record_non_hits?: boolean // Record non hits.
  thresholds?: Record<string, number> // Configured category thresholds.
  worker_count?: number // Configured worker count.
  queue_size?: number // Configured queue size.
  block_status?: number // Configured block status.
  block_message?: string // Configured block message.
  email_on_hit?: boolean // Email on hit.
  auto_ban_enabled?: boolean // Auto ban enabled.
  ban_threshold?: number // Configured ban threshold.
  violation_window_hours?: number // Violation window hours.
  retry_count?: number // Configured retry count.
  hit_retention_days?: number // Hit retention days.
  non_hit_retention_days?: number // Non hit retention days.
  pre_hash_check_enabled?: boolean // Pre hash check enabled.
  cyber_warning_enabled?: boolean // Cyber warning enabled.
  cyber_auto_ban_enabled?: boolean // Cyber auto ban enabled.
  cyber_ban_threshold?: number // Cyber ban threshold.
  cyber_violation_window_hours?: number // Cyber violation window hours.
  blocked_keywords?: string[] // Configured blocked keywords.
  keyword_blocking_mode?: KeywordBlockingMode // Keyword blocking mode.
  model_filter?: ContentModerationModelFilter // Configured model filter.
  audit_user_text_max_chars?: number // Audit user text max chars.
  audit_images?: boolean // Configured audit images.
  audit_tool_outputs?: boolean // Audit tool outputs.
  audit_tool_output_max_chars?: number // Audit tool output max chars.
}

/**
 * ContentModerationRuntimeStatus reports moderation workers, queued work,
 * synchronous checks and credential activity counters.
 */
export interface ContentModerationRuntimeStatus {
  enabled: boolean // Feature activation flag.
  risk_control_enabled: boolean // Risk control enabled.
  mode: ModerationMode // Selected operating mode.
  worker_count: number // Recorded worker count.
  max_workers: number // Recorded max workers.
  active_workers: number // Recorded active workers.
  idle_workers: number // Recorded idle workers.
  queue_size: number // Recorded queue size.
  queue_length: number // Recorded queue length.
  queue_usage_percent: number // Queue usage percent.
  enqueued: number // Enqueued task count.
  dropped: number // Dropped task count.
  processed: number // Processed task count.
  errors: number // Recorded failure count.
  pre_block_active: number // Pre block active.
  pre_block_checked: number // Pre block checked.
  pre_block_allowed: number // Pre block allowed.
  pre_block_blocked: number // Pre block blocked.
  pre_block_errors: number // Pre block errors.
  pre_block_avg_latency_ms: number // Pre block avg latency milliseconds.
  pre_block_api_key_active: number // Pre block API key active.
  pre_block_api_key_available_count: number // Pre block API key available count.
  pre_block_api_key_total_calls: number // Pre block API key total calls.
  pre_block_api_key_loads: ContentModerationAPIKeyLoad[] // Pre block API key loads.
  api_key_statuses: ContentModerationAPIKeyStatus[] // API key statuses.
  flagged_hash_count: number // Flagged hash count.
  last_cleanup_at?: string // Last cleanup at.
  last_cleanup_deleted_hit: number // Last cleanup deleted hit.
  last_cleanup_deleted_non_hit: number // Last cleanup deleted non hit.
}

/**
 * ContentModerationAPIKeyLoad reports one masked credential's current activity,
 * request counts and latency observations.
 */
export interface ContentModerationAPIKeyLoad {
  index: number // Zero-based entry index.
  key_hash: string // Stored credential hash.
  masked: string // Masked credential display.
  status: ContentModerationAPIKeyStatusValue // Current server-reported status.
  active: number // Active operation count.
  total: number // Total matching records.
  success: number // Successful operation count.
  errors: number // Recorded failure count.
  avg_latency_ms: number // Avg latency milliseconds.
  last_latency_ms: number // Last latency milliseconds.
  last_http_status: number // Last http status.
  priority: number // Configured selection priority.
  note: string // Administrator supplied note.
}

/**
 * ContentModerationLog records a moderation decision with request identity,
 * inspected content metadata and enforcement outcome.
 */
export interface ContentModerationLog {
  id: number // Record identifier value.
  request_id: string // Unified request identifier.
  user_id: number | null // Owning user identifier.
  user_email: string // Recorded user email.
  billing_user_id?: number | null // Billing user ID.
  team_id?: number | null // Team record identifier.
  api_key_id: number | null // API-key record identifier.
  api_key_name: string // API key name.
  group_id: number | null // Group record identifier.
  group_name: string // Recorded group name.
  endpoint: string // Protocol endpoint path.
  provider: string // Provider identity details.
  model: string // Model identifier value.
  mode: string // Selected operating mode.
  action: string // Selected outcome action.
  flagged: boolean // Moderation hit flag.
  highest_category: string // Recorded highest category.
  highest_score: number // Recorded highest score.
  matched_keyword: string // Recorded matched keyword.
  category_scores: Record<string, number> // Recorded category scores.
  threshold_snapshot: Record<string, number> // Recorded threshold snapshot.
  input_excerpt: string // Recorded input excerpt.
  source?: 'user' | 'tool' | 'mixed' // Recorded observation source.
  input_items?: ContentModerationInputItem[] // Recorded input items.
  content_complete?: boolean // Recorded content complete.
  audit_complete?: boolean // Recorded audit complete.
  text_unit_count?: number // Text unit count.
  image_unit_count?: number // Image unit count.
  failed_unit_count?: number // Failed unit count.
  failed_units?: ContentModerationFailedUnit[] // Recorded failed units.
  media?: ContentModerationMedia[] // Recorded media metadata.
  upstream_latency_ms: number | null // Upstream latency milliseconds.
  error: string // Operation failure detail.
  violation_count: number // Recorded violation count.
  auto_banned: boolean // Recorded auto banned.
  email_sent: boolean // Recorded email sent.
  user_status: string // Recorded user status.
  queue_delay_ms: number | null // Queue delay milliseconds.
  created_at: string // Record creation timestamp.
}

/**
 * ContentModerationCyberWarning records an upstream warning with request
 * ownership, captured inputs and any resulting account action.
 */
export interface ContentModerationCyberWarning {
  id: number // Record identifier value.
  request_id: string // Unified request identifier.
  user_id: number | null // Owning user identifier.
  user_email: string // Recorded user email.
  billing_user_id?: number | null // Billing user ID.
  team_id?: number | null // Team record identifier.
  api_key_id: number | null // API-key record identifier.
  api_key_name: string // API key name.
  group_id: number | null // Group record identifier.
  group_name: string // Recorded group name.
  provider_id: number | null // Provider record identifier.
  provider_name: string // Recorded provider name.
  endpoint: string // Protocol endpoint path.
  model: string // Model identifier value.
  upstream_status: number // Recorded upstream status.
  warning_text: string // Recorded warning text.
  prompt_excerpt: string // Recorded prompt excerpt.
  source?: 'user' | 'tool' | 'mixed' // Recorded observation source.
  input_items?: ContentModerationInputItem[] // Recorded input items.
  content_complete?: boolean // Recorded content complete.
  audit_complete?: boolean // Recorded audit complete.
  text_unit_count?: number // Text unit count.
  image_unit_count?: number // Image unit count.
  failed_unit_count?: number // Failed unit count.
  failed_units?: ContentModerationFailedUnit[] // Recorded failed units.
  media?: ContentModerationMedia[] // Recorded media metadata.
  violation_count: number // Recorded violation count.
  auto_banned: boolean // Recorded auto banned.
  email_sent: boolean // Recorded email sent.
  user_status: string // Recorded user status.
  created_at: string // Record creation timestamp.
}

/**
 * ContentModerationInputItem retains the source and kind of one audited text or
 * image input.
 */
export interface ContentModerationInputItem {
  index: number // Zero-based entry index.
  source: 'user' | 'tool' // Recorded observation source.
  type: 'text' | 'image' // Represented record category.
  text?: string // Recorded input text.
  image_ref?: string // Requested image ref.
}

/**
 * ContentModerationFailedUnit identifies an audited unit that failed and records
 * its error and optional input-source index.
 */
export interface ContentModerationFailedUnit {
  type: string // Represented record category.
  index: number // Zero-based entry index.
  source_index?: number // Original input index.
  error: string // Operation failure detail.
}

/**
 * ContentModerationMedia describes stored media metadata and snapshot
 * availability for a moderation record. Callers use id to fetch binary content
 * and snapshot_status to distinguish pending, ready and failed snapshots.
 */
export interface ContentModerationMedia {
  id: number // Record identifier value.
  source_index: number // Original input index.
  source: 'user' | 'tool' // Recorded observation source.
  mime_type: string // Content MIME type.
  sha256: string // SHA-256 content digest.
  byte_size: number // Content length bytes.
  original_ref: string // Recorded original ref.
  snapshot_status: 'pending' | 'ready' | 'error' // Recorded snapshot status.
  snapshot_error: string // Recorded snapshot error.
  created_at: string // Record creation timestamp.
}

/**
 * ListContentModerationLogsParams selects moderation decision history by
 * pagination, result, group, endpoint and interval.
 */
export interface ListContentModerationLogsParams {
  page?: number // Requested page number.
  page_size?: number // Requested page size.
  result?: string // Moderation result filter.
  group_id?: number // Group record identifier.
  endpoint?: string // Protocol endpoint path.
  search?: string // Free-text search query.
  from?: string // Requested interval start.
  to?: string // Requested interval end.
}

/**
 * ListCyberWarningsParams selects upstream warning history by pagination, user,
 * provider and interval filters.
 */
export interface ListCyberWarningsParams {
  page?: number // Requested page number.
  page_size?: number // Requested page size.
  user_id?: number // Owning user identifier.
  provider_id?: number // Provider record identifier.
  search?: string // Free-text search query.
  from?: string // Requested interval start.
  to?: string // Requested interval end.
}

/**
 * ContentModerationLogsResponse pairs moderation log entries with their
 * pagination counts and requested page size.
 */
export interface ContentModerationLogsResponse {
  items: ContentModerationLog[] // Returned collection entries.
  total: number // Total matching records.
  page: number // Requested page number.
  page_size: number // Requested page size.
  pages: number // Available page count.
}

/**
 * CyberWarningsResponse pairs upstream warning entries with pagination counts
 * for administrator review.
 */
export interface CyberWarningsResponse {
  items: ContentModerationCyberWarning[] // Returned collection entries.
  total: number // Total matching records.
  page: number // Requested page number.
  page_size: number // Requested page size.
  pages: number // Available page count.
}

/**
 * CyberSummaryUser groups warning observations by user with API-key and
 * most-recent-occurrence details.
 */
export interface CyberSummaryUser {
  count: number // Observed item count.
  user_id?: number // Owning user identifier.
  user_email: string // Recorded user email.
  api_keys: string // Joined recorded key IDs and names.
  last_seen: string // Recorded last seen.
}

/**
 * CyberSummaryProvider groups warning observations by provider with
 * affected-user count and latest occurrence.
 */
export interface CyberSummaryProvider {
  count: number // Observed item count.
  provider_id?: number // Provider record identifier.
  provider_name: string // Recorded provider name.
  users: number // Distinct user count.
  last_seen: string // Recorded last seen.
}

/**
 * CyberSummary combines warning event and request totals with user and provider
 * breakdowns.
 */
export interface CyberSummary {
  events: number // Warning event count.
  requests: number // Observed request count.
  users: number // Distinct user count.
  providers: number // Distinct provider count.
  by_user: CyberSummaryUser[] // Recorded by user.
  by_provider: CyberSummaryProvider[] // Recorded by provider.
}

/**
 * ContentModerationUnbanUserResponse identifies the user whose moderation ban
 * was removed and reports the resulting status.
 */
export interface ContentModerationUnbanUserResponse {
  user_id: number // Owning user identifier.
  status: string // Current server-reported status.
}

/**
 * DeleteFlaggedHashResponse identifies the submitted input hash and reports
 * whether its flagged entry was deleted.
 */
export interface DeleteFlaggedHashResponse {
  input_hash: string // Audited input hash.
  deleted: boolean // Whether the flagged entry was removed.
}

/**
 * ClearFlaggedHashesResponse reports the number of flagged input-hash entries
 * removed by the clear operation.
 */
export interface ClearFlaggedHashesResponse {
  deleted: number // Deleted record count.
}

/**
 * getConfig retrieves config and returns the server response data.
 */
export async function getConfig(): Promise<ContentModerationConfig> {
  const { data } = await apiClient.get<ContentModerationConfig>('/admin/risk-control/config')
  return data
}

/**
 * updateConfig updates config and returns the server response data.
 */
export async function updateConfig(
  payload: UpdateContentModerationConfig,
): Promise<ContentModerationConfig> {
  const { data } = await apiClient.put<ContentModerationConfig>(
    '/admin/risk-control/config',
    payload,
  )
  return data
}

/**
 * getStatus retrieves status and returns the server response data.
 */
export async function getStatus(): Promise<ContentModerationRuntimeStatus> {
  const { data } = await apiClient.get<ContentModerationRuntimeStatus>('/admin/risk-control/status')
  return data
}

/**
 * testAPIKeys submits moderation test inputs and optional connection or
 * credential overrides. An omitted payload sends an empty object so the server
 * can use its stored test configuration.
 */
export async function testAPIKeys(
  payload: TestContentModerationAPIKeysPayload = {},
): Promise<TestContentModerationAPIKeysResponse> {
  const { data } = await apiClient.post<TestContentModerationAPIKeysResponse>(
    '/admin/risk-control/api-keys/test',
    payload,
  )
  return data
}

/**
 * listLogs retrieves moderation decision records with optional pagination and
 * outcome filters. An omitted query sends an empty object, allowing the endpoint
 * to apply its default list selection.
 */
export async function listLogs(
  params: ListContentModerationLogsParams = {},
): Promise<ContentModerationLogsResponse> {
  const { data } = await apiClient.get<ContentModerationLogsResponse>('/admin/risk-control/logs', {
    params,
  })
  return data
}

/**
 * getLog retrieves log and returns the server response data.
 */
export async function getLog(id: number): Promise<ContentModerationLog> {
  const { data } = await apiClient.get<ContentModerationLog>(`/admin/risk-control/logs/${id}`)
  return data
}

/**
 * listCyberWarnings retrieves recorded upstream warning entries with optional
 * actor and interval filters. Omitted parameters send an empty query object,
 * allowing the endpoint to select its default result page.
 */
export async function listCyberWarnings(
  params: ListCyberWarningsParams = {},
): Promise<CyberWarningsResponse> {
  const { data } = await apiClient.get<CyberWarningsResponse>(
    '/admin/risk-control/cyber-warnings',
    {
      params,
    },
  )
  return data
}

/**
 * getCyberWarning retrieves cyber warning and returns the server response data.
 */
export async function getCyberWarning(id: number): Promise<ContentModerationCyberWarning> {
  const { data } = await apiClient.get<ContentModerationCyberWarning>(
    `/admin/risk-control/cyber-warnings/${id}`,
  )
  return data
}

/**
 * getMediaContent retrieves media content and returns the server response data.
 * The returned data is a Blob for download consumers. The function returns the
 * data portion of the completed response.
 */
export async function getMediaContent(id: number): Promise<Blob> {
  const { data } = await apiClient.get<Blob>(`/admin/risk-control/media/${id}/content`, {
    responseType: 'blob',
  })
  return data
}

/**
 * getCyberSummary retrieves aggregated upstream warnings for optional user,
 * provider and interval filters. An omitted query sends an empty object,
 * allowing the endpoint to select its default reporting scope.
 */
export async function getCyberSummary(params: ListCyberWarningsParams = {}): Promise<CyberSummary> {
  const { data } = await apiClient.get<CyberSummary>('/admin/risk-control/cyber-summary', {
    params,
  })
  return data
}

/**
 * unbanUser submits unban user and returns the server response data.
 */
export async function unbanUser(userID: number): Promise<ContentModerationUnbanUserResponse> {
  const { data } = await apiClient.post<ContentModerationUnbanUserResponse>(
    `/admin/risk-control/users/${userID}/unban`,
  )
  return data
}

/**
 * deleteFlaggedHash removes flagged hash and returns the server response data.
 */
export async function deleteFlaggedHash(inputHash: string): Promise<DeleteFlaggedHashResponse> {
  const { data } = await apiClient.delete<DeleteFlaggedHashResponse>('/admin/risk-control/hashes', {
    data: { input_hash: inputHash },
  })
  return data
}

/**
 * clearFlaggedHashes removes clear flagged hashes and returns the server
 * response data.
 */
export async function clearFlaggedHashes(): Promise<ClearFlaggedHashesResponse> {
  const { data } = await apiClient.delete<ClearFlaggedHashesResponse>(
    '/admin/risk-control/hashes/all',
  )
  return data
}

/**
 * riskControlAPI groups content moderation configuration and recorded outcomes
 * for callers using the module's named or default API object.
 */
export const riskControlAPI = {
  getConfig,
  updateConfig,
  getStatus,
  testAPIKeys,
  listLogs,
  getLog,
  listCyberWarnings,
  getCyberWarning,
  getMediaContent,
  getCyberSummary,
  unbanUser,
  deleteFlaggedHash,
  clearFlaggedHashes,
}

export default riskControlAPI
