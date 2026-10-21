import { apiClient } from '../client'
import type { AdminUser, UpdateUserRequest } from '@/api/admin/users.types'
import type { PaginatedResponse } from '@/api/common.types'
import type { ApiKey } from '@/api/keys.types'

/**
 * users.ts sends administrator user maintenance and identity-binding requests.
 * List construction expands nonempty custom attributes into bracketed query
 * keys, while balance, limits, group replacement and identity binding use
 * dedicated payloads and return their endpoint-specific results.
 */

/**
 * AdminBindAuthIdentityChannelRequest supplies a channel identity and optional
 * metadata for an administrator identity binding.
 */
export interface AdminBindAuthIdentityChannelRequest {
  channel: string // Authentication channel details.
  channel_app_id: string // Channel application identifier.
  channel_subject: string // Channel-scoped subject identifier.
  metadata?: Record<string, unknown> | null // Additional structured metadata.
}

/**
 * AdminBindAuthIdentityRequest supplies provider identity and optional channel
 * data for binding an administrator-selected user.
 */
export interface AdminBindAuthIdentityRequest {
  provider_type: string // Provider credential type.
  provider_key: string // Configured identity-provider key.
  provider_subject: string // Provider-scoped subject identifier.
  issuer?: string | null // Authentication issuer identity.
  metadata?: Record<string, unknown> | null // Additional structured metadata.
  channel?: AdminBindAuthIdentityChannelRequest // Authentication channel details.
}

/**
 * AdminBoundAuthIdentityChannel describes a stored channel identity with
 * metadata and its creation or update timestamps.
 */
export interface AdminBoundAuthIdentityChannel {
  channel: string // Authentication channel details.
  channel_app_id: string // Channel application identifier.
  channel_subject: string // Channel-scoped subject identifier.
  metadata: Record<string, unknown> | null // Additional structured metadata.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
}

/**
 * AdminBoundAuthIdentity describes a user's stored provider identity,
 * verification state and optional channel record.
 */
export interface AdminBoundAuthIdentity {
  user_id: number // Owning user identifier.
  provider_type: string // Provider credential type.
  provider_key: string // Configured identity-provider key.
  provider_subject: string // Provider-scoped subject identifier.
  verified_at?: string | null // Recorded verified at.
  issuer?: string | null // Authentication issuer identity.
  metadata: Record<string, unknown> | null // Additional structured metadata.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
  channel?: AdminBoundAuthIdentityChannel | null // Authentication channel details.
}

/**
 * BatchUpdateUserLimitsRequest selects users and the limit fields to change. A
 * supplied zero remains a requested limit value, while omitted concurrency or
 * RPM fields stay absent from the submitted payload.
 */
export interface BatchUpdateUserLimitsRequest {
  user_ids: number[] // Requested user ids.
  /**
   * This selection flag accompanies user_ids in the submitted request; the
   * server determines the selected user set for the batch operation.
   */
  all?: boolean // All-users selection flag.
  /**
   * When present this is submitted as the requested concurrency limit; omission
   * keeps the field out of the batch payload.
   */
  concurrency?: number // Concurrent request limit.
  /**
   * When present this is submitted as the requested RPM limit, including zero;
   * omission keeps it out of the batch payload.
   */
  rpm_limit?: number // Configured requests-per-minute limit.
}

/**
 * BatchUpdateUserLimitsResponse reports how many users the requested batch limit
 * update affected.
 */
export interface BatchUpdateUserLimitsResponse {
  affected: number // Affected record count.
}

/**
 * list retrieves an administrator user page with optional filters and
 * cancellation. Page and size default to one and twenty; nonempty custom
 * attribute values become attr[id] query entries, while empty attribute values
 * are omitted.
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    status?: 'active' | 'disabled' // Current server-reported status.
    role?: 'admin' | 'user' // Selected account role.
    search?: string // Free-text search query.
    group_name?: string // fuzzy filter by allowed group name
    api_key_group_id?: number /**
     * Filter users by the group bound to their API keys.
     */
    attributes?: Record<number, string> // attributeId -> value
    include_subscriptions?: boolean // Recorded include subscriptions.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<PaginatedResponse<AdminUser>> {
  // Build params with attribute filters in attr[id]=value format
  const params: Record<string, any> = {
    page,
    page_size: pageSize,
    status: filters?.status,
    role: filters?.role,
    search: filters?.search,
    group_name: filters?.group_name,
    api_key_group_id: filters?.api_key_group_id,
    include_subscriptions: filters?.include_subscriptions,
    sort_by: filters?.sort_by,
    sort_order: filters?.sort_order,
  }

  // Add attribute filters as attr[id]=value
  if (filters?.attributes) {
    for (const [attrId, value] of Object.entries(filters.attributes)) {
      if (value) {
        params[`attr[${attrId}]`] = value
      }
    }
  }
  const { data } = await apiClient.get<PaginatedResponse<AdminUser>>('/admin/users', {
    params,
    signal: options?.signal,
  })
  return data
}

/**
 * getById retrieves an administrator user record by identifier. The default
 * excludes deleted records; includeDeleted true appends the server's
 * include_deleted query flag for callers viewing historical user information.
 */
export async function getById(id: number, includeDeleted = false): Promise<AdminUser> {
  const url = includeDeleted ? `/admin/users/${id}?include_deleted=true` : `/admin/users/${id}`
  const { data } = await apiClient.get<AdminUser>(url)
  return data
}

/**
 * create creates new user and returns the server result.
 */
