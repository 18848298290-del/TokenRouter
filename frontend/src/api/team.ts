import { apiClient } from './client'

/**
 * team.ts sends role-scoped team requests for membership, invitations, ownership
 * and usage. Invitation and ownership responses can return a new team context,
 * while usage queries retain their selected interval and member filters for team
 * reporting consumers.
 */

/**
 * TeamRole distinguishes the team owner from a member for role-scoped frontend
 * operations.
 */
export type TeamRole = 'owner' | 'member'

/**
 * Team describes a workspace's status, membership limit and default member
 * spending limits.
 */
export interface Team {
  id: number // Record identifier value.
  name: string // Human-readable display name.
  status: 'active' | 'suspended' // Current server-reported status.
  member_limit: number // Recorded member limit.
  default_daily_limit_usd: number // Default daily limit USD.
  default_weekly_limit_usd: number // Default weekly limit USD.
  default_monthly_limit_usd: number // Default monthly limit USD.
  member_count: number // Recorded member count.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
}

/**
 * TeamMembership pairs a user's role and team identity with spending limits and
 * observed period usage.
 */
export interface TeamMembership {
  id: number // Record identifier value.
  team_id: number // Team record identifier.
  user_id: number // Owning user identifier.
  email: string // Account email address.
  username: string // Account display username.
  role: TeamRole // Selected account role.
  daily_limit_usd: number // Daily limit USD.
  weekly_limit_usd: number // Weekly limit USD.
  monthly_limit_usd: number // Monthly limit USD.
  daily_usage_usd: number // Daily usage USD.
  weekly_usage_usd: number // Weekly usage USD.
  monthly_usage_usd: number // Monthly usage USD.
  joined_at: string // Recorded joined at.
  last_active_at: string | null // Last active at.
}

/**
 * TeamContext combines a team record with the requesting member and owner
 * membership records.
 */
export interface TeamContext {
  team: Team // Current team details.
  membership: TeamMembership // Current user membership.
  owner: TeamMembership // Team owner membership.
}

/**
 * TeamInvitation records an invitation's recipient, inviter, status and
 * expiration or acceptance times.
 */
export interface TeamInvitation {
  id: number // Record identifier value.
  team_id: number // Team record identifier.
  inviter_user_id: number // Inviter user ID.
  email: string // Account email address.
  status: string // Current server-reported status.
  expires_at: string // Recorded expiration timestamp.
  accepted_at: string | null // Recorded accepted at.
  created_at: string // Record creation timestamp.
}

/**
 * TeamInvitationPreview provides team and inviter display information before an
 * invitation is accepted or declined.
 */
export interface TeamInvitationPreview {
  team_name: string // Recorded team name.
  inviter_name: string // Recorded inviter name.
  inviter_email: string // Recorded inviter email.
  expires_at: string // Recorded expiration timestamp.
}

/**
 * TeamAPIKey describes a member's masked key and team-owner disable state with
 * its group and usage timestamps.
 */
export interface TeamAPIKey {
  id: number // Record identifier value.
  user_id: number // Owning user identifier.
  user_email: string // Recorded user email.
  name: string // Human-readable display name.
  masked_key: string // Recorded masked key.
  status: string // Current server-reported status.
  team_owner_disabled: boolean // Team owner disabled.
  group_id: number | null // Group record identifier.
  group_name: string // Recorded group name.
  last_used_at: string | null // Last used at.
  created_at: string // Record creation timestamp.
}

/**
 * TeamUsageDaily pairs a calendar date with deducted cost and request count for
 * team reporting.
 */
export interface TeamUsageDaily {
  date: string // Usage calendar date.
  actual_cost: number // Actual deducted cost.
  request_count: number // Recorded request count.
}

/**
 * TeamUsageSummary combines interval costs and token totals with daily team
 * usage aggregates.
 */
export interface TeamUsageSummary {
  actual_cost: number // Actual deducted cost.
  request_count: number // Recorded request count.
  input_tokens: number // Consumed input tokens.
  output_tokens: number // Generated output tokens.
  daily: TeamUsageDaily[] // Daily usage buckets.
}

/**
 * TeamMemberUsageSeries pairs a member identity with interval usage totals. It
 * can include former members with spending in the selected range so historical
 * usage remains attributable after departure.
 */
