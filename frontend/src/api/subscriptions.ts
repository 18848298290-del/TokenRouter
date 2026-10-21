import { apiClient } from './client'
import type { UserSubscription } from '@/api/subscriptions.types'

/**
 * subscriptions.ts retrieves the signed-in user's subscription lists and submits
 * exhausted-subscription revocation. The revocation result exposes the revoked
 * record, any successor and rebound-key count, allowing callers to refresh
 * subscription and API-key state after the operation.
 */

/**
 * RevokeSubscriptionResponse identifies the revoked subscription, any selected
 * successor and the number of API keys rebound by the server.
 */
export interface RevokeSubscriptionResponse {
  revoked_subscription_id: number // Revoked subscription ID.
  replacement_subscription_id: number | null // Replacement subscription ID.
  rebound_api_key_count: number // Rebound API key count.
}

/**
 * getMySubscriptions retrieves list of current user's subscriptions and returns
 * the server result.
 */
export async function getMySubscriptions(): Promise<UserSubscription[]> {
  const response = await apiClient.get<UserSubscription[]>('/subscriptions')
  return response.data
}

/**
 * getActiveSubscriptions retrieves current user's active subscriptions and
 * returns the server result.
 */
export async function getActiveSubscriptions(): Promise<UserSubscription[]> {
  const response = await apiClient.get<UserSubscription[]>('/subscriptions/active')
  return response.data
}

/**
 * revokeExhaustedSubscription requests revocation of the current user's
 * exhausted subscription. The response includes a successor and rebound-key
 * count when the server transfers keys with explicit subscription bindings to a
 * continuation package.
 */
export async function revokeExhaustedSubscription(
  subscriptionId: number,
): Promise<RevokeSubscriptionResponse> {
  const response = await apiClient.post<RevokeSubscriptionResponse>(
    `/subscriptions/${subscriptionId}/revoke`,
  )
  return response.data
}

export default {
  getMySubscriptions,
  getActiveSubscriptions,
  revokeExhaustedSubscription,
}
