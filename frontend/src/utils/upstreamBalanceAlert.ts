import type { UpstreamBalanceAlertConfig } from '@/types'

// upstreamBalanceAlert.ts reads, checks, and compares the balance reminder policy
// that provider forms store in extra.upstream_balance_alert. The backend
// validates every saved object, including disabled ones, so these helpers keep
// form values inside its accepted ranges and turn reminders off for providers
// whose query adapter has no wallet balance.

// Interval bounds match the backend validator; values outside them are rejected
// even when the reminder is disabled.
export const BALANCE_ALERT_MIN_INTERVAL = 5
export const BALANCE_ALERT_MAX_INTERVAL = 1440

/** defaultUpstreamBalanceAlert returns a new disabled policy with the backend defaults. */
export function defaultUpstreamBalanceAlert(): UpstreamBalanceAlertConfig {
  return { enabled: false, threshold: 1000, interval_minutes: 60, recipient_email: '' }
}

/** readUpstreamBalanceAlert copies the saved policy from provider Extra into form values.
 * Missing objects and fields with the wrong type fall back to the defaults, so a
 * malformed saved value cannot leave the form holding something the backend rejects.
 */
export function readUpstreamBalanceAlert(extra: Record<string, unknown> | null | undefined): UpstreamBalanceAlertConfig {
  const config = defaultUpstreamBalanceAlert()
  const raw = extra?.upstream_balance_alert
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return config
  const saved = raw as Record<string, unknown>
  if (typeof saved.enabled === 'boolean') config.enabled = saved.enabled
  if (isValidThreshold(saved.threshold)) config.threshold = saved.threshold
  if (isValidInterval(saved.interval_minutes)) config.interval_minutes = saved.interval_minutes
  if (typeof saved.recipient_email === 'string') config.recipient_email = saved.recipient_email
  return config
}

/** supportsUpstreamBalanceAlert reports whether the provider's usage query returns a wallet balance.
 * Generic API-key adapters all report one. Among CN platforms, Kimi and DeepSeek
 * pay-as-you-go have balance endpoints, while coding plans report quota
 * percentages and Zhipu pay-as-you-go has no balance endpoint.
 */
export function supportsUpstreamBalanceAlert(type: string, platform: string, providerMode: string): boolean {
  if (type !== 'apikey' || platform === 'zhipu') return false
  const isCNPlatform = platform === 'kimi' || platform === 'deepseek'
  return !isCNPlatform || providerMode !== 'coding'
}

/** buildUpstreamBalanceAlert prepares the policy for a create or update request.
 * Unsupported providers are saved with reminders off, since the backend rejects
 * an enabled policy for them; the recipient is trimmed before submission.
 *
 * The number inputs only mount while reminders are on, and native validation
 * blocks submission for invalid values there. A field cleared before switching
 * reminders off still holds an empty value, so invalid numbers fall back to the
 * defaults to keep the disabled policy acceptable to the backend.
 */
export function buildUpstreamBalanceAlert(config: UpstreamBalanceAlertConfig, supported: boolean): UpstreamBalanceAlertConfig {
  const defaults = defaultUpstreamBalanceAlert()
  return {
    enabled: config.enabled && supported,
    threshold: isValidThreshold(config.threshold) ? config.threshold : defaults.threshold,
    interval_minutes: isValidInterval(config.interval_minutes) ? config.interval_minutes : defaults.interval_minutes,
    recipient_email: config.recipient_email.trim()
  }
}

/** sameUpstreamBalanceAlert reports whether two policies would save the same values. */
export function sameUpstreamBalanceAlert(a: UpstreamBalanceAlertConfig, b: UpstreamBalanceAlertConfig): boolean {
  return a.enabled === b.enabled &&
    a.threshold === b.threshold &&
    a.interval_minutes === b.interval_minutes &&
    a.recipient_email === b.recipient_email
}

/** isValidThreshold reports whether a value is a finite, non-negative balance threshold. */
export function isValidThreshold(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

/** isValidInterval reports whether a value is a whole number of minutes within the backend range. */
export function isValidInterval(value: unknown): value is number {
  return typeof value === 'number' &&
    Number.isInteger(value) &&
    value >= BALANCE_ALERT_MIN_INTERVAL &&
    value <= BALANCE_ALERT_MAX_INTERVAL
}
