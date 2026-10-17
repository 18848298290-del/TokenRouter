/**
 * Admin Ops API endpoints (vNext)
 * - Error logs list/detail
 * - Dashboard overview (raw path)
 */

import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export interface OpsRequestOptions {
  signal?: AbortSignal
}

export interface OpsDashboardOverview {
  start_time: string
  end_time: string
  platform: string
  group_id?: number | null

  health_score?: number

  system_metrics?: OpsSystemMetricsSnapshot | null
  job_heartbeats?: OpsJobHeartbeat[] | null

  success_count: number
  error_count_total: number
  business_limited_count: number
  error_count_sla: number
  request_count_total: number
  request_count_sla: number

  token_consumed: number

  sla: number
  error_rate: number
  upstream_error_rate: number
  upstream_error_count_excl_429_529: number
  upstream_429_count: number
  upstream_529_count: number

  qps: {
    current: number
    peak: number
    avg: number
  }
  tps: {
    current: number
    peak: number
    avg: number
  }

  duration: OpsPercentiles
  ttft: OpsPercentiles
}

export interface OpsPercentiles {
  p50_ms?: number | null
  p90_ms?: number | null
  p95_ms?: number | null
  p99_ms?: number | null
  avg_ms?: number | null
  max_ms?: number | null
}

export interface OpsThroughputTrendPoint {
  bucket_start: string
  request_count: number
  token_consumed: number
  switch_count?: number
  qps: number
  tps: number
}

export interface OpsThroughputPlatformBreakdownItem {
  platform: string
  request_count: number
  token_consumed: number
}

export interface OpsThroughputGroupBreakdownItem {
  group_id: number
  group_name: string
  request_count: number
  token_consumed: number
}

export interface OpsThroughputTrendResponse {
  bucket: string
  points: OpsThroughputTrendPoint[]
  by_platform?: OpsThroughputPlatformBreakdownItem[]
  top_groups?: OpsThroughputGroupBreakdownItem[]
}

export type OpsRequestKind = 'success' | 'error'
export type OpsRequestDetailsKind = OpsRequestKind | 'all'
export type OpsRequestDetailsSort = 'created_at_desc' | 'duration_desc'

export interface OpsRequestDetail {
  kind: OpsRequestKind
  created_at: string
  request_id: string

  platform?: string
  model?: string
  duration_ms?: number | null
  status_code?: number | null

  error_id?: number | null
  phase?: string
  severity?: string
  message?: string

  user_id?: number | null
  api_key_id?: number | null
  provider_id?: number | null
  group_id?: number | null

  stream?: boolean
}

export interface OpsRequestDetailsParams {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string

  kind?: OpsRequestDetailsKind
  sla_only?: '1' | '0'

  platform?: string
  group_id?: number | null

  user_id?: number
  api_key_id?: number
  provider_id?: number

  model?: string
  request_id?: string
  q?: string

  min_duration_ms?: number
  max_duration_ms?: number

  sort?: OpsRequestDetailsSort

  page?: number
  page_size?: number
}

export type OpsRequestDetailsResponse = PaginatedResponse<OpsRequestDetail>

export interface OpsLatencyHistogramBucket {
  range: string
  count: number
}

export interface OpsLatencyHistogramResponse {
  start_time: string
  end_time: string
  platform: string
  group_id?: number | null
  bucket_boundaries_ms: number[]

  total_requests: number
  buckets: OpsLatencyHistogramBucket[]
}

export interface OpsLatencyHistogramParams {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  bucket_boundaries_ms?: number[]
}

export interface OpsErrorTrendPoint {
  bucket_start: string
  error_count_total: number
  business_limited_count: number
  error_count_sla: number
  upstream_error_count_excl_429_529: number
  upstream_429_count: number
  upstream_529_count: number
}

export interface OpsErrorTrendResponse {
  bucket: string
  points: OpsErrorTrendPoint[]
}

export interface OpsErrorDistributionItem {
  status_code: number
  total: number
  sla: number
  business_limited: number
}

export interface OpsErrorDistributionResponse {
  total: number
  items: OpsErrorDistributionItem[]
}

export interface OpsDashboardSnapshotV2Response {
  generated_at: string
  overview: OpsDashboardOverview
  throughput_trend: OpsThroughputTrendResponse
  error_trend: OpsErrorTrendResponse
}

