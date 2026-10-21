import { apiClient } from '../client'
import type { AdminGroup, CreateGroupRequest, UpdateGroupRequest } from '@/api/groups.types'
import type { PaginatedResponse } from '@/api/common.types'

/**
 * groups.ts sends administrator group operations and coordinates idempotent
 * group duplication in the browser. List and summary requests return server
 * records, while the RPM view extracts only entries with an override from the
 * shared rate-multiplier response. Duplication is performed by the server so
 * configuration absent from list rows participates in the copy.
 *
 * An unresolved duplicate operation retains its key in memory and, when
 * available, sessionStorage. The cache key includes both administrator identity
 * and source group, preventing retries from borrowing another administrator's
 * pending operation. Success clears memory and attempts storage removal; a
 * failed removal can leave a stale stored key. Rejection retains the pending key
 * because the server may already have completed the copy. When identity cannot
 * be read, each call uses an uncached operation key.
 */

/**
 * LiveCapability reports server support for generating ChatGPT DeviceCheck
 * proofs and an optional explanatory reason.
 */
export interface LiveCapability {
  supported: boolean // Capability support flag.
  reason?: string // Server-provided outcome reason.
}

/**
 * list lists all groups with pagination. Page defaults to 1, pageSize defaults
 * to 20; the caller may cancel through the supplied AbortSignal. The function
 * returns the data portion of the completed response.
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    status?: 'active' | 'inactive' // Current server-reported status.
    is_exclusive?: boolean // Recorded is exclusive.
    search?: string // Free-text search query.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<PaginatedResponse<AdminGroup>> {
  const { data } = await apiClient.get<PaginatedResponse<AdminGroup>>('/admin/groups', {
    params: {
      page,
      page_size: pageSize,
      ...filters,
    },
    signal: options?.signal,
  })
  return data
}

/**
 * getAll retrieves all active groups (without pagination) and returns the server
 * result.
 */
export async function getAll(): Promise<AdminGroup[]> {
  const { data } = await apiClient.get<AdminGroup[]>('/admin/groups/all')
  return data
}

/**
 * getAllIncludingInactive retrieves groups including disabled entries for
 * administrator filtering and binding.
 */
export async function getAllIncludingInactive(): Promise<AdminGroup[]> {
  const { data } = await apiClient.get<AdminGroup[]>('/admin/groups/all', {
    params: { include_inactive: true },
  })
  return data
}

/**
 * getLiveCapability retrieves the server's current Live runtime capability
 * report.
 */
export async function getLiveCapability(): Promise<LiveCapability> {
  const { data } = await apiClient.get<LiveCapability>('/admin/groups/live-capability')
  return data
}

/**
 * getModelsListCandidates retrieves model names available for a group's custom
 * model list. Pass zero while creating a group; a missing models collection is
 * returned as an empty list.
 */
export async function getModelsListCandidates(id: number): Promise<string[]> {
  const { data } = await apiClient.get<{
    models: string[] // Model identifier list.
  }>(`/admin/groups/${id}/models-list-candidates`)
  // Missing model candidates leave the group editor with an empty selection
  // list, while the rest of the creation or edit form remains usable.
  return data.models || []
}

/**
 * create creates new group and returns the server result.
 */
export async function create(groupData: CreateGroupRequest): Promise<AdminGroup> {
  const { data } = await apiClient.post<AdminGroup>('/admin/groups', groupData)
  return data
}

/**
 * duplicateOperationKeys retains unresolved group-copy operation keys by
 * administrator and source group, allowing retries to reuse the same server
 * operation.
 */
const duplicateOperationKeys = new Map<string, string>()

/**
 * DuplicateOperationScope ties one retry-storage key to the administrator
 * identity used in its namespace.
 */
interface DuplicateOperationScope {
  adminID: string // Current administrator identifier.
  key: string // Stable entry key.
}

/**
 * getCurrentAdminID reads a positive safe-integer administrator identifier from
 * browser storage. Missing storage, malformed JSON and invalid IDs return null,
 * disabling persistent reuse when the current identity cannot be established.
 */
function getCurrentAdminID(): string | null {
  try {
    const rawUser = globalThis.localStorage?.getItem('auth_user')
    if (!rawUser) return null

    const user: unknown = JSON.parse(rawUser)
    if (typeof user !== 'object' || user === null) return null

    const id = (
      user as {
        id?: unknown // Record identifier value.
      }
    ).id
    if (typeof id !== 'number' || !Number.isSafeInteger(id) || id <= 0) return null
    return String(id)
  } catch {
    // Unreadable identity data cannot establish an administrator namespace, so
    // duplication uses an uncached operation key instead of sharing pending work.
    return null
  }
}

/**
 * duplicateOperationScope combines the current administrator and source group
 * into a retry-storage key. It returns null when identity is unavailable so a
 * retry key is never cached under an ambiguous administrator.
 */
