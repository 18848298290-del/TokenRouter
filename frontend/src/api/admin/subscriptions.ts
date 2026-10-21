import { apiClient } from '../client'
import type {
  UserSubscription,
  AssignSubscriptionRequest,
  ExtendSubscriptionRequest,
} from '@/api/subscriptions.types'
import type { PaginatedResponse } from '@/api/common.types'

/**
 * subscriptions.ts retrieves and modifies administrator subscription
 * assignments. List requests support filters and cancellation, while assignment,
 * extension, usage reset and revocation have separate endpoints so subscription
 * management views can submit the operation they selected.
 */

/**
 * list lists all subscriptions with pagination. Page defaults to 1, pageSize
 * defaults to 20; the caller may cancel through the supplied AbortSignal. The
 * function returns the data portion of the completed response.
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    status?: 'active' | 'pending' | 'expired' | 'suspended' | 'revoked' // Current server-reported status.
    user_id?: number // Owning user identifier.
    plan_id?: number // Subscription plan identifier.
    platform?: string // Provider platform identifier.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<PaginatedResponse<UserSubscription>> {
  const { data } = await apiClient.get<PaginatedResponse<UserSubscription>>(
    '/admin/subscriptions',
    {
      params: {
        page,
        page_size: pageSize,
        ...filters,
      },
      signal: options?.signal,
    },
  )
  return data
}

/**
 * assign assigns subscription to user and returns the server result.
 */
export async function assign(request: AssignSubscriptionRequest): Promise<UserSubscription> {
  const { data } = await apiClient.post<UserSubscription>('/admin/subscriptions/assign', request)
  return data
}

/**
 * extend extends subscription validity and returns the server result.
 */
export async function extend(
  id: number,
  request: ExtendSubscriptionRequest,
): Promise<UserSubscription> {
  const { data } = await apiClient.post<UserSubscription>(
    `/admin/subscriptions/${id}/extend`,
    request,
  )
  return data
}

/**
 * revoke revokes subscription and returns the server result.
 */
export async function revoke(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.post<{
    message: string // Server-provided result message.
  }>(`/admin/subscriptions/${id}/revoke`)
  return data
}

/**
 * restore submits restore and returns the server response data.
 */
export async function restore(id: number): Promise<UserSubscription> {
  const { data } = await apiClient.post<UserSubscription>(`/admin/subscriptions/${id}/restore`)
  return data
}

/**
 * resetQuota resets daily, weekly, and/or monthly usage quota for a
 * subscription.
 */
export async function resetQuota(
  id: number,
  options: {
    daily: boolean // Daily reset selection.
    weekly: boolean // Weekly reset selection.
    monthly: boolean // Monthly reset selection.
  },
): Promise<UserSubscription> {
  const { data } = await apiClient.post<UserSubscription>(
    `/admin/subscriptions/${id}/reset-quota`,
    options,
  )
  return data
}

/**
 * subscriptionsAPI groups subscription records and administrative adjustments
 * for callers using the module's named or default API object.
 */
export const subscriptionsAPI = {
  list,
  assign,
  extend,
  revoke,
  restore,
  resetQuota,
}

export default subscriptionsAPI
