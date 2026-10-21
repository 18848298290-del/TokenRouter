import { apiClient } from '../client'
import type {
  PromoCode,
  PromoCodeUsage,
  CreatePromoCodeRequest,
  UpdatePromoCodeRequest,
} from '@/api/admin/promo.types'
import type { BasePaginationResponse } from '@/api/common.types'

/**
 * promo.ts sends administrator promotion-code operations and retrieves
 * redemption usage. Code lists support filtering, sorting and cancellation,
 * while per-code usage has its own pagination; create and update requests use
 * the shared promotion payload definitions.
 */

/**
 * list retrieves administrator promo codes and returns the server response data.
 * Page defaults to 1, pageSize defaults to 20; the caller may cancel through the
 * supplied AbortSignal.
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    status?: string // Current server-reported status.
    search?: string // Free-text search query.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<BasePaginationResponse<PromoCode>> {
  const { data } = await apiClient.get<BasePaginationResponse<PromoCode>>('/admin/promo-codes', {
    params: { page, page_size: pageSize, ...filters },
    signal: options?.signal,
  })
  return data
}

/**
 * getById retrieves by id and returns the server response data.
 */
export async function getById(id: number): Promise<PromoCode> {
  const { data } = await apiClient.get<PromoCode>(`/admin/promo-codes/${id}`)
  return data
}

/**
 * create submits administrator promo codes and returns the server response data.
 */
export async function create(request: CreatePromoCodeRequest): Promise<PromoCode> {
  const { data } = await apiClient.post<PromoCode>('/admin/promo-codes', request)
  return data
}

/**
 * update updates administrator promo codes and returns the server response data.
 */
export async function update(id: number, request: UpdatePromoCodeRequest): Promise<PromoCode> {
  const { data } = await apiClient.put<PromoCode>(`/admin/promo-codes/${id}`, request)
  return data
}

/**
 * deleteCode removes code and returns the server response data.
 */
export async function deleteCode(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/promo-codes/${id}`)
  return data
}

/**
 * getUsages retrieves usages and returns the server response data. Page defaults
 * to 1, pageSize defaults to 20. The function returns the data portion of the
 * completed response.
 */
export async function getUsages(
  id: number,
  page: number = 1,
  pageSize: number = 20,
): Promise<BasePaginationResponse<PromoCodeUsage>> {
  const { data } = await apiClient.get<BasePaginationResponse<PromoCodeUsage>>(
    `/admin/promo-codes/${id}/usages`,
    { params: { page, page_size: pageSize } },
  )
  return data
}

/**
 * promoAPI groups promotion code records and usage lists for callers using the
 * module's named or default API object.
 */
const promoAPI = {
  list,
  getById,
  create,
  update,
  delete: deleteCode,
  getUsages,
}

export default promoAPI
