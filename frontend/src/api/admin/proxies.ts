import { apiClient } from '../client'
import type {
  Proxy,
  ProxyProviderSummary,
  ProxyQualityCheckResult,
  CreateProxyRequest,
  UpdateProxyRequest,
} from '@/api/admin/proxies.types'
import type { PaginatedResponse } from '@/api/common.types'
import type { AdminDataPayload, AdminDataImportResult } from '@/api/admin/providers.types'

/**
 * proxies.ts manages administrator proxy records, connectivity checks and data
 * import or export. Proxy choices can include provider counts, and separate
 * endpoints return provider assignments or quality observations for the selected
 * proxy before a form changes its configuration.
 */

/**
 * list lists all proxies with pagination. Page defaults to 1, pageSize defaults
 * to 20; the caller may cancel through the supplied AbortSignal. The function
 * returns the data portion of the completed response.
 */
export async function list(
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    protocol?: string // Selected protocol identifier.
    status?: 'active' | 'inactive' | 'expired' // Current server-reported status.
    search?: string // Free-text search query.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<PaginatedResponse<Proxy>> {
  const { data } = await apiClient.get<PaginatedResponse<Proxy>>('/admin/proxies', {
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
 * getAll retrieves all active proxies (without pagination) and returns the
 * server result.
 */
export async function getAll(): Promise<Proxy[]> {
  const { data } = await apiClient.get<Proxy[]>('/admin/proxies/all')
  return data
}

/**
 * getAllWithCount retrieves all active proxies with provider count (sorted by
 * creation time desc).
 */
export async function getAllWithCount(): Promise<Proxy[]> {
  const { data } = await apiClient.get<Proxy[]>('/admin/proxies/all', {
    params: { with_count: 'true' },
  })
  return data
}

/**
 * create creates new proxy and returns the server result.
 */
export async function create(proxyData: CreateProxyRequest): Promise<Proxy> {
  const { data } = await apiClient.post<Proxy>('/admin/proxies', proxyData)
  return data
}

/**
 * update updates proxy and returns the server result.
 */
export async function update(id: number, updates: UpdateProxyRequest): Promise<Proxy> {
  const { data } = await apiClient.put<Proxy>(`/admin/proxies/${id}`, updates)
  return data
}

/**
 * deleteProxy deletes proxy and returns the server result.
 */
export async function deleteProxy(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/proxies/${id}`)
  return data
}

/**
 * testProxy tests proxy connectivity and returns the server result.
 */
export async function testProxy(id: number): Promise<{
  success: boolean // Operation success flag.
  message: string // Server-provided result message.
  latency_ms?: number // Latency in milliseconds.
  ip_address?: string // Recorded ip address.
  city?: string // Observed endpoint city.
  region?: string // Service deployment region.
  country?: string // Observed endpoint country.
  country_code?: string // Recorded country code.
}> {
  const { data } = await apiClient.post<{
    success: boolean // Operation success flag.
    message: string // Server-provided result message.
    latency_ms?: number // Latency in milliseconds.
    ip_address?: string // Recorded ip address.
    city?: string // Observed endpoint city.
    region?: string // Service deployment region.
    country?: string // Observed endpoint country.
    country_code?: string // Recorded country code.
  }>(`/admin/proxies/${id}/test`)
  return data
}

/**
 * checkProxyQuality checks proxy quality across common AI targets.
 */
export async function checkProxyQuality(id: number): Promise<ProxyQualityCheckResult> {
  const { data } = await apiClient.post<ProxyQualityCheckResult>(
    `/admin/proxies/${id}/quality-check`,
  )
  return data
}

/**
 * getProxyProviders retrieves providers using a proxy and returns the server
 * result.
 */
export async function getProxyProviders(id: number): Promise<ProxyProviderSummary[]> {
  const { data } = await apiClient.get<ProxyProviderSummary[]>(`/admin/proxies/${id}/providers`)
  return data
}

/**
 * batchCreate submits batch requests to create proxies and returns the server
 * result.
 */
export async function batchCreate(
  proxies: Array<{
    protocol: string // Selected protocol identifier.
    host: string // Service host address.
    port: number // Service TCP port.
    username?: string // Account display username.
    password?: string // Submitted password text.
  }>,
): Promise<{
  created: number // Created record count.
  skipped: number // Skipped record count.
}> {
  const { data } = await apiClient.post<{
    created: number // Created record count.
    skipped: number // Skipped record count.
  }>('/admin/proxies/batch', { proxies })
  return data
}

/**
 * batchDelete submits batch delete and returns the server response data.
 */
export async function batchDelete(ids: number[]): Promise<{
  deleted_ids: number[] // Recorded deleted ids.
  skipped: Array<{
    id: number // Record identifier value.
    reason: string // Server-provided outcome reason.
  }> // Skipped IDs and reasons.
}> {
  const { data } = await apiClient.post<{
    deleted_ids: number[] // Recorded deleted ids.
    skipped: Array<{
      id: number // Record identifier value.
      reason: string // Server-provided outcome reason.
    }> // Skipped IDs and reasons.
  }>('/admin/proxies/batch-delete', { ids })
  return data
}

/**
 * exportData retrieves proxy records for an importable export document. A
 * nonempty IDs list takes precedence over every filter; filters are sent only
 * when IDs are absent or empty.
 */
export async function exportData(options?: {
  ids?: number[] // Selected record identifiers.
  filters?: {
    protocol?: string // Selected protocol identifier.
    status?: 'active' | 'inactive' | 'expired' // Current server-reported status.
    search?: string // Free-text search query.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  } // Applied query filters.
}): Promise<AdminDataPayload> {
  const params: Record<string, string> = {}
  if (options?.ids && options.ids.length > 0) {
    params.ids = options.ids.join(',')
  } else if (options?.filters) {
    const { protocol, status, search, sort_by, sort_order } = options.filters
    if (protocol) params.protocol = protocol
    if (status) params.status = status
    if (search) params.search = search
    if (sort_by) params.sort_by = sort_by
    if (sort_order) params.sort_order = sort_order
  }
  const { data } = await apiClient.get<AdminDataPayload>('/admin/proxies/data', { params })
  return data
}

/**
 * importData submits import data and returns the server response data.
 */
export async function importData(payload: {
  data: AdminDataPayload // Submitted proxy import document.
}): Promise<AdminDataImportResult> {
  const { data } = await apiClient.post<AdminDataImportResult>('/admin/proxies/data', payload)
  return data
}

/**
 * proxiesAPI groups administrator proxy records and connectivity checks for
 * callers using the module's named or default API object.
 */
export const proxiesAPI = {
  list,
  getAll,
  getAllWithCount,
  create,
  update,
  delete: deleteProxy,
  testProxy,
  checkProxyQuality,
  getProxyProviders,
  batchCreate,
  batchDelete,
  exportData,
  importData,
}

export default proxiesAPI