function duplicateOperationScope(id: number): DuplicateOperationScope | null {
  const adminID = getCurrentAdminID()
  if (!adminID) return null

  return {
    adminID,
    key: `tokenrouter:admin:group-duplicate:${adminID}:${id}`,
  }
}

/**
 * getStoredDuplicateOperationKey reads a pending group-copy key from
 * sessionStorage. Storage absence or access failure yields null; duplicate can
 * still use its in-memory key or create a fresh operation.
 */
function getStoredDuplicateOperationKey(storageKey: string): string | null {
  try {
    return globalThis.sessionStorage?.getItem(storageKey) ?? null
  } catch {
    // Blocked tab storage leaves recovery to the in-memory key, allowing this
    // document to retry pending work even when reload recovery is unavailable.
    return null
  }
}

/**
 * storeDuplicateOperationKey saves a pending group-copy key or removes it when
 * null. A failed save leaves the pending key in memory. A failed removal after
 * success can leave a stale storage key after the memory entry is cleared.
 */
function storeDuplicateOperationKey(storageKey: string, key: string | null): void {
  try {
    if (key) globalThis.sessionStorage?.setItem(storageKey, key)
    else globalThis.sessionStorage?.removeItem(storageKey)
  } catch {
    // Save failures retain retry identity in memory. Removal failures leave any
    // stored key behind because duplicate has already cleared the memory entry.
  }
}

/**
 * duplicate requests a server-side group copy and returns the resulting group.
 * It retains the operation key before sending, under the current administrator
 * and source group, so a rejected or interrupted request can retry an operation
 * whose server outcome is unknown. Success clears memory and attempts storage
 * removal; a failed removal can leave a stale stored key. If administrator identity
 * is unavailable, the call creates a fresh uncached key.
 */
export async function duplicate(id: number): Promise<AdminGroup> {
  const scope = duplicateOperationScope(id)
  let idempotencyKey = scope
    ? (duplicateOperationKeys.get(scope.key) ?? getStoredDuplicateOperationKey(scope.key))
    : null
  if (!idempotencyKey) {
    /**
     * Create a fresh identifier only when this administrator and source group
     * have no pending key, so ambiguous retries keep their identity.
     */
    const requestID =
      globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`
    idempotencyKey = `group-duplicate-${scope?.adminID ?? 'unknown-admin'}-${id}-${requestID}`
  }
  if (scope) {
    /**
     * Retain the operation key before the network call because a timeout can
     * occur after the server has already created the copy.
     */
    duplicateOperationKeys.set(scope.key, idempotencyKey)
    storeDuplicateOperationKey(scope.key, idempotencyKey)
  }

  const { data } = await apiClient.post<AdminGroup>(`/admin/groups/${id}/duplicate`, undefined, {
    headers: { 'Idempotency-Key': idempotencyKey },
  })

  if (scope) {
    /**
     * A confirmed response ends the pending operation. Clear memory before
     * attempting storage removal; a failed removal can expose a stale key later.
     */
    duplicateOperationKeys.delete(scope.key)
    storeDuplicateOperationKey(scope.key, null)
  }
  return data
}

/**
 * update updates group and returns the server result.
 */
export async function update(id: number, updates: UpdateGroupRequest): Promise<AdminGroup> {
  const { data } = await apiClient.put<AdminGroup>(`/admin/groups/${id}`, updates)
  return data
}

/**
 * deleteGroup deletes group and returns the server result.
 */
export async function deleteGroup(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/groups/${id}`)
  return data
}

/**
 * GroupRateMultiplierEntry pairs user display data with optional group-specific
 * billing and RPM overrides.
 */
export interface GroupRateMultiplierEntry {
  user_id: number // Owning user identifier.
  user_name: string // Recorded user name.
  user_email: string // Recorded user email.
  user_notes: string // Recorded user notes.
  user_status: string // Recorded user status.
  rate_multiplier?: number | null // Configured billing multiplier.
  rpm_override?: number | null // Recorded rpm override.
}

/**
 * getGroupRateMultipliers retrieves rate multipliers for users in a group.
 */
export async function getGroupRateMultipliers(id: number): Promise<GroupRateMultiplierEntry[]> {
  const { data } = await apiClient.get<GroupRateMultiplierEntry[]>(
    `/admin/groups/${id}/rate-multipliers`,
  )
  return data
}

/**
 * updateSortOrder updates group sort orders and returns the server result.
 */
export async function updateSortOrder(
  updates: Array<{
    id: number // Record identifier value.
    sort_order: number // Display ordering position.
  }>,
): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.put<{
    message: string // Server-provided result message.
  }>('/admin/groups/sort-order', {
    updates,
  })
  return data
}

