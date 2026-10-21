import type {
  UserPromptReplacementConfig,
  UserPromptReplacementRule,
  UserPromptReplacementType,
} from '@/api/admin/settings'
import type { LoginAgreementDocument } from '@/api/settings.types'

/**
 * settingsDefaults.ts supplies initial values and compatibility normalization for
 * administrator settings drafts. Agreement and prompt factories create fresh
 * editable arrays for each page instance, while numeric limits match the controls
 * and validation applied before a bulk request is sent. Localized user-facing
 * strings inside the default documents remain content values.
 *
 * Prompt normalization distinguishes absent configuration from an existing empty
 * rule list. Missing configuration receives the built-in timezone and date rules;
 * an explicitly empty list stays empty. Individual rule fields are converted into
 * the text, boolean, and nonnegative capture-group values consumed by controls,
 * with unsupported replacement kinds normalized to static text.
 */

/**
 * Table pagination limits constrain selectable page sizes and define the default draft size before settings are loaded.
 */
export const tablePageSizeMin = 5

export const tablePageSizeMax = 1000

export const tablePageSizeDefault = 20

/**
 * Ranking limits constrain the number of visible entries and supply the initial count used by the settings draft.
 */
export const usageRankingLimitMin = 1

export const usageRankingLimitMax = 100

export const usageRankingLimitDefault = 20

/**
 * Availability window limits use days and keep the marketplace observation range within the bounds accepted by settings controls.
 */
export const marketplaceAvailabilityWindowDaysMin = 1

export const marketplaceAvailabilityWindowDaysMax = 90

export const marketplaceAvailabilityWindowDaysDefault = 7

/**
 * Availability bucket limits use minutes and determine the initial aggregation interval shown by marketplace settings controls.
 */
export const marketplaceAvailabilityBucketMinutesMin = 5

export const marketplaceAvailabilityBucketMinutesMax = 1440

export const marketplaceAvailabilityBucketMinutesDefault = 120

/**
 * defaultLoginAgreementDocuments creates the four editable default legal documents.
 * Each call returns fresh records with stable route identifiers and empty
 * Markdown bodies, which the agreement editor can populate before enabling confirmation.
 */
export function defaultLoginAgreementDocuments(): LoginAgreementDocument[] {
  return [
    {
      id: 'terms',
      title: '服务条款',
      content_md: '',
    },
    {
      id: 'usage-policy',
      title: '使用政策',
      content_md: '',
    },
    {
      id: 'supported-regions',
      title: '支持的国家和地区',
      content_md: '',
    },
    {
      id: 'service-specific-terms',
      title: '服务特定条款',
      content_md: '',
    },
  ]
}

/**
 * defaultUserPromptReplacementConfig creates enabled timezone and current-date rules
 * for environment_context content. Each call returns fresh editable records; both
 * rules target capture group two and use the Asia/Tokyo timezone expected by
 * the default prompt replacement configuration.
 */
export function defaultUserPromptReplacementConfig(): UserPromptReplacementConfig {
  return {
    enabled: true,
    rules: [
      {
        id: 'environment-context-timezone-japan',
        name: 'environment_context timezone -> Asia/Tokyo',
        enabled: true,
        pattern:
          '(?s)(<environment_context\\b[^>]*>.*?<timezone>)([^<]*)(</timezone>.*?</environment_context>)',
        target_group: 2,
        replacement_type: 'timezone_name',
        scope: 'environment_context',
        timezone: 'Asia/Tokyo',
      },
      {
        id: 'environment-context-current-date-japan',
        name: 'environment_context current_date -> Asia/Tokyo today',
        enabled: true,
        pattern:
          '(?s)(<environment_context\\b[^>]*>.*?<current_date>)([^<]*)(</current_date>.*?</environment_context>)',
        target_group: 2,
        replacement_type: 'current_time',
        scope: 'environment_context',
        timezone: 'Asia/Tokyo',
        time_format: '2006-01-02',
      },
    ],
  }
}

/**
 * normalizeUserPromptReplacementConfig converts a stored prompt configuration into
 * editable control values. An absent configuration receives the built-in rules,
 * while an existing configuration whose rules are empty keeps that empty list.
 * Each returned rule owns a fresh record, retains explicit disabled state, and
 * converts optional text and capture-group values into the forms used by the
 * editor. Unsupported replacement kinds become static text, and missing timezone
 * and time-format values receive the editor defaults.
 */
export function normalizeUserPromptReplacementConfig(
  raw: UserPromptReplacementConfig | null | undefined,
): UserPromptReplacementConfig {
  // Missing configuration receives the built-in rules; an explicitly supplied
  // empty rules array remains editable as empty and is not replaced with defaults.
  const source = raw ?? defaultUserPromptReplacementConfig()
  return {
    enabled: source.enabled !== false,
    rules: Array.isArray(source.rules)
      ? source.rules.map((rule, index) => ({
          id: String(rule.id || `rule-${index + 1}`).trim(),
          name: String(rule.name || rule.id || `Rule ${index + 1}`).trim(),
          enabled: rule.enabled !== false,
          pattern: String(rule.pattern || '').trim(),
          // Capture indexes require nonnegative integers; truncate fractions and
          // use group zero for negative or nonfinite input before building editable rules.
          target_group: Number.isFinite(Number(rule.target_group))
            ? Math.max(0, Math.floor(Number(rule.target_group)))
            : 0,
          replacement_type: normalizeUserPromptReplacementType(rule.replacement_type),
          scope: String(rule.scope || '').trim(),
          static_text: String(rule.static_text ?? ''),
          timezone: String(rule.timezone || 'Asia/Tokyo').trim(),
          time_format: String(rule.time_format || '2006-01-02').trim(),
        }))
      : [],
  }
}

/**
 * normalizeUserPromptReplacementType selects one of the three supported replacement
 * kinds. Unknown input becomes static text so the editor can bind a supported
 * selection and submit a value accepted by the settings API.
 */
export function normalizeUserPromptReplacementType(value: unknown): UserPromptReplacementType {
  return value === 'static' || value === 'timezone_name' || value === 'current_time'
    ? value
    : 'static'
}

/**
 * createUserPromptReplacementRule creates an enabled static-text rule for the editor.
 * It uses the current millisecond timestamp in the identifier, starts with empty
 * pattern and replacement text, and includes the default timezone and time format.
 */
export function createUserPromptReplacementRule(): UserPromptReplacementRule {
  return {
    id: `rule-${Date.now()}`,
    name: '',
    enabled: true,
    pattern: '',
    target_group: 0,
    replacement_type: 'static',
    scope: '',
    static_text: '',
    timezone: 'Asia/Tokyo',
    time_format: '2006-01-02',
  }
}

/**
 * The browser origin supplies redirect suggestions and default recharge links; server-side module evaluation uses an empty string.
 */
export const currentOrigin = typeof window !== 'undefined' ? window.location.origin : ''