export async function create(userData: {
  email: string // Account email address.
  password: string // Submitted password text.
  username?: string // Account display username.
  notes?: string // Administrator supplied notes.
  role?: 'admin' | 'user' // Selected account role.
  balance?: number // Account credit balance.
  concurrency?: number // Concurrent request limit.
  rpm_limit?: number // Configured requests-per-minute limit.
  api_key_limit?: number // API key limit.
  allowed_groups?: number[] | null // Recorded allowed groups.
  disabled_public_groups?: number[] | null // Disabled public groups.
}): Promise<AdminUser> {
  const { data } = await apiClient.post<AdminUser>('/admin/users', userData)
  return data
}

/**
 * update updates user and returns the server result.
 */
export async function update(id: number, updates: UpdateUserRequest): Promise<AdminUser> {
  const { data } = await apiClient.put<AdminUser>(`/admin/users/${id}`, updates)
  return data
}

/**
 * deleteUser deletes user and returns the server result.
 */
export async function deleteUser(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/users/${id}`)
  return data
}

/**
 * updateBalance submits a user balance adjustment and returns the updated user.
 * Operation defaults to set; the requested amount is forwarded unchanged, and
 * missing or empty notes are sent as an empty string.
 */
export async function updateBalance(
  id: number,
  balance: number,
  operation: 'set' | 'add' | 'subtract' = 'set',
  notes?: string,
): Promise<AdminUser> {
  const { data } = await apiClient.post<AdminUser>(`/admin/users/${id}/balance`, {
    balance,
    operation,
    notes: notes || '',
  })
  return data
}

/**
 * batchUpdateLimits submits concurrency and RPM limit edits for multiple users
 * in one request.
 */
export async function batchUpdateLimits(
  request: BatchUpdateUserLimitsRequest,
): Promise<BatchUpdateUserLimitsResponse> {
  const { data } = await apiClient.post<BatchUpdateUserLimitsResponse>(
    '/admin/users/batch-limits',
    request,
  )
  return data
}

/**
 * toggleStatus sets user status and returns the server result.
 */
export async function toggleStatus(id: number, status: 'active' | 'disabled'): Promise<AdminUser> {
  return update(id, { status })
}

/**
 * getUserApiKeys retrieves user's API keys and returns the server result.
 */
export async function getUserApiKeys(id: number): Promise<PaginatedResponse<ApiKey>> {
  const { data } = await apiClient.get<PaginatedResponse<ApiKey>>(`/admin/users/${id}/api-keys`)
  return data
}

/**
 * BalanceHistoryItem records a balance, concurrency or subscription adjustment
 * with optional user and group display records.
 */
export interface BalanceHistoryItem {
  id: number // Record identifier value.
  code: string // Redemption or adjustment code.
  type: string // Represented record category.
  value: number // Represented entry value.
  status: string // Current server-reported status.
  used_by: number | null // Redeeming user identifier.
  used_at: string | null // Recorded used at.
  created_at: string // Record creation timestamp.
  group_id: number | null // Group record identifier.
  validity_days: number // Recorded validity days.
  notes: string // Administrator supplied notes.
  user?: {
    id: number // Record identifier value.
    email: string // Account email address.
  } | null // Expanded redeeming user summary.
  group?: {
    id: number // Record identifier value.
    name: string // Human-readable display name.
  } | null // Expanded associated group summary.
}

/**
 * BalanceHistoryResponse adds accumulated recharge totals to paginated user
 * balance-history entries.
 */
export interface BalanceHistoryResponse extends PaginatedResponse<BalanceHistoryItem> {
  total_recharged: number // Recorded total recharged.
}

/**
 * getUserBalanceHistory retrieves a user's balance and entitlement adjustments
 * with recharge totals. Pagination defaults to page one with twenty entries, and
 * a type filter is included only when truthy.
 */
export async function getUserBalanceHistory(
  id: number,
  page: number = 1,
  pageSize: number = 20,
  type?: string,
): Promise<BalanceHistoryResponse> {
  const params: Record<string, any> = { page, page_size: pageSize }
  if (type) params.type = type
  const { data } = await apiClient.get<BalanceHistoryResponse>(
    `/admin/users/${id}/balance-history`,
    { params },
  )
  return data
}

/**
 * replaceGroup submits replace group and returns the server response data.
 */
export async function replaceGroup(
  userId: number,
  oldGroupId: number,
  newGroupId: number,
): Promise<{
  migrated_keys: number // Recorded migrated keys.
}> {
  const { data } = await apiClient.post<{
    migrated_keys: number // Recorded migrated keys.
  }>(`/admin/users/${userId}/replace-group`, { old_group_id: oldGroupId, new_group_id: newGroupId })
  return data
}

/**
 * bindUserAuthIdentity submits bind user auth identity and returns the server
 * response data.
 */
export async function bindUserAuthIdentity(
  userId: number,
  input: AdminBindAuthIdentityRequest,
): Promise<AdminBoundAuthIdentity> {
  const { data } = await apiClient.post<AdminBoundAuthIdentity>(
    `/admin/users/${userId}/auth-identities`,
    input,
  )
  return data
}

/**
 * usersAPI groups administrator user records and identity bindings for callers
 * using the module's named or default API object.
 */
export const usersAPI = {
  list,
  getById,
  create,
  update,
  delete: deleteUser,
  updateBalance,
  batchUpdateLimits,
  toggleStatus,
  getUserApiKeys,
  getUserBalanceHistory,
  replaceGroup,
  bindUserAuthIdentity,
}

export default usersAPI
