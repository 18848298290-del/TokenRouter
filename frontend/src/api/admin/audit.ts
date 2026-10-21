import { apiClient } from '../client'
import type { PaginatedResponse } from '@/api/common.types'

/**
 * audit.ts reads administrator operation logs and submits requests to clear the
 * entire log. List and detail operations return recorded request metadata;
 * clearing supplies the current TOTP code for the server to authorize that
 * operation.
 */

/**
 * AuditLog records an administrator action with request identity, actor,
 * sanitized input and HTTP outcome.
 */
export interface AuditLog {
  id: number // Record identifier value.
  created_at: string // Record creation timestamp.
  actor_user_id?: number // Actor user ID.
  actor_email: string // Recorded actor email.
  actor_role: string // Recorded actor role.
  auth_method: string // Recorded auth method.
  credential_masked: string // Recorded credential masked.
  action: string // Selected outcome action.
  method: string // HTTP request method.
  path: string // HTTP request path.
  request_id: string // Unified request identifier.
  client_ip: string // Recorded client ip.
  user_agent: string // Recorded user agent.
  request_body?: string // Recorded request body.
  status_code: number // HTTP status code.
  latency_ms: number // Latency in milliseconds.
  extra?: Record<string, any> // Additional provider metadata.
}

/**
 * AuditLogQuery selects audit records by time, actor, request identity and
 * operation filters.
 */
export interface AuditLogQuery {
  request_id?: string // Unified request identifier.
  page?: number // Requested page number.
  page_size?: number // Requested page size.
  start_time?: string // Requested interval start.
  end_time?: string // Requested interval end.
  actor_user_id?: number // Actor user ID.
  actor_email?: string // Requested actor email.
  auth_method?: string // Requested auth method.
  action?: string // Selected outcome action.
  method?: string // HTTP request method.
  client_ip?: string // Requested client ip.
  // Optional result filter: trimmed "true" selects HTTP statuses below 400;
  // other nonempty strings select failures, while empty or omitted values disable filtering.
  success?: string
  q?: string // Free-text search query.
}

/**
 * AuditLogListResponse wraps audit records in the shared pagination response
 * used by administrator tables.
 */
export type AuditLogListResponse = PaginatedResponse<AuditLog>

/**
 * list retrieves paginated administrator audit records using the supplied
 * filters.
 */
export async function list(params: AuditLogQuery): Promise<AuditLogListResponse> {
  const { data } = await apiClient.get('/admin/audit-logs', { params })
  return data
}

/**
 * get retrieves one audit record including its server-sanitized request body.
 */
export async function get(id: number): Promise<AuditLog> {
  const { data } = await apiClient.get(`/admin/audit-logs/${id}`)
  return data
}

/**
 * clear requests deletion of all audit logs using the supplied TOTP code. The
 * server checks this proof for the current administrator; callers collect a
 * fresh code before submitting the request.
 */
export async function clear(totpCode: string): Promise<{
  deleted: number // Deleted record count.
}> {
  const { data } = await apiClient.post('/admin/audit-logs/clear', { totp_code: totpCode })
  return data
}

/**
 * auditAPI groups administrator operation audit records for callers using the
 * module's named or default API object.
 */
export const auditAPI = {
  list,
  get,
  clear,
}

export default auditAPI
