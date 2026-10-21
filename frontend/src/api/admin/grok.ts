import { apiClient } from '../client'
import type { GrokBillingSummary, GrokQuotaWindow, WindowStats } from '@/api/admin/providers.types'

/**
 * grok.ts sends administrator xAI authorization, SSO import and quota requests.
 * Authorization URL and code-exchange functions retain their session fields,
 * while password authorization accepts a combined email and password string from
 * the form. That string can contain the separator inside the password, so the
 * split uses its first occurrence and trims only the email.
 *
 * SSO import request budgets grow with groups of three submitted tokens plus
 * fixed overhead, whereas individual password and SSO conversion requests use a
 * two-minute budget. Quota responses keep upstream observations, local usage,
 * persistence state and reset support separate so callers can display what each
 * probe actually returned.
 */

export type { GrokBillingSummary, GrokQuotaWindow } from '@/api/admin/providers.types'

/**
 * GrokAuthUrlResponse pairs the xAI authorization URL with its session and
 * correlation state.
 */
export interface GrokAuthUrlResponse {
  auth_url: string // Authorization navigation URL.
  session_id: string // Authorization session identifier.
  state: string // OAuth correlation state.
}

/**
 * GrokAuthUrlRequest selects optional proxy and redirect parameters for starting
 * xAI authorization.
 */
export interface GrokAuthUrlRequest {
  proxy_id?: number // Selected proxy identifier.
  redirect_uri?: string // OAuth callback URL.
}

/**
 * GrokOAuthCapabilities reports whether the server permits the xAI password
 * authorization flow.
 */
export interface GrokOAuthCapabilities {
  password_auth_enabled: boolean // Password auth enabled.
}

/**
 * GROK_AUTHORIZATION_TIMEOUT_MS gives SSO conversion and password authorization
 * two minutes before the browser request times out.
 */
const GROK_AUTHORIZATION_TIMEOUT_MS = 120_000

/**
 * getCapabilities retrieves capabilities and returns the server response data.
 */
export async function getCapabilities(): Promise<GrokOAuthCapabilities> {
  const { data } = await apiClient.get<GrokOAuthCapabilities>('/admin/grok/oauth/capabilities')
  return data
}

/**
 * GrokExchangeCodeRequest submits an xAI authorization code with its matching
 * session and redirect context.
 */
export interface GrokExchangeCodeRequest {
  session_id: string // Authorization session identifier.
  state: string // OAuth correlation state.
  code: string // OAuth authorization code.
  proxy_id?: number // Selected proxy identifier.
  redirect_uri?: string // OAuth callback URL.
}

/**
 * GrokTokenInfo carries xAI credentials and optional account, team and
 * entitlement information returned by authorization.
 */
export interface GrokTokenInfo {
  access_token?: string // Bearer access token.
  refresh_token?: string // Session rotation token.
  token_type?: string // Authorization scheme name.
  id_token?: string // Recorded id token.
  expires_at?: number | string // Recorded expiration timestamp.
  expires_in?: number // Lifetime in seconds.
  scope?: string // Requested access scope.
  client_id?: string // OAuth client identifier.
  email?: string // Account email address.
  sub?: string // OAuth subject identifier.
  team_id?: string // Team record identifier.
  subscription_tier?: string // Recorded subscription tier.
  entitlement_status?: string // Recorded entitlement status.
  [key: string]: unknown // Additional token response fields.
}

/**
 * GrokSSOToOAuthRequest combines submitted SSO tokens with the provider settings
 * requested for imported accounts.
 */
export interface GrokSSOToOAuthRequest {
  sso_tokens: string[] // Requested sso tokens.
  name?: string // Human-readable display name.
  notes?: string | null // Administrator supplied notes.
  proxy_id?: number | null // Selected proxy identifier.
  group_ids?: number[] // Requested group ids.
  credentials?: Record<string, unknown> // Submitted provider credentials.
  extra?: Record<string, unknown> // Additional provider metadata.
  concurrency?: number // Concurrent request limit.
  load_factor?: number // Requested load factor.
  priority?: number // Configured selection priority.
  rate_multiplier?: number // Configured billing multiplier.
  expires_at?: number | null // Recorded expiration timestamp.
  auto_pause_on_expired?: boolean // Auto pause on expired.
}

