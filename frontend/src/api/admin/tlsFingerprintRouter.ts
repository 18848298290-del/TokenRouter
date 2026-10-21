import { apiClient } from '../client'

/**
 * tlsFingerprintRouter.ts maintains User-Agent routing rules for TLS fingerprint
 * profiles. Router records retain ordered matching rules and separate
 * OAuth-token or Codex reset overrides, while create and update requests carry
 * those fields to administrator endpoints.
 */

/**
 * TLSFingerprintRouterMatchType selects substring, prefix, exact or
 * regular-expression matching for a User-Agent rule.
 */
export type TLSFingerprintRouterMatchType = 'contains' | 'prefix' | 'exact' | 'regex'

/**
 * TLSFingerprintRouterRule pairs a User-Agent match with its selected TLS
 * profile and optional upstream header overrides.
 */
export interface TLSFingerprintRouterRule {
  name: string // Human-readable display name.
  enabled: boolean // Feature activation flag.
  match_type: TLSFingerprintRouterMatchType // Recorded match type.
  pattern: string // Configured match pattern.
  case_sensitive: boolean // Recorded case sensitive.
  tls_fingerprint_profile_id: number // TLS fingerprint profile ID.
  upstream_user_agent?: string // Upstream user agent.
  upstream_originator?: string // Recorded upstream originator.
}

/**
 * TLSFingerprintRouter stores ordered matching rules plus separate token-refresh
 * and invite-reset request overrides.
 */
export interface TLSFingerprintRouter {
  id: number // Record identifier value.
  name: string // Human-readable display name.
  description: string | null // Human-readable record description.
  enabled: boolean // Feature activation flag.
  chatgpt_oauth_token_user_agent?: string // Chatgpt OAuth token user agent.
  chatgpt_oauth_token_tls_fingerprint_profile_id?: number | null // Chatgpt OAuth token TLS fingerprint profile ID.
  codex_invite_reset_user_agent?: string // Codex invite reset user agent.
  codex_invite_reset_tls_fingerprint_profile_id?: number | null // Codex invite reset TLS fingerprint profile ID.
  rules: TLSFingerprintRouterRule[] // Configured matching rules.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
}

/**
 * CreateTLSFingerprintRouterRequest supplies a router name and optional matching
 * or special-request settings for creation.
 */
export interface CreateTLSFingerprintRouterRequest {
  name: string // Human-readable display name.
  description?: string | null // Human-readable record description.
  enabled?: boolean // Feature activation flag.
  chatgpt_oauth_token_user_agent?: string // Chatgpt OAuth token user agent.
  chatgpt_oauth_token_tls_fingerprint_profile_id?: number | null // Chatgpt OAuth token TLS fingerprint profile ID.
  codex_invite_reset_user_agent?: string // Codex invite reset user agent.
  codex_invite_reset_tls_fingerprint_profile_id?: number | null // Codex invite reset TLS fingerprint profile ID.
  rules?: TLSFingerprintRouterRule[] // Configured matching rules.
}

/**
 * UpdateTLSFingerprintRouterRequest supplies selected router edits, including
 * matching rules and special-request TLS profile choices.
 */
export interface UpdateTLSFingerprintRouterRequest {
  name?: string // Human-readable display name.
  description?: string | null // Human-readable record description.
  enabled?: boolean // Feature activation flag.
  chatgpt_oauth_token_user_agent?: string // Chatgpt OAuth token user agent.
  chatgpt_oauth_token_tls_fingerprint_profile_id?: number | null // Chatgpt OAuth token TLS fingerprint profile ID.
  codex_invite_reset_user_agent?: string // Codex invite reset user agent.
  codex_invite_reset_tls_fingerprint_profile_id?: number | null // Codex invite reset TLS fingerprint profile ID.
  rules?: TLSFingerprintRouterRule[] // Configured matching rules.
}

/**
 * list retrieves administrator tls fingerprint routers and returns the server
 * response data.
 */
export async function list(): Promise<TLSFingerprintRouter[]> {
  const { data } = await apiClient.get<TLSFingerprintRouter[]>('/admin/tls-fingerprint-routers')
  return data
}

/**
 * create submits administrator tls fingerprint routers and returns the server
 * response data.
 */
export async function create(
  payload: CreateTLSFingerprintRouterRequest,
): Promise<TLSFingerprintRouter> {
  const { data } = await apiClient.post<TLSFingerprintRouter>(
    '/admin/tls-fingerprint-routers',
    payload,
  )
  return data
}

/**
 * update updates administrator tls fingerprint routers and returns the server
 * response data.
 */
export async function update(
  id: number,
  payload: UpdateTLSFingerprintRouterRequest,
): Promise<TLSFingerprintRouter> {
  const { data } = await apiClient.put<TLSFingerprintRouter>(
    `/admin/tls-fingerprint-routers/${id}`,
    payload,
  )
  return data
}

/**
 * deleteRouter removes router and returns the server response data.
 */
export async function deleteRouter(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/tls-fingerprint-routers/${id}`)
  return data
}

/**
 * tlsFingerprintRouterAPI groups User-Agent routing rules for TLS fingerprints
 * for callers using the module's named or default API object.
 */
export const tlsFingerprintRouterAPI = {
  list,
  create,
  update,
  delete: deleteRouter,
}

export default tlsFingerprintRouterAPI
