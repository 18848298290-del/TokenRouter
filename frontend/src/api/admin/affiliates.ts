import { apiClient } from '../client'
import type { PaginatedResponse } from '@/api/common.types'

/**
 * affiliates.ts retrieves administrator referral records and per-user rebate
 * summaries. Invite, rebate and transfer lists share date, sorting and
 * pagination normalization, while user overview requests select one referral
 * account by its numeric user identifier.
 */

/**
 * ListAffiliateRecordsParams selects referral-record pagination, date range,
 * search and ordering for administrator lists.
 */
export interface ListAffiliateRecordsParams {
  page?: number // Requested page number.
  page_size?: number // Requested page size.
  search?: string // Free-text search query.
  start_at?: string // Requested start at.
  end_at?: string // Requested end at.
  sort_by?: string // Requested sort column.
  sort_order?: 'asc' | 'desc' // Requested ordering direction.
  timezone?: string // IANA timezone name.
}

/**
 * AffiliateInviteRecord pairs inviter and invitee identities with the referral
 * code and accumulated rebate.
 */
export interface AffiliateInviteRecord {
  inviter_id: number // Inviting user identifier.
  inviter_email: string // Recorded inviter email.
  inviter_username: string // Recorded inviter username.
  invitee_id: number // Invited user identifier.
  invitee_email: string // Recorded invitee email.
  invitee_username: string // Recorded invitee username.
  aff_code: string // Referral invitation code.
  total_rebate: number // Recorded total rebate.
  created_at: string // Record creation timestamp.
}

/**
 * AffiliateRebateRecord records a paid order's referral relationship, charged
 * currencies and resulting rebate amount.
 */
export interface AffiliateRebateRecord {
  order_id: number // Payment order identifier.
  out_trade_no: string // External trade identifier.
  inviter_id: number // Inviting user identifier.
  inviter_email: string // Recorded inviter email.
  inviter_username: string // Recorded inviter username.
  invitee_id: number // Invited user identifier.
  invitee_email: string // Recorded invitee email.
  invitee_username: string // Recorded invitee username.
  order_amount: number // Recorded order amount.
  pay_amount: number // Recorded pay amount.
  order_type: 'balance' | 'subscription' // Recorded order type.
  /**
   * Subscription list-price currency; balance orders return an empty string
   * because their principal is represented as account credit.
   */
  order_currency: string // Recorded order currency.
  /**
   * Payment channel charge currency.
   */
  pay_currency: string // Recorded pay currency.
  rebate_amount: number // Recorded rebate amount.
  payment_type: string // Recorded payment type.
  order_status: string // Recorded order status.
  created_at: string // Record creation timestamp.
}

/**
 * AffiliateTransferRecord describes a referral-credit transfer and optional
 * resulting balance snapshots. snapshot_available tells consumers whether the
 * post-transfer quota and balance observations are present for historical
 * display.
 */
export interface AffiliateTransferRecord {
  ledger_id: number // Transfer ledger identifier.
  user_id: number // Owning user identifier.
  user_email: string // Recorded user email.
  username: string // Account display username.
  amount: number // Credit transfer amount.
  balance_after?: number | null // Recorded balance after.
  available_quota_after?: number | null // Available quota after.
  frozen_quota_after?: number | null // Frozen quota after.
  history_quota_after?: number | null // History quota after.
  snapshot_available: boolean // Post-transfer snapshot availability.
  created_at: string // Record creation timestamp.
}

/**
 * AffiliateUserOverview summarizes one user's referral code, rebate percentage,
 * invite counts and available credit.
 */
export interface AffiliateUserOverview {
  user_id: number // Owning user identifier.
  email: string // Account email address.
  username: string // Account display username.
  aff_code: string // Referral invitation code.
  rebate_rate_percent: number // Rebate rate percent.
  invited_count: number // Recorded invited count.
  rebated_invitee_count: number // Rebated invitee count.
  available_quota: number // Recorded available quota.
  history_quota: number // Recorded history quota.
}

/**
 * recordParams builds the shared referral-record query parameters. Page and page
 * size default to one and twenty, search defaults to empty, and falsy dates,
 * sort fields and timezone are omitted from the query.
 */
function recordParams(params: ListAffiliateRecordsParams = {}) {
  return {
    page: params.page ?? 1,
    page_size: params.page_size ?? 20,
    search: params.search ?? '',
    start_at: params.start_at || undefined,
    end_at: params.end_at || undefined,
    sort_by: params.sort_by || undefined,
    sort_order: params.sort_order || undefined,
    timezone: params.timezone || undefined,
  }
}

/**
 * listInviteRecords retrieves referral invitations using the shared record
 * filters. Omitted parameters select page one with twenty records, and empty
 * date, sorting or timezone filters are omitted by recordParams.
 */
export async function listInviteRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateInviteRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateInviteRecord>>(
    '/admin/affiliates/invites',
    { params: recordParams(params) },
  )
  return data
}

/**
 * listRebateRecords retrieves rebate ledger records using the shared query
 * normalization. Page and size default to one and twenty; empty date or ordering
 * values stay absent from the outgoing query.
 */
export async function listRebateRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateRebateRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateRebateRecord>>(
    '/admin/affiliates/rebates',
    { params: recordParams(params) },
  )
  return data
}

/**
 * listTransferRecords retrieves referral-credit transfers with the shared list
 * filters. Omitted parameters request the first twenty records, and recordParams
 * removes empty dates, sorting fields and timezone values before dispatch.
 */
export async function listTransferRecords(
  params: ListAffiliateRecordsParams = {},
): Promise<PaginatedResponse<AffiliateTransferRecord>> {
  const { data } = await apiClient.get<PaginatedResponse<AffiliateTransferRecord>>(
    '/admin/affiliates/transfers',
    { params: recordParams(params) },
  )
  return data
}

/**
 * getUserOverview retrieves user overview and returns the server response data.
 */
export async function getUserOverview(userId: number): Promise<AffiliateUserOverview> {
  const { data } = await apiClient.get<AffiliateUserOverview>(
    `/admin/affiliates/users/${userId}/overview`,
  )
  return data
}

/**
 * affiliatesAPI groups administrator referral records and rebate summaries for
 * callers using the module's named or default API object.
 */
export const affiliatesAPI = {
  listInviteRecords,
  listRebateRecords,
  listTransferRecords,
  getUserOverview,
}

export default affiliatesAPI