export type OpsTokenStatsTimeRange = '30m' | '1h' | '1d' | '15d' | '30d'

export interface OpsTokenStatsItem {
  model: string
  request_count: number
  avg_tokens_per_sec?: number | null
  avg_ttft_ms?: number | null
  total_output_tokens: number
  avg_duration_ms: number
  requests_with_first_token: number
}

export interface OpsTokenStatsResponse {
  time_range: OpsTokenStatsTimeRange
  start_time: string
  end_time: string
  platform?: string
  group_id?: number | null
  items: OpsTokenStatsItem[]
  total: number
  page?: number
  page_size?: number
  top_n?: number | null
}

export interface OpsTokenStatsParams {
  time_range?: OpsTokenStatsTimeRange
  platform?: string
  group_id?: number | null
  page?: number
  page_size?: number
  top_n?: number
}

export interface OpsSystemMetricsSnapshot {
  id: number
  created_at: string
  window_minutes: number

  cpu_usage_percent?: number | null
  memory_used_mb?: number | null
  memory_total_mb?: number | null
  memory_usage_percent?: number | null
  disk_used_mb?: number | null
  disk_total_mb?: number | null
  disk_usage_percent?: number | null

  db_ok?: boolean | null
  redis_ok?: boolean | null

  // Config-derived limits (best-effort) for rendering "current vs max".
  db_max_open_conns?: number | null
  redis_pool_size?: number | null

  redis_conn_total?: number | null
  redis_conn_idle?: number | null

  db_conn_active?: number | null
  db_conn_idle?: number | null
  db_conn_waiting?: number | null

  goroutine_count?: number | null
  concurrency_queue_depth?: number | null
  provider_switch_count?: number | null
}

export interface OpsJobHeartbeat {
  job_name: string
  last_run_at?: string | null
  last_success_at?: string | null
  last_error_at?: string | null
  last_error?: string | null
  last_duration_ms?: number | null
  last_result?: string | null
  updated_at: string
}

export interface PlatformConcurrencyInfo {
  platform: string
  current_in_use: number
  max_capacity: number
  load_percentage: number
  waiting_in_queue: number
}

export interface GroupConcurrencyInfo {
  group_id: number
  group_name: string
  current_in_use: number
  max_capacity: number
  load_percentage: number
  waiting_in_queue: number
}

export interface ProviderConcurrencyInfo {
  provider_id: number
  provider_name?: string
  platform: string
  group_id: number
  group_name: string
  current_in_use: number
  max_capacity: number
  load_percentage: number
  waiting_in_queue: number
}

export interface OpsConcurrencyStatsResponse {
  enabled: boolean
  platform: Record<string, PlatformConcurrencyInfo>
  group: Record<string, GroupConcurrencyInfo>
  provider: Record<string, ProviderConcurrencyInfo>
  timestamp?: string
}

export interface UserConcurrencyInfo {
  user_id: number
  user_email: string
  username: string
  current_in_use: number
  max_capacity: number
  load_percentage: number
  waiting_in_queue: number
}

export interface OpsUserConcurrencyStatsResponse {
  enabled: boolean
  user: Record<string, UserConcurrencyInfo>
  timestamp?: string
}

export async function getConcurrencyStats(platform?: string, groupId?: number | null): Promise<OpsConcurrencyStatsResponse> {
  const params: Record<string, any> = {}
  if (platform) {
    params.platform = platform
  }
  if (typeof groupId === 'number' && groupId > 0) {
    params.group_id = groupId
  }

  const { data } = await apiClient.get<OpsConcurrencyStatsResponse>('/admin/ops/concurrency', { params })
  return data
}

export async function getUserConcurrencyStats(): Promise<OpsUserConcurrencyStatsResponse> {
  const { data } = await apiClient.get<OpsUserConcurrencyStatsResponse>('/admin/ops/user-concurrency')
  return data
}

export interface PlatformAvailability {
  platform: string
  total_providers: number
  available_count: number
  rate_limit_count: number
  error_count: number
}

export interface GroupAvailability {
  group_id: number
  group_name: string
  total_providers: number
  available_count: number
  rate_limit_count: number
  error_count: number
}