export interface TeamMemberUsageSeries {
  actor_user_id: number // Actor user ID.
  display_name: string // Recorded display name.
  status: 'active' | 'left' // Current server-reported status.
  summary: TeamUsageSummary // Aggregated usage summary.
}

/**
 * TeamUsageLog records one team request's acting member, key, model and deducted
 * usage.
 */
export interface TeamUsageLog {
  id: number // Record identifier value.
  actor_user_id: number // Actor user ID.
  actor_email: string // Recorded actor email.
  api_key_id: number // API-key record identifier.
  api_key_name: string // API key name.
  request_id: string // Unified request identifier.
  model: string // Model identifier value.
  actual_cost: number // Actual deducted cost.
  input_tokens: number // Consumed input tokens.
  output_tokens: number // Generated output tokens.
  created_at: string // Record creation timestamp.
}

/**
 * TeamUsagePage pairs team usage rows with total count, limit and offset
 * pagination metadata.
 */
export interface TeamUsagePage {
  items: TeamUsageLog[] // Returned collection entries.
  total: number // Total matching records.
  limit: number // Maximum returned entries.
  offset: number // Skipped entry count.
}

/**
 * TeamUsageQuery selects a reporting interval, member, key and pagination for
 * team usage requests.
 */
export interface TeamUsageQuery {
  from?: string // Requested interval start.
  to?: string // Requested interval end.
  member_id?: number // Requested member id.
  api_key_id?: number // API-key record identifier.
  limit?: number // Maximum returned entries.
  offset?: number // Skipped entry count.
}

/**
 * teamAPI groups team membership, invitations and usage for callers using the
 * module's named or default API object.
 */
