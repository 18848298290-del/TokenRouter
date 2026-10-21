import { apiClient } from './client'
import type { MarketplaceGroup, MarketplaceStats } from '@/api/marketplace.types'

/**
 * marketplace.ts retrieves public model-marketplace groups and summary
 * statistics. The model query disables capacity aggregation because the
 * marketplace display uses prices and availability, and both responses are
 * returned through the shared browser API client.
 */

/**
 * getMarketplaceModels retrieves marketplace models and returns the server
 * response data.
 */
export async function getMarketplaceModels(): Promise<MarketplaceGroup[]> {
  /**
   * The marketplace needs prices and availability for display; skipping capacity
   * aggregation reduces the server work needed for this request.
   */
  const { data } = await apiClient.get<MarketplaceGroup[]>('/marketplace/models', {
    params: { include_capacity: false },
  })
  return data
}

/**
 * getMarketplaceStats retrieves marketplace stats and returns the server
 * response data.
 */
export async function getMarketplaceStats(): Promise<MarketplaceStats> {
  const { data } = await apiClient.get<MarketplaceStats>('/marketplace/stats')
  return data
}

/**
 * marketplaceAPI groups public model-marketplace groups and statistics for
 * callers using the module's named or default API object.
 */
export const marketplaceAPI = {
  getMarketplaceModels,
  getMarketplaceStats,
}

export default marketplaceAPI
