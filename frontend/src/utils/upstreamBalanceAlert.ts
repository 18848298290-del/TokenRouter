import type { UpstreamBalanceAlertConfig } from '@/api/admin/providers.types'

/**
 * upstreamBalanceAlert.ts reads, validates, and compares provider balance
 * reminder settings for editing forms. Missing or invalid numeric fields use
 * the backend defaults, and unsupported provider modes are saved with
 * reminders disabled. Recipient strings remain editable and are trimmed when
 * constructing the submission.
 */

/**
 * These minute bounds match backend reminder validation and apply to numeric
 * fields even when reminders are disabled.
 */
export const BALANCE_ALERT_MIN_INTERVAL = 5
export const BALANCE_ALERT_MAX_INTERVAL = 1440

/**
 * defaultUpstreamBalanceAlert returns a fresh disabled policy using the
 * backend defaults.
 */
export function defaultUpstreamBalanceAlert(): UpstreamBalanceAlertConfig {
  return { enabled: false, threshold: 1000, interval_minutes: 60, recipient_email: '' }
}

/**
 * readUpstreamBalanceAlert copies recognized saved fields over a fresh default
 * policy. Invalid thresholds and intervals fall back independently; recipient
 * strings are retained without email validation so the form can display saved
 * input.
 */
export function readUpstreamBalanceAlert(
  extra: Record<string, unknown> | null | undefined,
): UpstreamBalanceAlertConfig {
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

/**
 * supportsUpstreamBalanceAlert checks whether the provider category offers
 * wallet-balance reminders. API keys qualify except Zhipu and Kimi or DeepSeek
 * coding modes; this predicate does not inspect enabled state or recipient
 * validity.
 */
export function supportsUpstreamBalanceAlert(
  type: string,
  platform: string,
  providerMode: string,
): boolean {
  if (type !== 'apikey' || platform === 'zhipu') return false
  const isCNPlatform = platform === 'kimi' || platform === 'deepseek'
  return !isCNPlatform || providerMode !== 'coding'
}

/**
 * buildUpstreamBalanceAlert prepares a fresh submission policy and trims the
 * recipient. Unsupported providers are disabled, while invalid numeric fields
 * use defaults even when reminders are off because disabled policies still
 * require valid numbers.
 */
export function buildUpstreamBalanceAlert(
  config: UpstreamBalanceAlertConfig,
  supported: boolean,
): UpstreamBalanceAlertConfig {
  /**
   * Disabled policies still require valid numeric fields, so cleared or
   * malformed form numbers use defaults before submission rather than being
   * passed through unchanged.
   */
  const defaults = defaultUpstreamBalanceAlert()
  return {
    enabled: config.enabled && supported,
    threshold: isValidThreshold(config.threshold) ? config.threshold : defaults.threshold,
    interval_minutes: isValidInterval(config.interval_minutes)
      ? config.interval_minutes
      : defaults.interval_minutes,
    recipient_email: config.recipient_email.trim(),
  }
}

/**
 * sameUpstreamBalanceAlert compares all reminder fields without normalizing
 * either policy.
 */
export function sameUpstreamBalanceAlert(
  a: UpstreamBalanceAlertConfig,
  b: UpstreamBalanceAlertConfig,
): boolean {
  return (
    a.enabled === b.enabled &&
    a.threshold === b.threshold &&
    a.interval_minutes === b.interval_minutes &&
    a.recipient_email === b.recipient_email
  )
}

/**
 * isValidThreshold reports whether a value is a finite nonnegative number.
 */
export function isValidThreshold(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

/**
 * isValidInterval checks whole-minute values against the backend accepted
 * interval range.
 */
export function isValidInterval(value: unknown): value is number {
  return (
    typeof value === 'number' &&
    Number.isInteger(value) &&
    value >= BALANCE_ALERT_MIN_INTERVAL &&
    value <= BALANCE_ALERT_MAX_INTERVAL
  )
}