export const teamAPI = {
  /**
   * current retrieves current and returns the server response data.
   */
  async current(): Promise<TeamContext> {
    const { data } = await apiClient.get<TeamContext>('/team')
    return data
  },
  /**
   * create submits team and returns the server response data.
   */
  async create(name: string): Promise<TeamContext> {
    const { data } = await apiClient.post<TeamContext>('/team', { name })
    return data
  },
  /**
   * rename updates rename and returns the server response data.
   */
  async rename(name: string): Promise<TeamContext> {
    const { data } = await apiClient.patch<TeamContext>('/team', { name })
    return data
  },
  /**
   * updateDefaultMemberLimits updates default member limits and returns the
   * server response data.
   */
  async updateDefaultMemberLimits(limits: {
    default_daily_limit_usd: number // Default daily limit USD.
    default_weekly_limit_usd: number // Default weekly limit USD.
    default_monthly_limit_usd: number // Default monthly limit USD.
  }): Promise<TeamContext> {
    const { data } = await apiClient.patch<TeamContext>('/team/default-member-limits', limits)
    return data
  },
  /**
   * setStatus submits status and returns the server response data.
   */
  async setStatus(status: 'active' | 'suspended'): Promise<TeamContext> {
    const { data } = await apiClient.post<TeamContext>('/team/status', { status })
    return data
  },
  /**
   * dissolve removes dissolve and returns after the request completes.
   */
  async dissolve(): Promise<void> {
    await apiClient.delete('/team')
  },
  /**
   * leave submits leave and returns after the request completes.
   */
  async leave(): Promise<void> {
    await apiClient.post('/team/leave')
  },
  /**
   * members retrieves members and returns the server response data.
   */
  async members(): Promise<TeamMembership[]> {
    const { data } = await apiClient.get<TeamMembership[]>('/team/members')
    return data
  },
  /**
   * removeMember removes member and returns after the request completes.
   */
  async removeMember(userID: number): Promise<void> {
    await apiClient.delete(`/team/members/${userID}`)
  },
  /**
   * updateLimits updates limits and returns after the request completes.
   */
  async updateLimits(
    userID: number,
    limits: {
      daily_limit_usd: number // Daily limit USD.
      weekly_limit_usd: number // Weekly limit USD.
      monthly_limit_usd: number // Monthly limit USD.
    },
  ): Promise<void> {
    await apiClient.patch(`/team/members/${userID}/limits`, limits)
  },
  /**
   * resetUsage submits usage and returns after the request completes.
   */
  async resetUsage(
    userID: number,
    periods: {
      daily: boolean // Daily reset selection.
      weekly: boolean // Weekly reset selection.
      monthly: boolean // Monthly reset selection.
    },
  ): Promise<void> {
    await apiClient.post(`/team/members/${userID}/usage/reset`, periods)
  },
  /**
   * invitations retrieves invitations and returns the server response data.
   */
  async invitations(): Promise<TeamInvitation[]> {
    const { data } = await apiClient.get<TeamInvitation[]>('/team/invitations')
    return data
  },
  /**
   * invite submits invite and returns the server response data.
   */
  async invite(email: string): Promise<TeamInvitation> {
    const { data } = await apiClient.post<TeamInvitation>('/team/invitations', { email })
    return data
  },
  /**
   * previewInvitation submits preview invitation and returns the server response
   * data.
   */
  async previewInvitation(token: string): Promise<TeamInvitationPreview> {
    const { data } = await apiClient.post<TeamInvitationPreview>('/team/invitations/preview', {
      token,
    })
    return data
  },
  /**
   * resolveInvitation submits the token and acceptance or decline decision. A
   * successful acceptance returns the resulting team context; a successful
   * decline returns null after recording the decision.
   */
  async resolveInvitation(
    token: string,
    resolution: 'accepted' | 'declined',
  ): Promise<TeamContext | null> {
    const { data } = await apiClient.post<TeamContext | null>('/team/invitations/resolve', {
      token,
      resolution,
    })
    return data
  },
  /**
   * reissueInvitation submits reissue invitation and returns the server response
   * data.
   */
  async reissueInvitation(id: number): Promise<TeamInvitation> {
    const { data } = await apiClient.post<TeamInvitation>(`/team/invitations/${id}/reissue`)
    return data
  },
  /**
   * revokeInvitation removes revoke invitation and returns after the request
   * completes.
   */
  async revokeInvitation(id: number): Promise<void> {
    await apiClient.delete(`/team/invitations/${id}`)
  },
  /**
   * startTransfer submits start transfer and returns after the request
   * completes.
   */
  async startTransfer(targetUserID: number): Promise<void> {
    await apiClient.post('/team/ownership-transfer', { target_user_id: targetUserID })
  },
  /**
   * resolveTransfer submits resolve transfer and returns the server response
   * data.
   */
  async resolveTransfer(
    token: string,
    resolution: 'accepted' | 'declined',
  ): Promise<TeamContext | null> {
    const { data } = await apiClient.post<TeamContext | null>('/team/ownership-transfer/resolve', {
      token,
      resolution,
    })
    return data
  },

  /**
   * usage retrieves aggregate team spending and token usage using optional query
   * filters. An omitted query sends an empty object, allowing the team endpoint
   * to select its default reporting interval.
   */
  async usage(query: TeamUsageQuery = {}): Promise<TeamUsageSummary> {
    const { data } = await apiClient.get<TeamUsageSummary>('/team/usage', { params: query })
    return data
  },

  /**
   * memberUsage retrieves per-member team usage for an optional start and end
   * interval. Missing interval fields remain absent from the query, leaving
   * interval selection to the team member-usage endpoint.
   */
  async memberUsage(
    query: Pick<TeamUsageQuery, 'from' | 'to'> = {},
  ): Promise<TeamMemberUsageSeries[]> {
    const { data } = await apiClient.get<TeamMemberUsageSeries[]>('/team/usage/members', {
      params: query,
    })
    return data
  },

  /**
   * usageLogs retrieves team usage records using optional interval, member, key
   * and pagination filters. An omitted query sends an empty object, allowing the
   * endpoint to use its default list selection.
   */
  async usageLogs(query: TeamUsageQuery = {}): Promise<TeamUsagePage> {
    const { data } = await apiClient.get<TeamUsagePage>('/team/usage/logs', { params: query })
    return data
  },
  /**
   * keys retrieves keys and returns the server response data.
   */
  async keys(): Promise<TeamAPIKey[]> {
    const { data } = await apiClient.get<TeamAPIKey[]>('/team/keys')
    return data
  },
  /**
   * disableKey submits disable key and returns after the request completes.
   */
  async disableKey(id: number): Promise<void> {
    await apiClient.post(`/team/keys/${id}/disable`)
  },
  /**
   * enableKey submits enable key and returns after the request completes.
   */
  async enableKey(id: number): Promise<void> {
    await apiClient.post(`/team/keys/${id}/enable`)
  },
  /**
   * deleteKey removes key and returns after the request completes.
   */
  async deleteKey(id: number): Promise<void> {
    await apiClient.delete(`/team/keys/${id}`)
  },
}

export default teamAPI
