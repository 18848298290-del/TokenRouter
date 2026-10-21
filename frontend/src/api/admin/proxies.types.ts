import type { ProviderPlatform, ProviderType } from '@/api/admin/providers.types'

// proxies.types.ts defines administrator proxy records, health probes, and connection
// changes. Proxy records include expiration and fallback configuration, while quality
// checks retain each target's result so administrators can inspect connectivity failures
// or challenges behind the aggregate assessment.

// ProxyProtocol selects the transport scheme used for an upstream proxy connection.
export type ProxyProtocol = 'http' | 'https' | 'socks5' | 'socks5h'

// Proxy describes proxy connectivity, expiration handling, and the latest health
// observations.
export interface Proxy {
  id: number // Record numeric identifier.
  name: string // Record display name.
  protocol: ProxyProtocol // Proxy transport protocol.
  host: string // Proxy server hostname.
  port: number // Proxy listening port.
  username: string | null // Proxy authentication username.
  password?: string | null // Proxy authentication password.
  status: 'active' | 'inactive' | 'expired' // Current lifecycle state.
  provider_count?: number // Number of providers using this proxy
  latency_ms?: number // Measured latency milliseconds.
  latency_status?: 'success' | 'failed' // Latency probe outcome.
  latency_message?: string // Latency probe explanation.
  ip_address?: string // Observed network address.
  country?: string // Proxy exit country.
  country_code?: string // Country region code.
  region?: string // Geographic region name.
  city?: string // Geographic city name.
  quality_status?: 'healthy' | 'warn' | 'challenge' | 'failed' // Overall quality assessment.
  quality_score?: number // Aggregate quality score.
  quality_grade?: string // Quality assessment grade.
  quality_summary?: string // Quality assessment explanation.
  quality_checked?: number // Quality check Unix seconds.
  expires_at: string | null // Record expiration timestamp.
  fallback_mode: 'none' | 'proxy' | 'direct' // Proxy expiration fallback policy.
  backup_proxy_id?: number | null // Backup proxy identifier.
  expiry_warn_days: number // Expiration warning lead days.
  created_at: string // Record creation timestamp.
  updated_at: string // Record update timestamp.
}

// ProxyProviderSummary identifies an upstream provider currently attached to a proxy.
export interface ProxyProviderSummary {
  id: number // Record numeric identifier.
  name: string // Record display name.
  platform: ProviderPlatform // Upstream service platform.
  type: ProviderType // Record kind discriminator.
  notes?: string | null // Administrator record notes.
}

// ProxyQualityCheckItem records connectivity and challenge results for one probe target.
export interface ProxyQualityCheckItem {
  target: string // Probe target identifier.
  status: 'pass' | 'warn' | 'fail' | 'challenge' // Target probe outcome.
  http_status?: number // Observed HTTP response status.
  latency_ms?: number // Measured latency milliseconds.
  message?: string // Human-readable result message.
  cf_ray?: string // Cloudflare request trace identifier.
}

// ProxyQualityCheckResult aggregates target probes into the proxy's overall quality
// assessment.
export interface ProxyQualityCheckResult {
  proxy_id: number // Assigned proxy identifier.
  score: number // Aggregate quality score.
  grade: string // Quality assessment grade.
  summary: string // Aggregate result explanation.
  exit_ip?: string // Proxy exit address.
  country?: string // Proxy exit country.
  country_code?: string // Country region code.
  base_latency_ms?: number // Baseline latency milliseconds.
  passed_count: number // Successful target count.
  warn_count: number // Warning target count.
  failed_count: number // Failed target count.
  challenge_count: number // Challenged target count.
  checked_at: number // Quality check time in Unix seconds.
  items: ProxyQualityCheckItem[] // Individual target probe outcomes.
}

// CreateProxyRequest supplies proxy connectivity and expiration handling for a new proxy.
export interface CreateProxyRequest {
  name: string // Record display name.
  protocol: ProxyProtocol // Proxy transport protocol.
  host: string // Proxy server hostname.
  port: number // Proxy listening port.
  username?: string | null // Proxy authentication username.
  password?: string | null // Proxy authentication password.
  expires_at?: number | null // Unix seconds; null or zero means no expiration.
  fallback_mode?: 'none' | 'proxy' | 'direct' // Proxy expiration fallback policy.
  backup_proxy_id?: number | null // Backup proxy identifier.
  expiry_warn_days?: number // Expiration warning lead days.
}

// UpdateProxyRequest combines nonempty connection patches with complete expiration-policy
// replacement. Callers must include policy values they intend to retain, even when
// changing only connection details; omitted policy fields do not preserve stored values.
export interface UpdateProxyRequest {
  // name, protocol, host, username, password, and status retain their stored values
  // when omitted or empty; strings are trimmed, and zero port preserves its value.
  // Null credential values likewise leave the stored authentication strings unchanged.
  name?: string // Record display name.
  protocol?: ProxyProtocol // Proxy transport protocol.
  host?: string // Proxy server hostname.
  port?: number // Proxy listening port.
  username?: string | null // Proxy authentication username.
  password?: string | null // Proxy authentication password.
  status?: 'active' | 'inactive' // Current lifecycle state.
  // expires_at, fallback_mode, backup_proxy_id, and expiry_warn_days are always replaced
  // together. Omitted, null, or nonpositive
  // expiration clears it; omitted backup ID clears the reference, omitted mode
  // becomes none, and omitted warning days becomes zero. Proxy fallback requires
  // a nonnull backup ID, and a proxy cannot name itself as its backup.
  expires_at?: number | null // Expiration timestamp in Unix seconds.
  fallback_mode?: 'none' | 'proxy' | 'direct' // Replacement expiration fallback policy.
  backup_proxy_id?: number | null // Replacement backup proxy identifier.
  expiry_warn_days?: number // Nonnegative replacement warning lead days.
}
