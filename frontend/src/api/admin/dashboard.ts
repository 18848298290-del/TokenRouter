import { apiClient } from '../client'
import type {
  DashboardStats,
  UserUsageTrendPoint,
  UserSpendingRankingResponse,
  UserBreakdownItem,
} from '@/api/admin/dashboard.types'
import type { TrendDataPoint, ModelStat, GroupStat, UsageRequestType } from '@/api/usage.types'

/**
 * dashboard.ts retrieves administrator usage aggregates for dashboard displays.
 * Query types share date and ownership filters while allowing model-source
 * selection, and snapshot requests can select included sections so callers
 * receive the aggregates needed for their current view.
 */

/**
 * TrendParams selects a usage trend's date range, granularity and available
 * ownership filters.
 */
export interface TrendParams {
  start_date?: string // Requested start date.
  end_date?: string // Requested end date.
  granularity?: 'day' | 'hour' // Time bucket granularity.
  user_id?: number // Owning user identifier.
  api_key_id?: number // API-key record identifier.
  model?: string // Model identifier value.
  provider_id?: number // Provider record identifier.
  group_id?: number // Group record identifier.
  team_id?: number // Team record identifier.
  request_type?: UsageRequestType // Recorded request mode.
  stream?: boolean // Streaming request flag.
  billing_type?: number | null // Recorded billing classification.
  native_compaction_v2?: boolean | null // Native compaction v2.
}

/**
 * ModelStatsParams selects administrator model aggregates, including which model
 * identity source to group by.
 */
export interface ModelStatsParams {
  start_date?: string // Requested start date.
  end_date?: string // Requested end date.
  user_id?: number // Owning user identifier.
  api_key_id?: number // API-key record identifier.
  model?: string // Model identifier value.
  model_source?: 'requested' | 'upstream' | 'mapping' // Model identifier selection.
  provider_id?: number // Provider record identifier.
  group_id?: number // Group record identifier.
  team_id?: number // Team record identifier.
  request_type?: UsageRequestType // Recorded request mode.
  stream?: boolean // Streaming request flag.
  billing_type?: number | null // Recorded billing classification.
  native_compaction_v2?: boolean | null // Native compaction v2.
}

/**
 * ModelStatsResponse pairs model usage aggregates with the date range returned
 * by the statistics endpoint.
 */
export interface ModelStatsResponse {
  models: ModelStat[] // Model usage aggregates.
  start_date: string // Requested start date.
  end_date: string // Requested end date.
}

/**
 * getModelStats retrieves model usage statistics and returns the server result.
 */
export async function getModelStats(params?: ModelStatsParams): Promise<ModelStatsResponse> {
  const { data } = await apiClient.get<ModelStatsResponse>('/admin/dashboard/models', { params })
  return data
}

/**
 * DashboardSnapshotV2Params extends trend filters with switches selecting
 * included administrator dashboard aggregates.
 */
export interface DashboardSnapshotV2Params extends TrendParams {
  include_stats?: boolean // Requested include stats.
  include_trend?: boolean // Requested include trend.
  include_model_stats?: boolean // Include model stats.
  include_group_stats?: boolean // Include group stats.
  include_users_trend?: boolean // Include users trend.
  users_trend_limit?: number // Users trend limit.
}

/**
 * DashboardSnapshotV2Stats adds service uptime to the administrator dashboard's
 * shared statistics fields.
 */
export interface DashboardSnapshotV2Stats extends DashboardStats {
  uptime: number // Service uptime seconds.
}

/**
 * DashboardSnapshotV2Response collects requested dashboard sections with their
 * generation time and shared date range.
 */
export interface DashboardSnapshotV2Response {
  generated_at: string // Recorded generated at.
  start_date: string // Requested start date.
  end_date: string // Requested end date.
  granularity: string // Time bucket granularity.
  stats?: DashboardSnapshotV2Stats // Optional dashboard totals and uptime.
  trend?: TrendDataPoint[] // Usage time-series points.
  models?: ModelStat[] // Model usage aggregates.
  groups?: GroupStat[] // Group usage aggregates.
  users_trend?: UserUsageTrendPoint[] // Recorded users trend.
}

/**
 * UserBreakdownParams selects per-user ranking filters, ordering and result
 * limits for the administrator dashboard.
 */
