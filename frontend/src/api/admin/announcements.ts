import { apiClient } from '../client'
import type {
  Announcement,
  AnnouncementUserReadStatus,
  CreateAnnouncementRequest,
  UpdateAnnouncementRequest,
} from '@/api/announcements.types'
import type { BasePaginationResponse } from '@/api/common.types'

/**
 * announcements.ts sends administrator announcement edits and read-status
 * queries. Announcement pages and per-announcement readership lists accept
 * independent pagination and cancellation, while creation and update requests
 * use the shared announcement payload types for localized publication data.
 */

/**
 * list retrieves administrator announcements and returns the server response
 * data. Page defaults to 1, pageSize defaults to 20; the caller may cancel
 * through the supplied AbortSignal.
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
): Promise<BasePaginationResponse<Announcement>> {
  const { data } = await apiClient.get<BasePaginationResponse<Announcement>>(
    '/admin/announcements',
    {
      params: { page, page_size: pageSize, ...filters },
      signal: options?.signal,
    },
  )
  return data
}

/**
 * getById retrieves by id and returns the server response data.
 */
export async function getById(id: number): Promise<Announcement> {
  const { data } = await apiClient.get<Announcement>(`/admin/announcements/${id}`)
  return data
}

/**
 * create submits administrator announcements and returns the server response
 * data.
 */
export async function create(request: CreateAnnouncementRequest): Promise<Announcement> {
  const { data } = await apiClient.post<Announcement>('/admin/announcements', request)
  return data
}

/**
 * update updates administrator announcements and returns the server response
 * data.
 */
export async function update(
  id: number,
  request: UpdateAnnouncementRequest,
): Promise<Announcement> {
  const { data } = await apiClient.put<Announcement>(`/admin/announcements/${id}`, request)
  return data
}

/**
 * deleteAnnouncement removes announcement and returns the server response data.
 */
export async function deleteAnnouncement(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/announcements/${id}`)
  return data
}

/**
 * getReadStatus retrieves read status and returns the server response data. Page
 * defaults to 1, pageSize defaults to 20; the caller may cancel through the
 * supplied AbortSignal.
 */
export async function getReadStatus(
  id: number,
  page: number = 1,
  pageSize: number = 20,
  filters?: {
    search?: string // Free-text search query.
    sort_by?: string // Requested sort column.
    sort_order?: 'asc' | 'desc' // Requested ordering direction.
  },
  options?: {
    signal?: AbortSignal // Caller-owned cancellation signal.
  },
): Promise<BasePaginationResponse<AnnouncementUserReadStatus>> {
  const { data } = await apiClient.get<BasePaginationResponse<AnnouncementUserReadStatus>>(
    `/admin/announcements/${id}/read-status`,
    {
      params: { page, page_size: pageSize, ...filters },
      signal: options?.signal,
    },
  )
  return data
}

/**
 * announcementsAPI groups announcement records and read status for callers using
 * the module's named or default API object.
 */
const announcementsAPI = {
  list,
  getById,
  create,
  update,
  delete: deleteAnnouncement,
  getReadStatus,
}

export default announcementsAPI