export interface ProviderAvailability {
  provider_id: number
  provider_name: string
  platform: string
  group_id: number
  group_name: string
  status: string
  is_available: boolean
  is_rate_limited: boolean
  rate_limit_reset_at?: string
  rate_limit_remaining_sec?: number
  is_overloaded: boolean
  overload_until?: string
  overload_remaining_sec?: number
  has_error: boolean
  error_message?: string
}

export interface OpsProviderAvailabilityStatsResponse {
  enabled: boolean
  platform: Record<string, PlatformAvailability>
  group: Record<string, GroupAvailability>
  provider: Record<string, ProviderAvailability>
  timestamp?: string
}

export async function getProviderAvailabilityStats(platform?: string, groupId?: number | null): Promise<OpsProviderAvailabilityStatsResponse> {
  const params: Record<string, any> = {}
  if (platform) {
    params.platform = platform
  }
  if (typeof groupId === 'number' && groupId > 0) {
    params.group_id = groupId
  }
  const { data } = await apiClient.get<OpsProviderAvailabilityStatsResponse>('/admin/ops/provider-availability', { params })
  return data
}

export interface OpsRateSummary {
  current: number
  peak: number
  avg: number
}

export interface OpsRealtimeTrafficSummary {
  window: string
  start_time: string
  end_time: string
  platform: string
  group_id?: number | null
  qps: OpsRateSummary
  tps: OpsRateSummary
}

export interface OpsRealtimeTrafficSummaryResponse {
  enabled: boolean
  summary: OpsRealtimeTrafficSummary | null
  timestamp?: string
}

export async function getRealtimeTrafficSummary(
  window: string,
  platform?: string,
  groupId?: number | null
): Promise<OpsRealtimeTrafficSummaryResponse> {
  const params: Record<string, any> = { window }
  if (platform) {
    params.platform = platform
  }
  if (typeof groupId === 'number' && groupId > 0) {
    params.group_id = groupId
  }

  const { data } = await apiClient.get<OpsRealtimeTrafficSummaryResponse>('/admin/ops/realtime-traffic', { params })
  return data
}

export type OpsSeverity = string
export type OpsPhase = string

export type AlertSeverity = 'critical' | 'warning' | 'info'
export type MetricType =
  | 'success_rate'
  | 'error_rate'
  | 'upstream_error_rate'
  | 'cpu_usage_percent'
  | 'memory_usage_percent'
  | 'disk_usage_percent'
  | 'concurrency_queue_depth'
  | 'group_available_providers'
  | 'group_available_ratio'
  | 'group_rate_limit_ratio'
  | 'provider_rate_limited_count'
  | 'provider_error_count'
  | 'provider_error_ratio'
  | 'overload_provider_count'
export type Operator = '>' | '>=' | '<' | '<=' | '==' | '!='

export interface AlertRule {
  id?: number
  name: string
  description?: string
  enabled: boolean
  metric_type: MetricType
  operator: Operator
  threshold: number
  window_minutes: number
  sustained_minutes: number
  severity: OpsSeverity
  cooldown_minutes: number
  notify_email: boolean
  filters?: Record<string, any>
  created_at?: string
  updated_at?: string
  last_triggered_at?: string | null
}

export interface AlertEvent {
  id: number
  rule_id: number
  severity: OpsSeverity | string
  status: 'firing' | 'resolved' | 'manual_resolved' | string
  title?: string
  description?: string
  metric_value?: number
  threshold_value?: number
  dimensions?: Record<string, any>
  fired_at: string
  resolved_at?: string | null
  email_sent: boolean
  created_at: string
}

export interface EmailNotificationConfig {
  alert: {
    enabled: boolean
    recipients: string[]
    min_severity: AlertSeverity | ''
    rate_limit_per_hour: number
    batching_window_seconds: number
    include_resolved_alerts: boolean
  }
  report: {
    enabled: boolean
    recipients: string[]
    daily_summary_enabled: boolean
    daily_summary_schedule: string
    weekly_summary_enabled: boolean
    weekly_summary_schedule: string
    error_digest_enabled: boolean
    error_digest_schedule: string
    error_digest_min_count: number
    provider_health_enabled: boolean
    provider_health_schedule: string
    provider_health_error_rate_threshold: number
  }
}

export interface OpsMetricThresholds {
  sla_percent_min?: number | null                 // SLA低于此值变红
  ttft_p99_ms_max?: number | null                 // TTFT P99高于此值变红
  request_error_rate_percent_max?: number | null  // 请求错误率高于此值变红
  upstream_error_rate_percent_max?: number | null // 上游错误率高于此值变红
}