export interface UserBreakdownParams {
  start_date?: string // Requested start date.
  end_date?: string // Requested end date.
  group_id?: number // Group record identifier.
  model?: string // Model identifier value.
  model_source?: 'requested' | 'upstream' | 'mapping' // Model identifier selection.
  endpoint?: string // Protocol endpoint path.
  endpoint_type?: 'inbound' | 'upstream' | 'path' // Requested endpoint type.
  limit?: number // Maximum returned entries.
  /**
   * The server validates this ranking column against its allowlist and uses
   * actual_cost when the supplied name is invalid.
   */
  sort_by?:
    | 'total_tokens'
    | 'input_tokens'
    | 'output_tokens'
    | 'cache_tokens'
    | 'requests'
    | 'cost'
    | 'actual_cost' // Requested sort column.
  // Additional filter conditions
  user_id?: number // Owning user identifier.
  api_key_id?: number // API-key record identifier.
  provider_id?: number // Provider record identifier.
  request_type?: UsageRequestType // Recorded request mode.
  stream?: boolean // Streaming request flag.
  billing_type?: number | null // Recorded billing classification.
  native_compaction_v2?: boolean | null // Native compaction v2.
}

/**
 * UserBreakdownResponse contains ranked user aggregates together with the
 * returned statistics date range.
 */
export interface UserBreakdownResponse {
  users: UserBreakdownItem[] // Reported user entries.
  start_date: string // Requested start date.
  end_date: string // Requested end date.
}

/**
 * getUserBreakdown retrieves user breakdown and returns the server response
 * data.
 */
export async function getUserBreakdown(
  params: UserBreakdownParams,
): Promise<UserBreakdownResponse> {
  const { data } = await apiClient.get<UserBreakdownResponse>('/admin/dashboard/user-breakdown', {
    params,
  })
  return data
}

/**
 * getSnapshotV2 retrieves dashboard snapshot v2 (aggregated response for heavy
 * admin pages).
 */
export async function getSnapshotV2(
  params?: DashboardSnapshotV2Params,
): Promise<DashboardSnapshotV2Response> {
  const { data } = await apiClient.get<DashboardSnapshotV2Response>(
    '/admin/dashboard/snapshot-v2',
    {
      params,
    },
  )
  return data
}

/**
 * UserTrendParams adds a user-series limit to the administrator dashboard's
 * trend filters.
 */
export interface UserTrendParams extends TrendParams {
  limit?: number // Maximum returned entries.
}

/**
 * UserTrendResponse contains per-user trend points with their interval and
 * bucket granularity.
 */
export interface UserTrendResponse {
  trend: UserUsageTrendPoint[] // Usage time-series points.
  start_date: string // Requested start date.
  end_date: string // Requested end date.
  granularity: string // Time bucket granularity.
}

/**
 * UserSpendingRankingParams selects the spending-ranking date range and maximum
 * number of returned users.
 */
export interface UserSpendingRankingParams extends Pick<TrendParams, 'start_date' | 'end_date'> {
  limit?: number // Maximum returned entries.
}

/**
 * getUserUsageTrend retrieves user usage trend data and returns the server
 * result.
 */
export async function getUserUsageTrend(params?: UserTrendParams): Promise<UserTrendResponse> {
  const { data } = await apiClient.get<UserTrendResponse>('/admin/dashboard/users-trend', {
    params,
  })
  return data
}

/**
 * getUserSpendingRanking retrieves user spending ranking data and returns the
 * server result.
 */
export async function getUserSpendingRanking(
  params?: UserSpendingRankingParams,
): Promise<UserSpendingRankingResponse> {
  const { data } = await apiClient.get<UserSpendingRankingResponse>(
    '/admin/dashboard/users-ranking',
    {
      params,
    },
  )
  return data
}

/**
 * PlatformUsage reports today's and accumulated deducted costs for one provider
 * platform.
 */
export interface PlatformUsage {
  platform: string // Provider platform identifier.
  today_actual_cost: number // Today actual cost.
  total_actual_cost: number // Total actual cost.
}

/**
 * BatchUserUsageStats pairs a user's deducted-cost totals with optional
 * per-platform breakdowns.
 */
export interface BatchUserUsageStats {
  user_id: number // Owning user identifier.
  today_actual_cost: number // Today actual cost.
  total_actual_cost: number // Total actual cost.
  by_platform?: PlatformUsage[] // Recorded by platform.
}

/**
 * BatchUsersUsageResponse maps stringified user identifiers to their requested
 * usage summary records.
 */
export interface BatchUsersUsageResponse {
  stats: Record<string, BatchUserUsageStats> // Identifier-indexed usage summaries.
}

/**
 * getBatchUsersUsage retrieves batch usage stats for multiple users.
 */
export async function getBatchUsersUsage(userIds: number[]): Promise<BatchUsersUsageResponse> {
  const { data } = await apiClient.post<BatchUsersUsageResponse>('/admin/dashboard/users-usage', {
    user_ids: userIds,
  })
  return data
}

/**
 * dashboardAPI groups administrator usage dashboard aggregates for callers using
 * the module's named or default API object.
 */
export const dashboardAPI = {
  getModelStats,
  getSnapshotV2,
  getUserUsageTrend,
  getUserSpendingRanking,
  getBatchUsersUsage,
}

export default dashboardAPI
