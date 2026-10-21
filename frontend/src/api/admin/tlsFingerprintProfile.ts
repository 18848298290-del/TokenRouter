import { apiClient } from '../client'

/**
 * tlsFingerprintProfile.ts maintains administrator TLS profiles and operates the
 * fingerprint collector. Profile records retain ordered handshake parameters,
 * while collector sessions supply capture URLs and tokens used to retrieve
 * captured client fingerprints from their session-specific endpoint.
 */

/**
 * TLSFingerprintProfile stores ordered TLS handshake parameters and their
 * administrator-facing profile identity.
 */
export interface TLSFingerprintProfile {
  id: number // Record identifier value.
  name: string // Human-readable display name.
  description: string | null // Human-readable record description.
  enable_grease: boolean // Recorded enable grease.
  cipher_suites: number[] // Recorded cipher suites.
  curves: number[] // Supported curve identifiers.
  point_formats: number[] // Recorded point formats.
  signature_algorithms: number[] // Recorded signature algorithms.
  alpn_protocols: string[] // Recorded alpn protocols.
  supported_versions: number[] // Recorded supported versions.
  key_share_groups: number[] // Key share groups.
  psk_modes: number[] // Recorded psk modes.
  extensions: number[] // TLS extension identifiers.
  created_at: string // Record creation timestamp.
  updated_at: string // Last modification timestamp.
}

/**
 * CreateProfileRequest supplies a TLS profile name and optional handshake
 * parameter selections for creation.
 */
export interface CreateProfileRequest {
  name: string // Human-readable display name.
  description?: string | null // Human-readable record description.
  enable_grease?: boolean // Requested enable grease.
  cipher_suites?: number[] // Requested cipher suites.
  curves?: number[] // Supported curve identifiers.
  point_formats?: number[] // Requested point formats.
  signature_algorithms?: number[] // Requested signature algorithms.
  alpn_protocols?: string[] // Requested alpn protocols.
  supported_versions?: number[] // Requested supported versions.
  key_share_groups?: number[] // Key share groups.
  psk_modes?: number[] // Requested psk modes.
  extensions?: number[] // TLS extension identifiers.
}

/**
 * UpdateProfileRequest supplies selected edits to an existing TLS fingerprint
 * profile's identity or handshake parameters.
 */
export interface UpdateProfileRequest {
  name?: string // Human-readable display name.
  description?: string | null // Human-readable record description.
  enable_grease?: boolean // Requested enable grease.
  cipher_suites?: number[] // Requested cipher suites.
  curves?: number[] // Supported curve identifiers.
  point_formats?: number[] // Requested point formats.
  signature_algorithms?: number[] // Requested signature algorithms.
  alpn_protocols?: string[] // Requested alpn protocols.
  supported_versions?: number[] // Requested supported versions.
  key_share_groups?: number[] // Key share groups.
  psk_modes?: number[] // Requested psk modes.
  extensions?: number[] // TLS extension identifiers.
}

/**
 * TLSFingerprintCollectorStatus reports collector availability, certificate
 * state and session retention limits for administrator controls.
 */
export interface TLSFingerprintCollectorStatus {
  running: boolean // Worker running flag.
  listen_address: string // Recorded listen address.
  public_base_url: string // Public base URL.
  using_generated_cert: boolean // Using generated cert.
  ca_pem?: string // Recorded ca pem.
  session_ttl_seconds: number // Session ttl seconds.
  max_records_per_session: number // Max records per session.
  started_at?: string // Operation start timestamp.
  last_error?: string // Recorded last error.
}

/**
 * TLSFingerprintCollectorSession pairs a temporary capture token and expiry with
 * the URL used to collect a client fingerprint.
 */
export interface TLSFingerprintCollectorSession {
  token: string // Opaque operation token.
  expires_at: string // Recorded expiration timestamp.
  capture_url: string // Recorded capture url.
  ca_pem?: string // Recorded ca pem.
}

/**
 * TLSFingerprintCaptureRecord records observed client headers, protocol metadata
 * and the captured TLS profile with YAML output.
 */
