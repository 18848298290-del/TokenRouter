import { apiClient } from '../client'
import type { TeamContext, TeamMembership, TeamUsageQuery, TeamUsageSummary } from '../team'

/**
 * teams.ts sends administrator team maintenance requests and returns the managed
 * records. Team selection uses numeric identifiers, with separate operations for
 * status, ownership, membership and limits so administrator forms submit the
 * chosen maintenance action.
 */

/**
 * AdminTeam summarizes a managed team with its owner identity and member count
 * or limit.
 */
export interface AdminTeam {
  id: number // Record identifier value.
  name: string // Human-readable display name.
  status: 'active' | 'suspended' // Current server-reported status.
  member_limit: number // Recorded member limit.
  member_count: number // Recorded member count.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
  owner_user_id: number // Owner user ID.
  owner_email: string // Recorded owner email.
}

/**
 * teamsAPI groups administrator team maintenance operations for callers using
 * the module's named or default API object.
 */
const teamsAPI = {
  /**
   * list retrieves administrator teams and returns the server response data.
   */
  async list(): Promise<AdminTeam[]> {
    const { data } = await apiClient.get<AdminTeam[]>('/admin/teams')
    return data
  },
  /**
   * get retrieves administrator teams and returns the server response data.
   */
  async get(id: number): Promise<TeamContext> {
    const { data } = await apiClient.get<TeamContext>(`/admin/teams/${id}`)
    return data
  },
  /**
   * members retrieves members and returns the server response data.
   */
  async members(id: number): Promise<TeamMembership[]> {
    const { data } = await apiClient.get<TeamMembership[]>(`/admin/teams/${id}/members`)
    return data
  },

  /**
   * usage retrieves the selected team's aggregate usage with optional reporting
   * filters. An omitted query becomes an empty object, leaving interval and
   * member selection to the administrator team endpoint.
   */
  async usage(id: number, query: TeamUsageQuery = {}): Promise<TeamUsageSummary> {
    const { data } = await apiClient.get<TeamUsageSummary>(`/admin/teams/${id}/usage`, {
      params: query,
    })
    return data
  },
  /**
   * create submits administrator teams and returns the server response data.
   */
  async create(payload: {
    owner_user_id: number // Owner user ID.
    name: string // Human-readable display name.
    member_limit: number // Recorded member limit.
  }): Promise<TeamContext> {
    const { data } = await apiClient.post<TeamContext>('/admin/teams', payload)
    return data
  },
  /**
   * update updates administrator teams and returns the server response data.
   */
  async update(
    id: number,
    payload: {
      name?: string // Human-readable display name.
      status?: 'active' | 'suspended' // Current server-reported status.
      member_limit?: number // Recorded member limit.
    },
  ): Promise<TeamContext> {
    const { data } = await apiClient.patch<TeamContext>(`/admin/teams/${id}`, payload)
    return data
  },
  /**
   * forceTransfer submits force transfer and returns the server response data.
   */
  async forceTransfer(id: number, targetUserID: number): Promise<TeamContext> {
    const { data } = await apiClient.post<TeamContext>(`/admin/teams/${id}/force-transfer`, {
      target_user_id: targetUserID,
    })
    return data
  },
  /**
   * dissolve removes dissolve and returns after the request completes.
   */
  async dissolve(id: number): Promise<void> {
    await apiClient.delete(`/admin/teams/${id}`)
  },
}

export default teamsAPI