export interface OpsDistributedLockSettings {
  enabled: boolean
  key: string
  ttl_seconds: number
}

export interface OpsAlertRuntimeSettings {
  evaluation_interval_seconds: number
  distributed_lock: OpsDistributedLockSettings
  silencing: {
    enabled: boolean
    global_until_rfc3339: string
    global_reason: string
    entries?: Array<{
      rule_id?: number
      severities?: Array<OpsSeverity | string>
      until_rfc3339: string
      reason: string
    }>
  }
  thresholds: OpsMetricThresholds // 指标阈值配置
}

export interface OpsOpenAIProviderQuotaAutoPauseSettings {
  default_threshold_5h: number // 0~1，0 表示不启用全局默认 5h 阈值
  default_threshold_7d: number // 0~1，0 表示不启用全局默认 7d 阈值
}

export interface OpsAdvancedSettings {
  data_retention: OpsDataRetentionSettings
  openai_provider_quota_auto_pause: OpsOpenAIProviderQuotaAutoPauseSettings
  ignore_count_tokens_errors: boolean
  ignore_context_canceled: boolean
  ignore_no_available_providers: boolean
  ignore_invalid_api_key_errors: boolean
  ignore_insufficient_balance_errors: boolean
  ignored_status_codes: number[]
  display_openai_token_stats: boolean
  display_alert_events: boolean
  auto_refresh_enabled: boolean
  auto_refresh_interval_seconds: number
}

export interface OpsDataRetentionSettings {
  cleanup_enabled: boolean
  cleanup_schedule: string
  cleanup_batch_size: number
  cleanup_pause_ms: number
  error_log_retention_days: number
  minute_metrics_retention_days: number
  hourly_metrics_retention_days: number
}

export interface OpsRuntimeLogConfig {
  level: 'debug' | 'info' | 'warn' | 'error'
  enable_sampling: boolean
  sampling_initial: number
  sampling_thereafter: number
  caller: boolean
  stacktrace_level: 'none' | 'error' | 'fatal'
  retention_days: number
  source?: string
  updated_at?: string
  updated_by_user_id?: number
}

export interface OpsSystemLog {
  id: number
  created_at: string
  host: string
  level: string
  component: string
  message: string
  request_id?: string
  client_request_id?: string
  user_id?: number | null
  api_key_id?: number | null
  provider_id?: number | null
  platform?: string
  model?: string
  extra?: Record<string, any>
}

export type OpsSystemLogListResponse = PaginatedResponse<OpsSystemLog>

export interface OpsSystemLogQuery {
  page?: number
  page_size?: number
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h' | '7d' | '30d'
  start_time?: string
  end_time?: string
  host?: string
  level?: string
  component?: string
  request_id?: string
  client_request_id?: string
  user_id?: number | null
  api_key_id?: number | null
  provider_id?: number | null
  platform?: string
  model?: string
  q?: string
}

export interface OpsSystemLogCleanupRequest {
  start_time?: string
  end_time?: string
  host?: string
  level?: string
  component?: string
  request_id?: string
  client_request_id?: string
  user_id?: number | null
  api_key_id?: number | null
  provider_id?: number | null
  platform?: string
  model?: string
  q?: string
}

export interface OpsSystemLogSinkHealth {
  queue_depth: number
  queue_capacity: number
  dropped_count: number
  write_failed_count: number
  written_count: number
  avg_write_delay_ms: number
  last_error?: string
}

export interface OpsErrorLog {
  id: number
  created_at: string

  // Standardized classification
  phase: OpsPhase
  type: string
  error_owner: 'client' | 'provider' | 'platform' | string
  error_source: 'client_request' | 'upstream_http' | 'gateway' | string

  severity: OpsSeverity
  status_code: number
  platform: string
  model: string

  resolved: boolean
  resolved_at?: string | null
  resolved_by_user_id?: number | null

  client_request_id: string
  request_id: string
  message: string

  user_id?: number | null
  user_email: string
  api_key_id?: number | null
  // 关联 api_key 名称（后端 LEFT JOIN api_keys；软删保留 name，故已删 key 仍有原名）。
  api_key_name?: string
  api_key_deleted?: boolean
  provider_id?: number | null
  provider_name: string
  group_id?: number | null
  group_name: string