export interface TLSFingerprintCaptureRecord {
  id: string // Record identifier value.
  captured_at: string // Recorded captured at.
  client_kind: string // Recorded client kind.
  request_path: string // Recorded request path.
  method: string // HTTP request method.
  user_agent: string // Recorded user agent.
  ja3_raw: string // Recorded ja3 raw.
  ja3_hash: string // Recorded ja3 hash.
  negotiated_alpn: string // Recorded negotiated alpn.
  http_proto: string // Recorded http proto.
  profile: TLSFingerprintProfile // Captured TLS profile.
  yaml: string // Captured profile YAML.
  headers_summary: Record<string, string> // Recorded headers summary.
  stainless_summary: Record<string, string> // Recorded stainless summary.
}

/**
 * list retrieves administrator tls fingerprint profiles and returns the server
 * response data.
 */
export async function list(): Promise<TLSFingerprintProfile[]> {
  const { data } = await apiClient.get<TLSFingerprintProfile[]>('/admin/tls-fingerprint-profiles')
  return data
}

/**
 * create submits administrator tls fingerprint profiles and returns the server
 * response data.
 */
export async function create(profileData: CreateProfileRequest): Promise<TLSFingerprintProfile> {
  const { data } = await apiClient.post<TLSFingerprintProfile>(
    '/admin/tls-fingerprint-profiles',
    profileData,
  )
  return data
}

/**
 * update updates administrator tls fingerprint profiles and returns the server
 * response data.
 */
export async function update(
  id: number,
  updates: UpdateProfileRequest,
): Promise<TLSFingerprintProfile> {
  const { data } = await apiClient.put<TLSFingerprintProfile>(
    `/admin/tls-fingerprint-profiles/${id}`,
    updates,
  )
  return data
}

/**
 * deleteProfile removes profile and returns the server response data.
 */
export async function deleteProfile(id: number): Promise<{
  message: string // Server-provided result message.
}> {
  const { data } = await apiClient.delete<{
    message: string // Server-provided result message.
  }>(`/admin/tls-fingerprint-profiles/${id}`)
  return data
}

/**
 * collectorStatus retrieves collector status and returns the server response
 * data.
 */
export async function collectorStatus(): Promise<TLSFingerprintCollectorStatus> {
  const { data } = await apiClient.get<TLSFingerprintCollectorStatus>(
    '/admin/tls-fingerprint-profiles/collector/status',
  )
  return data
}

/**
 * startCollector submits start collector and returns the server response data.
 */
export async function startCollector(): Promise<TLSFingerprintCollectorStatus> {
  const { data } = await apiClient.post<TLSFingerprintCollectorStatus>(
    '/admin/tls-fingerprint-profiles/collector/start',
  )
  return data
}

/**
 * stopCollector submits stop collector and returns the server response data.
 */
export async function stopCollector(): Promise<TLSFingerprintCollectorStatus> {
  const { data } = await apiClient.post<TLSFingerprintCollectorStatus>(
    '/admin/tls-fingerprint-profiles/collector/stop',
  )
  return data
}

/**
 * createCollectorSession submits collector session and returns the server
 * response data.
 */
export async function createCollectorSession(): Promise<TLSFingerprintCollectorSession> {
  const { data } = await apiClient.post<TLSFingerprintCollectorSession>(
    '/admin/tls-fingerprint-profiles/collector/sessions',
  )
  return data
}

/**
 * listCollectorCaptures retrieves collector captures and returns the server
 * response data.
 */
export async function listCollectorCaptures(token: string): Promise<TLSFingerprintCaptureRecord[]> {
  const { data } = await apiClient.get<TLSFingerprintCaptureRecord[]>(
    `/admin/tls-fingerprint-profiles/collector/sessions/${encodeURIComponent(token)}/captures`,
  )
  return data
}

/**
 * tlsFingerprintProfileAPI groups TLS fingerprint profiles and collector
 * sessions for callers using the module's named or default API object.
 */
export const tlsFingerprintProfileAPI = {
  list,
  create,
  update,
  delete: deleteProfile,
  collectorStatus,
  startCollector,
  stopCollector,
  createCollectorSession,
  listCollectorCaptures,
}

export default tlsFingerprintProfileAPI
