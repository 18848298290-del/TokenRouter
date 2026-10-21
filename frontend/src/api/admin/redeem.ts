import { apiClient } from '../client'
import type {
  RedeemCode,
  GenerateRedeemCodesRequest,
  UpdateRedeemCodeRequest,
  BatchUpdateRedeemCodeFields,
  RedeemCodeType,
} from '@/api/redeem.types'
import type { PaginatedResponse } from '@/api/common.types'

/**
 * redeem.ts manages administrator redemption codes and their batch operations.
 * Creation carries entitlement fields, while updates and batch edits use their
 * respective payloads; list requests accept cancellation and export returns the
 * CSV Blob produced by the server.
 */

/**
 * list lists all redeem codes with pagination. Page defaults to 1, pageSize
 * defaults to 20; the caller may cancel through the supplied AbortSignal. The
 * function returns the data portion of the completed response.
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    type?: RedeemCodeType // Represented record category.
    status?: 'active' | 'used' | 'expired' | 'unused' | 'disabled' // Current server-reported status.
    search?: string // Free-text search query.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<PaginatedResponse<RedeemCode>> {
  const { data } = await apiClient.get<PaginatedResponse<RedeemCode>>('/admin/redeem-codes', {
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
 * generate submits redemption-code creation with the supplied entitlement type
 * and value. Nonnegative maxUses is retained, expiry fields require positive
 * values, and a nonempty custom code is trimmed. planId is included only for
 * subscription codes; requiresPayment defaults false.
 */
export async function generate(
  count: number,
  type: RedeemCodeType,
  value: number,
  planId?: number | null,
  maxUses?: number,
  expiresAt?: number | null,
  code?: string,
  expiresInDays?: number | null,
  requiresPayment: boolean = false,
): Promise<RedeemCode[]> {
  const payload: GenerateRedeemCodesRequest = {
    count,
    type,
    value,
    /**
     * The redemption handler checks the payment record when a user claims this code,
     * using the submitted payment restriction.
     */
    requires_payment: requiresPayment,
  }

  if (maxUses !== undefined && maxUses >= 0) {
    payload.max_uses = maxUses
  }

  if (expiresAt && expiresAt > 0) {
    payload.expires_at = expiresAt
  }
  if (expiresInDays && expiresInDays > 0) {
    payload.expires_in_days = expiresInDays
  }

  if (code && code.trim()) {
    payload.code = code.trim()
  }

  /**
   * Subscription redemption fields.
   */
  if (type === 'subscription') {
    payload.plan_id = planId
  }

  const { data } = await apiClient.post<RedeemCode[]>('/admin/redeem-codes/generate', payload)
  return data
}

/**
 * update submits edits to the selected redemption code and returns its record.
 */
export async function update(id: number, request: UpdateRedeemCodeRequest): Promise<RedeemCode> {
  const { data } = await apiClient.put<RedeemCode>(`/admin/redeem-codes/${id}`, request)
  return data
}

/**
 * deleteCode deletes redeem code and returns the server result.
 */
export async function deleteCode(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/redeem-codes/${id}`)
  return data
}

/**
 * batchDelete submits batch requests to delete redeem codes.
 */
export async function batchDelete(ids: number[]): Promise<{
  deleted: number // Deleted record count.
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.post<{
    deleted: number // Deleted record count.
    message: string // Server-provided result message.
  }>('/admin/redeem-codes/batch-delete', { ids })
  return data
}

/**
 * batchUpdate submits non-entitlement field edits for selected redemption codes
 * and returns the affected count.
 */
export async function batchUpdate(
  ids: number[],
  fields: BatchUpdateRedeemCodeFields,
): Promise<{
  updated: number // Updated record count.
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.post<{
    updated: number // Updated record count.
    message: string // Server-provided result message.
  }>('/admin/redeem-codes/batch-update', { ids, fields })
  return data
}

/**
 * exportCodes downloads filtered redemption codes as a CSV Blob. The request
 * forwards the supplied list filters and selects binary response handling so
 * browser download consumers receive the file data.
 */
export async function exportCodes(filters?: {
  type?: RedeemCodeType // Represented record category.
  status?: 'active' | 'used' | 'expired' | 'unused' | 'disabled' // Current server-reported status.
  search?: string // Free-text search query.
  sort_by?: string // Requested sort column.
  sort_order?: 'asc' | 'desc' // Requested ordering direction.
}): Promise<Blob> {
  const response = await apiClient.get('/admin/redeem-codes/export', {
    params: filters,
    responseType: 'blob',
  })
  return response.data
}

/**
 * redeemAPI groups redemption code records and operations for callers using the
 * module's named or default API object.
 */
export const redeemAPI = {
  list,
  generate,
  update,
  delete: deleteCode,
  batchDelete,
  batchUpdate,
  exportCodes,
}

export default redeemAPI