/**
 * batchSetGroupRateMultipliers updates user billing multipliers within the
 * selected group. The endpoint edits rate_multiplier values while retaining
 * existing RPM overrides, so callers can save price adjustments independently of
 * rate limits.
 */
export async function batchSetGroupRateMultipliers(
  id: number,
  entries: Array<{
    user_id: number // Owning user identifier.
    rate_multiplier: number // Configured billing multiplier.
  }>,
): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.put<{
    message: string // Server-provided result message.
  }>(`/admin/groups/${id}/rate-multipliers`, { entries })
  return data
}

/**
 * GroupRPMOverrideEntry contains user display data for an entry with a defined
 * group RPM override.
 */
export interface GroupRPMOverrideEntry {
  user_id: number // Owning user identifier.
  user_name: string // Recorded user name.
  user_email: string // Recorded user email.
  user_notes: string // Recorded user notes.
  user_status: string // Recorded user status.
  rpm_override: number // Recorded rpm override.
}

/**
 * getGroupRPMOverrides retrieves rate-multiplier rows and extracts entries with
 * an RPM override. Null or missing overrides are omitted, while zero is
 * retained; returned objects contain user identity and RPM fields.
 */
export async function getGroupRPMOverrides(id: number): Promise<GroupRPMOverrideEntry[]> {
  const { data } = await apiClient.get<GroupRateMultiplierEntry[]>(
    `/admin/groups/${id}/rate-multipliers`,
  )
  return data
    .filter((e) => e.rpm_override != null)
    .map((e) => ({
      user_id: e.user_id,
      user_name: e.user_name,
      user_email: e.user_email,
      user_notes: e.user_notes,
      user_status: e.user_status,
      rpm_override: e.rpm_override as number,
    }))
}

/**
 * batchSetGroupRPMOverrides updates user RPM limits within the selected group.
 * The endpoint edits rpm_override values while retaining existing rate
 * multipliers, so callers can save rate limits independently of billing
 * adjustments.
 */
export async function batchSetGroupRPMOverrides(
  id: number,
  entries: Array<{
    user_id: number // Owning user identifier.
    rpm_override: number // Recorded rpm override.
  }>,
): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.put<{
    message: string // Server-provided result message.
  }>(`/admin/groups/${id}/rpm-overrides`, {
    entries,
  })
  return data
}

/**
 * clearGroupRPMOverrides clears all RPM overrides for a group (preserves
 * rate_multiplier).
 */
export async function clearGroupRPMOverrides(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/groups/${id}/rpm-overrides`)
  return data
}

/**
 * getUsageSummary retrieves group costs for today, yesterday and all time in the
 * server timezone.
 */
export async function getUsageSummary(): Promise<
  {
    group_id: number // Group record identifier.
    today_cost: number // Recorded today cost.
    yesterday_cost: number // Recorded yesterday cost.
    total_cost: number // Recorded total cost.
  }[]
> {
  const { data } = await apiClient.get<
    {
      group_id: number // Group record identifier.
      today_cost: number // Recorded today cost.
      yesterday_cost: number // Recorded yesterday cost.
      total_cost: number // Recorded total cost.
    }[]
  >('/admin/groups/usage-summary')
  return data
}

/**
 * getCapacitySummary retrieves capacity summary (concurrency/sessions/RPM) for
 * all active groups.
 */
export async function getCapacitySummary(): Promise<
  {
    group_id: number // Group record identifier.
    concurrency_used: number // Recorded concurrency used.
    concurrency_max: number // Recorded concurrency max.
    sessions_used: number // Recorded sessions used.
    sessions_max: number // Recorded sessions max.
    rpm_used: number // Recorded rpm used.
    rpm_max: number // Recorded rpm max.
  }[]
> {
  const { data } = await apiClient.get<
    {
      group_id: number // Group record identifier.
      concurrency_used: number // Recorded concurrency used.
      concurrency_max: number // Recorded concurrency max.
      sessions_used: number // Recorded sessions used.
      sessions_max: number // Recorded sessions max.
      rpm_used: number // Recorded rpm used.
      rpm_max: number // Recorded rpm max.
    }[]
  >('/admin/groups/capacity-summary')
  return data
}

/**
 * groupsAPI groups administrator group configuration and summaries for callers
 * using the module's named or default API object.
 */
export const groupsAPI = {
  list,
  getAll,
  getAllIncludingInactive,
  getLiveCapability,
  getModelsListCandidates,
  create,
  duplicate,
  update,
  delete: deleteGroup,
  getGroupRateMultipliers,
  batchSetGroupRateMultipliers,
  getGroupRPMOverrides,
  clearGroupRPMOverrides,
  batchSetGroupRPMOverrides,
  updateSortOrder,
  getUsageSummary,
  getCapacitySummary,
}

export default groupsAPI
