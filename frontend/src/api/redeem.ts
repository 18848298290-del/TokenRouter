import { apiClient } from './client'
import type { PaginatedResponse } from '@/api/common.types'
import type { RedeemCode, RedeemCodeRequest } from '@/api/redeem.types'

/**
 * redeem.ts submits user redemption codes and retrieves redemption history.
 * Submission wraps the code in the shared request type, while history callers
 * supply one-based pagination values and receive a paginated collection of the
 * resulting redemption records.
 */

/**
 * RedeemHistoryItem uses the shared redemption record shape for the current
 * user's history entries.
 */
export type RedeemHistoryItem = RedeemCode

/**
 * redeem submits redeem and returns the server response data.
 */
export async function redeem(code: string): Promise<RedeemCode> {
  const payload: RedeemCodeRequest = { code }

  const { data } = await apiClient.post<RedeemCode>('/redeem', payload)

  return data
}

/**
 * getHistory retrieves the current user's redemption history in descending
 * usage-time order. Pagination starts at one, and the pageSize argument controls
 * the number of records requested from the server.
 */
export async function getHistory(
  page: number,
  pageSize: number,
): Promise<PaginatedResponse<RedeemHistoryItem>> {
  const { data } = await apiClient.get<PaginatedResponse<RedeemHistoryItem>>('/redeem/history', {
    params: { page, page_size: pageSize },
  })
  return data
}

/**
 * redeemAPI groups redemption code records and operations for callers using the
 * module's named or default API object.
 */
export const redeemAPI = {
  redeem,
  getHistory,
}

export default redeemAPI