  client_ip?: string | null
  request_path?: string
  stream?: boolean

  // Error observability context (endpoint + model mapping)
  inbound_endpoint?: string
  upstream_endpoint?: string
  requested_model?: string
  upstream_model?: string
  request_type?: number | null
  user_agent?: string

}

export interface OpsErrorDetail extends OpsErrorLog {
  error_body: string

  // Upstream context (optional; enriched by gateway services)
  upstream_status_code?: number | null
  upstream_error_message?: string
  upstream_error_detail?: string
  upstream_errors?: string

  auth_latency_ms?: number | null
  routing_latency_ms?: number | null
  upstream_latency_ms?: number | null
  response_latency_ms?: number | null

  is_business_limited: boolean

  // 有效且未删除的 Key 在错误发生时保存的前缀快照
  api_key_prefix?: string | null
}

export type OpsErrorLogsResponse = PaginatedResponse<OpsErrorLog>

export async function getDashboardOverview(
  params: {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  },
  options: OpsRequestOptions = {}
): Promise<OpsDashboardOverview> {
  const { data } = await apiClient.get<OpsDashboardOverview>('/admin/ops/dashboard/overview', {
    params,
    signal: options.signal
  })
  return data
}

export async function getDashboardSnapshotV2(
  params: {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  },
  options: OpsRequestOptions = {}
): Promise<OpsDashboardSnapshotV2Response> {
  const { data } = await apiClient.get<OpsDashboardSnapshotV2Response>('/admin/ops/dashboard/snapshot-v2', {
    params,
    signal: options.signal
  })
  return data
}

export async function getThroughputTrend(
  params: {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  },
  options: OpsRequestOptions = {}
): Promise<OpsThroughputTrendResponse> {
  const { data } = await apiClient.get<OpsThroughputTrendResponse>('/admin/ops/dashboard/throughput-trend', {
    params,
    signal: options.signal
  })
  return data
}

export async function getLatencyHistogram(
  params: OpsLatencyHistogramParams,
  options: OpsRequestOptions = {}
): Promise<OpsLatencyHistogramResponse> {
  const { bucket_boundaries_ms: boundaries, ...filterParams } = params
  const { data } = await apiClient.get<OpsLatencyHistogramResponse>('/admin/ops/dashboard/latency-histogram', {
    params: {
      ...filterParams,
      bucket_boundaries_ms: boundaries?.join(',')
    },
    signal: options.signal
  })
  return data
}

export async function getErrorTrend(
  params: {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  },
  options: OpsRequestOptions = {}
): Promise<OpsErrorTrendResponse> {
  const { data } = await apiClient.get<OpsErrorTrendResponse>('/admin/ops/dashboard/error-trend', {
    params,
    signal: options.signal
  })
  return data
}

export async function getErrorDistribution(
  params: {
  time_range?: '5m' | '30m' | '1h' | '6h' | '24h'
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  },
  options: OpsRequestOptions = {}
): Promise<OpsErrorDistributionResponse> {
  const { data } = await apiClient.get<OpsErrorDistributionResponse>('/admin/ops/dashboard/error-distribution', {
    params,
    signal: options.signal
  })
  return data
}

export async function getTokenStats(
  params: OpsTokenStatsParams,
  options: OpsRequestOptions = {}
): Promise<OpsTokenStatsResponse> {
  const { data } = await apiClient.get<OpsTokenStatsResponse>('/admin/ops/dashboard/token-stats', {
    params,
    signal: options.signal
  })
  return data
}

export type OpsErrorListView = 'errors' | 'excluded' | 'all'

export type OpsErrorListQueryParams = {
 request_id?: string
  page?: number
  page_size?: number
  time_range?: string
  start_time?: string
  end_time?: string
  platform?: string
  group_id?: number | null
  provider_id?: number | null
  user_id?: number
  api_key_id?: number
  // 模型过滤：后端以 COALESCE(requested_model, model) 精确匹配（admin 路径）。
  model?: string

  phase?: string
  // 分类(用户侧粗分类码,如 auth/rate_limit/upstream),后端反查为 phase/type ANY 条件
  category?: string
  error_owner?: string
  error_source?: string
  resolved?: string
  view?: OpsErrorListView

  q?: string
  status_codes?: string
  status_codes_other?: string

  // 服务端排序,列白名单见后端 opsErrorLogsOrderBy(created_at/model/status_code)
  sort_by?: string
  sort_order?: 'asc' | 'desc'
}

