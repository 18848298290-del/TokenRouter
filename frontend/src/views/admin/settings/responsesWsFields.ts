import type { ResponsesWSParameters } from '@/api/admin/settings'

/**
 * responsesWsFields.ts describes the editable Responses WebSocket parameters and their
 * display groups. Numeric metadata supplies input bounds, translation keys, and conversion
 * scales to the shared number field; the complete key list also supports clearing
 * overrides when an administrator edits after restoring defaults.
 */

/** ResponsesWSNumberKey selects numeric parameter names accepted by the number editor. */
export type ResponsesWSNumberKey = {
  [K in keyof ResponsesWSParameters]: ResponsesWSParameters[K] extends number ? K : never
}[keyof ResponsesWSParameters]
/** ResponsesWSBooleanKey selects toggle parameter names from the remaining API fields. */
export type ResponsesWSBooleanKey = Exclude<keyof ResponsesWSParameters, ResponsesWSNumberKey>

/** ResponsesWSNumberField defines display units and bounds for one numeric parameter. */
export interface ResponsesWSNumberField {
  key: ResponsesWSNumberKey // API numeric parameter name.
  // ResponsesWSSettings multiplies display input by scale and divides stored values by it;
  // both paths use the same metadata so byte fields can be edited in MiB.
  scale: number // API units per displayed unit.
  min: number // Minimum accepted display value.
  max?: number // Optional maximum display value.
  step: string // HTML numeric stepping constraint.
  unit?: 'seconds' | 'ms' | 'mib' // Localized display unit suffix.
  hint?: boolean // Enables translated explanatory text.
}

/** ResponsesWSGroup arranges parameters into a section with optional toggle-dependent fields. */
export interface ResponsesWSGroup {
  key: string // Section translation key suffix.
  advanced: boolean // Places section inside advanced disclosure.
  hint?: boolean // Enables translated explanatory text.
  toggle?: ResponsesWSBooleanKey // Optional section toggle parameter.
  // When dependent and toggle are present, the numeric controls remain mounted inside
  // a collapsed subpanel until the toggle becomes enabled in the current draft.
  dependent?: boolean // Collapses numeric fields when toggle disables.
  fields: ResponsesWSNumberField[] // Ordered section number editors.
}

// Group order determines section order; byte limits use 1048576 bytes per MiB,
// and the positive fractional minimum keeps factor and utilization controls above zero.
export const responsesWsGroups: ResponsesWSGroup[] = [
  {
    key: 'pool',
    advanced: false,
    hint: true,
    fields: [
      { key: 'max_conns_per_provider', scale: 1, min: 1, step: '1', hint: true },
      { key: 'queue_limit_per_conn', scale: 1, min: 1, step: '1', hint: true },
      { key: 'min_idle_per_provider', scale: 1, min: 0, step: '1', hint: true },
      { key: 'max_idle_per_provider', scale: 1, min: 0, step: '1', hint: true },
    ],
  },
  {
    key: 'client',
    advanced: false,
    hint: true,
    fields: [
      { key: 'max_ingress_connections_per_api_key', scale: 1, min: 0, step: '1', hint: true },
      { key: 'client_first_message_timeout_seconds', scale: 1, min: 1, step: '1', unit: 'seconds' },
      {
        key: 'ingress_inter_turn_idle_timeout_seconds',
        scale: 1,
        min: 0,
        step: '1',
        unit: 'seconds',
        hint: true,
      },
    ],
  },
  {
    key: 'messageSize',
    advanced: false,
    fields: [
      { key: 'client_read_limit_bytes', scale: 1048576, min: 1, step: 'any', unit: 'mib' },
      {
        key: 'http_bridge_threshold_bytes',
        scale: 1048576,
        min: 1,
        step: 'any',
        unit: 'mib',
        hint: true,
      },
    ],
  },
  {
    key: 'timeout',
    advanced: false,
    hint: true,
    fields: [
      { key: 'dial_timeout_seconds', scale: 1, min: 1, step: '1', unit: 'seconds' },
      { key: 'read_timeout_seconds', scale: 1, min: 1, step: '1', unit: 'seconds' },
      { key: 'write_timeout_seconds', scale: 1, min: 1, step: '1', unit: 'seconds' },
    ],
  },
  {
    key: 'dynamic',
    advanced: true,
    toggle: 'dynamic_max_conns_by_provider_concurrency_enabled',
    dependent: true,
    fields: [
      { key: 'oauth_max_conns_factor', scale: 1, min: 1e-6, step: 'any' },
      { key: 'apikey_max_conns_factor', scale: 1, min: 1e-6, step: 'any' },
    ],
  },
  {
    key: 'prewarm',
    advanced: true,
    fields: [
      { key: 'pool_target_utilization', scale: 1, min: 1e-6, max: 1, step: 'any', hint: true },
      { key: 'prewarm_cooldown_ms', scale: 1, min: 0, step: '1', unit: 'ms' },
    ],
  },
  {
    key: 'session',
    advanced: true,
    toggle: 'ingress_previous_response_recovery_enabled',
    fields: [
      { key: 'sticky_session_ttl_seconds', scale: 1, min: 1, step: '1', unit: 'seconds' },
      { key: 'sticky_response_id_ttl_seconds', scale: 1, min: 1, step: '1', unit: 'seconds' },
    ],
  },
]

// responsesWsKeys covers toggles and numeric fields so an edit after restoring defaults
// can send null for every other override managed by this editor.
export const responsesWsKeys: (keyof ResponsesWSParameters)[] = responsesWsGroups.flatMap(
  (group) => [...(group.toggle ? [group.toggle] : []), ...group.fields.map((field) => field.key)],
)

/**
 * responsesWsGridClass chooses responsive columns from a section's field count. Four or
 * more fields use four desktop columns, three fields use three, and smaller groups use the
 * two-column layout.
 */
export function responsesWsGridClass(count: number) {
  if (count >= 4) return 'grid gap-4 sm:grid-cols-2 lg:grid-cols-4'
  if (count === 3) return 'grid gap-4 sm:grid-cols-3'
  return 'grid gap-4 sm:grid-cols-2'
}