/**
 * GrokSSOToOAuthItemResult identifies an individual SSO import by input index
 * and records its provider or error.
 */
export interface GrokSSOToOAuthItemResult {
  index: number // Zero-based entry index.
  name?: string // Human-readable display name.
  email?: string // Account email address.
  provider?: unknown // Provider identity details.
  error?: string // Operation failure detail.
}

/**
 * GrokSSOToOAuthResponse separates created and failed SSO imports while
 * retaining per-input result records.
 */
export interface GrokSSOToOAuthResponse {
  created: GrokSSOToOAuthItemResult[] // Created provider entries.
  failed: GrokSSOToOAuthItemResult[] // Failed result entries.
}

/**
 * GROK_SSO_IMPORT_CONCURRENCY groups submitted SSO tokens into batches of three
 * when estimating the browser timeout budget.
 */
const GROK_SSO_IMPORT_CONCURRENCY = 3

/**
 * GROK_SSO_IMPORT_TIMEOUT_PER_BATCH_MS reserves ninety seconds for each
 * estimated import batch in the request timeout calculation.
 */
const GROK_SSO_IMPORT_TIMEOUT_PER_BATCH_MS = 90_000

/**
 * GROK_SSO_IMPORT_TIMEOUT_BUFFER_MS adds ninety seconds beyond estimated batch
 * processing when createFromSSO chooses its request timeout.
 */
const GROK_SSO_IMPORT_TIMEOUT_BUFFER_MS = 90_000

/**
 * getGrokSSOImportTimeout calculates the SSO-import request budget in
 * milliseconds. It reserves ninety seconds per group of three keys plus ninety
 * seconds of overhead, with at least one batch even for nonpositive counts.
 */
export function getGrokSSOImportTimeout(keyCount: number): number {
  const batches = Math.ceil(Math.max(1, keyCount) / GROK_SSO_IMPORT_CONCURRENCY)
  return batches * GROK_SSO_IMPORT_TIMEOUT_PER_BATCH_MS + GROK_SSO_IMPORT_TIMEOUT_BUFFER_MS
}

/**
 * GrokQuotaSnapshot describes observed request and token windows with their
 * source and last observation times.
 */
export interface GrokQuotaSnapshot {
  requests?: GrokQuotaWindow | null // Request quota window.
  tokens?: GrokQuotaWindow | null // Observed token window.
  retry_after_seconds?: number | null // Retry after seconds.
  subscription_tier?: string // Recorded subscription tier.
  entitlement_status?: string // Recorded entitlement status.
  status_code?: number // HTTP status code.
  headers?: Record<string, string> // Recorded response headers.
  headers_observed: boolean // Recorded headers observed.
  observation_source?: string // Recorded observation source.
  last_probe_at?: string // Last probe at.
  last_headers_seen_at?: string // Last headers seen at.
  updated_at: string // Last modification timestamp.
}

/**
 * GrokQuotaProbeResult combines probe observations, optional billing and local
 * usage with persistence and reset support flags.
 */
export interface GrokQuotaProbeResult {
  source: 'active_probe' | 'billing_probe' | 'hybrid_probe' // Recorded observation source.
  model?: string // Model identifier value.
  billing?: GrokBillingSummary | null // Observed billing summary.
  snapshot?: GrokQuotaSnapshot | null // Observed quota snapshot.
  local_usage_24h?: WindowStats | null // Local usage 24h.
  local_usage_7d?: WindowStats | null // Local usage 7d.
  local_usage_monthly?: WindowStats | null // Local usage monthly.
  status_code?: number // HTTP status code.
  headers_observed: boolean // Recorded headers observed.
  reset_supported: boolean // Recorded reset supported.
  fetched_at: number // Observation fetch timestamp.
  persisted?: boolean // Snapshot persistence flag.
  probe_error?: string // Recorded probe error.
}

/**
 * GrokQuotaResetResult reports whether the requested quota reset is supported
 * and returns its result code and message.
 */
export interface GrokQuotaResetResult {
  supported: boolean // Capability support flag.
  code: string // Server result code.
  message: string // Server-provided result message.
}

/**
 * generateAuthUrl submits generate auth url and returns the server response
 * data.
 */
export async function generateAuthUrl(payload: GrokAuthUrlRequest): Promise<GrokAuthUrlResponse> {
  const { data } = await apiClient.post<GrokAuthUrlResponse>('/admin/grok/oauth/auth-url', payload)
  return data
}