// Legacy unified endpoints
export async function listErrorLogs(params: OpsErrorListQueryParams): Promise<OpsErrorLogsResponse> {
  const { data } = await apiClient.get<OpsErrorLogsResponse>('/admin/ops/errors', { params })
  return data
}

// New split endpoints
export async function listRequestErrors(params: OpsErrorListQueryParams): Promise<OpsErrorLogsResponse> {
  const { data } = await apiClient.get<OpsErrorLogsResponse>('/admin/ops/request-errors', { params })
  return data
}

export async function listUpstreamErrors(params: OpsErrorListQueryParams): Promise<OpsErrorLogsResponse> {
  const { data } = await apiClient.get<OpsErrorLogsResponse>('/admin/ops/upstream-errors', { params })
  return data
}

export async function getRequestErrorDetail(id: number): Promise<OpsErrorDetail> {
  const { data } = await apiClient.get<OpsErrorDetail>(`/admin/ops/request-errors/${id}`)
  return data
}

export async function getUpstreamErrorDetail(id: number): Promise<OpsErrorDetail> {
  const { data } = await apiClient.get<OpsErrorDetail>(`/admin/ops/upstream-errors/${id}`)
  return data
}

export async function listRequestErrorUpstreamErrors(
  id: number,
  params: OpsErrorListQueryParams = {},
  options: { include_detail?: boolean } = {}
): Promise<PaginatedResponse<OpsErrorDetail>> {
  const query: Record<string, any> = { ...params }
  if (options.include_detail) query.include_detail = '1'
  const { data } = await apiClient.get<PaginatedResponse<OpsErrorDetail>>(`/admin/ops/request-errors/${id}/upstream-errors`, { params: query })
  return data
}

export async function listRequestDetails(params: OpsRequestDetailsParams): Promise<OpsRequestDetailsResponse> {
  const { data } = await apiClient.get<OpsRequestDetailsResponse>('/admin/ops/requests', { params })
  return data
}

// Alert rules
export async function listAlertRules(): Promise<AlertRule[]> {
  const { data } = await apiClient.get<AlertRule[]>('/admin/ops/alert-rules')
  return data
}

export async function createAlertRule(rule: AlertRule): Promise<AlertRule> {
  const { data } = await apiClient.post<AlertRule>('/admin/ops/alert-rules', rule)
  return data
}

export async function updateAlertRule(id: number, rule: Partial<AlertRule>): Promise<AlertRule> {
  const { data } = await apiClient.put<AlertRule>(`/admin/ops/alert-rules/${id}`, rule)
  return data
}

export async function deleteAlertRule(id: number): Promise<void> {
  await apiClient.delete(`/admin/ops/alert-rules/${id}`)
}

export interface AlertEventsQuery {
  limit?: number
  status?: string
  severity?: string
  email_sent?: boolean
  time_range?: string
  start_time?: string
  end_time?: string
  before_fired_at?: string
  before_id?: number
  platform?: string
  group_id?: number
}

export async function listAlertEvents(params: AlertEventsQuery = {}): Promise<AlertEvent[]> {
  const { data } = await apiClient.get<AlertEvent[]>('/admin/ops/alert-events', { params })
  return data
}

export async function getAlertEvent(id: number): Promise<AlertEvent> {
  const { data } = await apiClient.get<AlertEvent>(`/admin/ops/alert-events/${id}`)
  return data
}

export async function updateAlertEventStatus(id: number, status: 'resolved' | 'manual_resolved'): Promise<void> {
  await apiClient.put(`/admin/ops/alert-events/${id}/status`, { status })
}

export async function createAlertSilence(payload: {
  rule_id: number
  platform: string
  group_id?: number | null
  region?: string | null
  until: string
  reason?: string
}): Promise<void> {
  await apiClient.post('/admin/ops/alert-silences', payload)
}

// Email notification config
export async function getEmailNotificationConfig(): Promise<EmailNotificationConfig> {
  const { data } = await apiClient.get<EmailNotificationConfig>('/admin/ops/email-notification/config')
  return data
}

export async function updateEmailNotificationConfig(config: EmailNotificationConfig): Promise<EmailNotificationConfig> {
  const { data } = await apiClient.put<EmailNotificationConfig>('/admin/ops/email-notification/config', config)
  return data
}

