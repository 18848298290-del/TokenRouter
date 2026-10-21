import { apiClient } from './client'
import type { Group } from '@/api/groups.types'

/**
 * groups.ts retrieves group choices and custom user rates for personal or team
 * API-key selection. The server supplies canonical and optional display names,
 * and selection controls need the display label while other consumers still need
 * the canonical value. Available-group requests can select a subscription or
 * skip model expansion for lighter filters.
 *
 * The result mapping copies each group, saves its server name as canonical_name
 * and then chooses the visible name from display_name when present. Other nested
 * values remain shared with the response. Rate lookup uses a separate endpoint
 * and converts a missing map into an empty object so consumers can perform
 * group-ID lookups directly.
 */

/**
 * getAvailable retrieves groups for a personal or team scope and exposes display
 * names for selectors. It preserves each server name as canonical_name before
 * choosing display_name when truthy, so presentation does not erase the
 * canonical identifier. A positive subscription ID narrows the request, while
 * includeModels false omits model expansion through an explicit query flag. The
 * returned objects are shallow copies, with other nested group values shared
 * with the response.
 */
export async function getAvailable(
  scope: 'personal' | 'team' = 'personal',
  subscriptionId?: number,
  includeModels = true,
): Promise<Group[]> {
  const params: {
    scope: 'personal' | 'team' // Requested access scope.
    subscription_id?: number // Recorded subscription id.
    include_models?: boolean // Recorded include models.
  } = { scope }
  if (!includeModels) params.include_models = false
  if (subscriptionId && subscriptionId > 0) {
    params.subscription_id = subscriptionId
  }
  const { data } = await apiClient.get<Group[]>('/groups/available', { params })
  /**
   * Preserve the server name as canonical_name before replacing the visible
   * name, so localized labels cannot erase the group identifier used by
   * consumers.
   */
  return data.map((group) => ({
    ...group,
    canonical_name: group.name,
    name: group.display_name || group.name,
  }))
}

/**
 * getUserGroupRates retrieves custom group rate multipliers for the selected
 * scope. Personal scope is the default, and a null response becomes an empty
 * object for callers performing group-ID lookups.
 */
export async function getUserGroupRates(
  scope: 'personal' | 'team' = 'personal',
): Promise<Record<number, number>> {
  const { data } = await apiClient.get<Record<number, number> | null>('/groups/rates', {
    params: { scope },
  })
  /**
   * A missing rate map means there are no returned user overrides; expose an
   * empty object so callers can perform group lookups.
   */
  return data || {}
}

/**
 * userGroupsAPI groups personal and team group choices with user-specific rate
 * lookups for callers using the module's named or default API object.
 */
export const userGroupsAPI = {
  getAvailable,
  getUserGroupRates,
}

export default userGroupsAPI
