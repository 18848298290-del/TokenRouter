import { apiClient } from './client'
import type { UserAnnouncement } from '@/api/announcements.types'

/**
 * announcements.ts retrieves personalized announcements and records read
 * acknowledgments. List callers can request unread entries or force a fresh URL
 * with a timestamp, while each read acknowledgment targets one announcement
 * identifier through the authenticated client.
 */

/**
 * list retrieves personalized announcements with optional unread filtering and
 * cache bypass. unreadOnly adds unread_only=1, while bypassCache adds the
 * current timestamp to the URL so a forced refresh uses a fresh request.
 */
export async function list(
  unreadOnly: boolean = false,
  bypassCache: boolean = false,
): Promise<UserAnnouncement[]> {
  const params: Record<string, number> = {}
  if (unreadOnly) params.unread_only = 1

  /**
   * A forced refresh adds a timestamp to the URL so personalized announcement data
   * is requested again despite browser caching.
   */
  if (bypassCache) params.refresh_timestamp = Date.now()

  const { data } = await apiClient.get<UserAnnouncement[]>('/announcements', {
    params,
  })
  return data
}

/**
 * markRead submits mark read and returns the server response data.
 */
export async function markRead(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.post<{
    message: string // Server-provided result message.
  }>(`/announcements/${id}/read`)
  return data
}

/**
 * announcementsAPI groups announcement records and read status for callers using
 * the module's named or default API object.
 */
const announcementsAPI = {
  list,
  markRead,
}

export default announcementsAPI