/**
 * exchangeCode submits exchange code and returns the server response data.
 */
export async function exchangeCode(payload: GrokExchangeCodeRequest): Promise<GrokTokenInfo> {
  const { data } = await apiClient.post<GrokTokenInfo>('/admin/grok/oauth/exchange-code', payload)
  return data
}

/**
 * refreshGrokToken exchanges the supplied refresh token for token metadata.
 * A truthy proxy identifier is forwarded; zero, null, and omission leave it out
 * of the request. The completed response data is returned without reshaping it.
 */
export async function refreshGrokToken(
  refreshToken: string,
  proxyId?: number | null,
): Promise<GrokTokenInfo> {
  const payload: Record<string, unknown> = { refresh_token: refreshToken }
  if (proxyId) payload.proxy_id = proxyId

  const { data } = await apiClient.post<GrokTokenInfo>('/admin/grok/oauth/refresh-token', payload)
  return data
}

/**
 * queryQuota retrieves quota and returns the server response data.
 */
export async function queryQuota(id: number): Promise<GrokQuotaProbeResult> {
  const { data } = await apiClient.get<GrokQuotaProbeResult>(`/admin/grok/providers/${id}/quota`)
  return data
}

/**
 * resetQuota submits quota and returns the server response data.
 */
export async function resetQuota(id: number): Promise<GrokQuotaResetResult> {
  const { data } = await apiClient.post<GrokQuotaResetResult>(
    `/admin/grok/providers/${id}/reset-quota`,
  )
  return data
}

/**
 * createFromSSO submits from sso and returns the server response data. The
 * request uses the getGrokSSOImportTimeout timeout budget. The function returns
 * the data portion of the completed response.
 */
export async function createFromSSO(
  payload: GrokSSOToOAuthRequest,
): Promise<GrokSSOToOAuthResponse> {
  const { data } = await apiClient.post<GrokSSOToOAuthResponse>(
    '/admin/grok/sso-to-oauth',
    payload,
    { timeout: getGrokSSOImportTimeout(payload.sso_tokens.length) },
  )
  return data
}

/**
 * validateSSOToken exchanges a browser SSO cookie for Build OAuth credentials
 * through the administrator endpoint. A truthy proxy ID is included, and the
 * request uses the longer authorization timeout. The request uses the
 * GROK_AUTHORIZATION_TIMEOUT_MS timeout budget.
 */
export async function validateSSOToken(
  ssoToken: string,
  proxyId?: number | null,
): Promise<GrokTokenInfo> {
  const payload: Record<string, unknown> = { sso_token: ssoToken }
  if (proxyId) payload.proxy_id = proxyId
  const { data } = await apiClient.post<GrokTokenInfo>('/admin/grok/oauth/sso-token', payload, {
    timeout: GROK_AUTHORIZATION_TIMEOUT_MS,
  })
  return data
}

/**
 * authorizePassword extracts email and password for administrator xAI
 * authorization. It splits at the first four-hyphen separator so later separator
 * characters remain in the password, and trims only the email portion. When the
 * separator is absent, the entire input becomes email and the password is empty.
 * The request includes a truthy proxy ID and uses the two-minute authorization
 * budget before returning credential data.
 */
export async function authorizePassword(
  emailAndPassword: string,
  proxyId?: number | null,
): Promise<GrokTokenInfo> {
  /**
   * Split at the first email----password separator because a password may itself
   * contain the separator, and trimming applies only to email.
   */
  const sep = '----'
  const idx = emailAndPassword.indexOf(sep)
  const email = (idx >= 0 ? emailAndPassword.slice(0, idx) : emailAndPassword).trim()
  const password = idx >= 0 ? emailAndPassword.slice(idx + sep.length) : ''
  const payload: Record<string, unknown> = { email, password }
  if (proxyId) payload.proxy_id = proxyId
  const { data } = await apiClient.post<GrokTokenInfo>('/admin/grok/oauth/password', payload, {
    timeout: GROK_AUTHORIZATION_TIMEOUT_MS,
  })
  return data
}

export default {
  generateAuthUrl,
  getCapabilities,
  exchangeCode,
  refreshGrokToken,
  queryQuota,
  resetQuota,
  createFromSSO,
  validateSSOToken,
  authorizePassword,
}