// Runtime settings (DB-backed)
export async function getAlertRuntimeSettings(): Promise<OpsAlertRuntimeSettings> {
  const { data } = await apiClient.get<OpsAlertRuntimeSettings>('/admin/ops/runtime/alert')
  return data
}

export async function updateAlertRuntimeSettings(config: OpsAlertRuntimeSettings): Promise<OpsAlertRuntimeSettings> {
  const { data } = await apiClient.put<OpsAlertRuntimeSettings>('/admin/ops/runtime/alert', config)
  return data
}

export async function getRuntimeLogConfig(): Promise<OpsRuntimeLogConfig> {
  const { data } = await apiClient.get<OpsRuntimeLogConfig>('/admin/ops/runtime/logging')
  return data
}

export async function updateRuntimeLogConfig(config: OpsRuntimeLogConfig): Promise<OpsRuntimeLogConfig> {
  const { data } = await apiClient.put<OpsRuntimeLogConfig>('/admin/ops/runtime/logging', config)
  return data
}

export async function resetRuntimeLogConfig(): Promise<OpsRuntimeLogConfig> {
  const { data } = await apiClient.post<OpsRuntimeLogConfig>('/admin/ops/runtime/logging/reset')
  return data
}

export async function listSystemLogs(params: OpsSystemLogQuery): Promise<OpsSystemLogListResponse> {
  const { data } = await apiClient.get<OpsSystemLogListResponse>('/admin/ops/system-logs', { params })
  return data
}

export async function cleanupSystemLogs(payload: OpsSystemLogCleanupRequest): Promise<{ deleted: number }> {
  const { data } = await apiClient.post<{ deleted: number }>('/admin/ops/system-logs/cleanup', payload)
  return data
}

export async function getSystemLogSinkHealth(): Promise<OpsSystemLogSinkHealth> {
  const { data } = await apiClient.get<OpsSystemLogSinkHealth>('/admin/ops/system-logs/health')
  return data
}

// Advanced settings (DB-backed)
export async function getAdvancedSettings(): Promise<OpsAdvancedSettings> {
  const { data } = await apiClient.get<OpsAdvancedSettings>('/admin/ops/advanced-settings')
  return data
}

export async function updateAdvancedSettings(config: OpsAdvancedSettings): Promise<OpsAdvancedSettings> {
  const { data } = await apiClient.put<OpsAdvancedSettings>('/admin/ops/advanced-settings', config)
  return data
}

// ==================== Metric Thresholds ====================

async function getMetricThresholds(): Promise<OpsMetricThresholds> {
  const { data } = await apiClient.get<OpsMetricThresholds>('/admin/ops/settings/metric-thresholds')
  return data
}

async function updateMetricThresholds(thresholds: OpsMetricThresholds): Promise<void> {
  await apiClient.put('/admin/ops/settings/metric-thresholds', thresholds)
}

export const opsAPI = {
  getDashboardSnapshotV2,
  getDashboardOverview,
  getThroughputTrend,
  getLatencyHistogram,
  getErrorTrend,
  getErrorDistribution,
  getTokenStats,
  getConcurrencyStats,
  getUserConcurrencyStats,
  getProviderAvailabilityStats,
  getRealtimeTrafficSummary,

  // Legacy unified endpoints
  listErrorLogs,

  // New split endpoints
  listRequestErrors,
  listUpstreamErrors,
  getRequestErrorDetail,
  getUpstreamErrorDetail,
  listRequestErrorUpstreamErrors,

  listRequestDetails,
  listAlertRules,
  createAlertRule,
  updateAlertRule,
  deleteAlertRule,
  listAlertEvents,
  getAlertEvent,
  updateAlertEventStatus,
  createAlertSilence,
  getEmailNotificationConfig,
  updateEmailNotificationConfig,
  getAlertRuntimeSettings,
  updateAlertRuntimeSettings,
  getRuntimeLogConfig,
  updateRuntimeLogConfig,
  resetRuntimeLogConfig,
  getAdvancedSettings,
  updateAdvancedSettings,
  getMetricThresholds,
  updateMetricThresholds,
  listSystemLogs,
  cleanupSystemLogs,
  getSystemLogSinkHealth
}

export default opsAPI
