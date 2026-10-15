import type { UsageLogTiming } from '@/types'

// requestTiming.ts groups persisted request timing into admission durations and
// ingress-relative event offsets for the usage tooltip, request lookup and
// exports. Durations partition the time before the first provider admission and
// may be summed; offsets all start at gateway ingress and must never be added.
// Keeping the two groups apart lets each surface label them without guessing.
//
// Historical records carry only offsets, so the duration group stays empty
// instead of showing a column of missing values. Stream-only stages are dropped
// for synchronous requests because they can never occur there. A measured zero
// remains a value, while an absent observation becomes null for the caller to
// render as a placeholder.

// Admission fields are independent durations and never enter the event timeline.
export const admissionTimingKeys = [
  'request_body_read_ms', 'preprocess_ms', 'user_slot_wait_ms', 'provider_slot_wait_ms',
] as const

// Event fields keep their ingress-relative meaning in historical and new records.
export const eventTimingKeys = [
  'provider_slot_acquired_ms', 'upstream_get_conn_ms', 'upstream_got_conn_ms',
  'upstream_wrote_request_ms', 'upstream_first_response_byte_ms', 'upstream_first_event_ms',
  'first_visible_output_ms', 'first_downstream_flush_ms',
] as const

// Stream-only events exist only when the client asked for a streaming response.
const streamOnlyTimingKeys: ReadonlySet<TimingKey> = new Set([
  'upstream_first_event_ms', 'first_visible_output_ms', 'first_downstream_flush_ms',
])

// TimingKey names one recognized displayable duration or event offset.
export type TimingKey = typeof admissionTimingKeys[number] | typeof eventTimingKeys[number] | 'retry_provider_slot_wait_ms'

// TimingRow pairs a metric with its observed milliseconds, or null when unrecorded.
export interface TimingRow {
  key: TimingKey // Recognized metric name.
  value: number | null // Observed milliseconds.
}

// TimingBreakdown holds the displayable rows of one request timing record.
export interface TimingBreakdown {
  // Durations stay empty for historical records that lack the admission split.
  // When present, the four rows add up to admissionTotal.
  durations: TimingRow[]
  // AdmissionTotal sums the durations; after a successful admission it equals
  // the provider_slot_acquired_ms offset. Null when any duration is missing.
  admissionTotal: number | null
  // RetryWait is provider queueing after the first admission, so it stays out
  // of the durations sum. Null unless a later attempt actually queued.
  retryWait: TimingRow | null
  events: TimingRow[] // Ingress-relative offsets in pipeline order.
  // Admitted is null when the record predates the admission flag; false means
  // the durations run until request completion instead of provider admission.
  admitted: boolean | null
}

// timingValue reads one observation without deriving it from other fields.
// The protocol-independent first event wins, including a measured zero; only
// an absent value falls back to the SSE offset kept by historical records.
export function timingValue(timing: UsageLogTiming, key: TimingKey): number | null {
  if (key === 'upstream_first_event_ms') return timing.upstream_first_event_ms ?? timing.upstream_first_sse_data_ms ?? null
  return timing[key] ?? null
}

// timingBreakdown selects the rows a timing view should show for one record.
// The record's own stream flag takes precedence over the optional legacy
// stream argument, and an unknown mode keeps every stream-only stage visible.
//
// Durations appear only when at least one admission field was recorded, so a
// historical record renders exactly its offsets. The collector splits the time
// until the first admission into these four parts, so their sum is the slot
// offset; retry waits fall after that point and are reported separately.
// Rows keep null for a missing value, letting callers draw a placeholder.
export function timingBreakdown(timing: UsageLogTiming, stream?: boolean): TimingBreakdown {
  const streaming = timing.timing_stream == null ? stream : timing.timing_stream === 1
  const row = (key: TimingKey): TimingRow => ({ key, value: timingValue(timing, key) })
  const durations = admissionTimingKeys.some((key) => timing[key] != null) ? admissionTimingKeys.map(row) : []
  const complete = durations.length > 0 && durations.every((item) => item.value != null)
  const events = eventTimingKeys.filter((key) => streaming !== false || !streamOnlyTimingKeys.has(key)).map(row)
  return {
    durations,
    admissionTotal: complete ? durations.reduce((sum, item) => sum + (item.value ?? 0), 0) : null,
    // Retry waits are rare; a zero would add a row to every request without telling anything.
    retryWait: (timing.retry_provider_slot_wait_ms ?? 0) > 0 ? row('retry_provider_slot_wait_ms') : null,
    events,
    admitted: timing.timing_admitted == null ? null : timing.timing_admitted === 1,
  }
}

// formatTimingMs shows ms below one second, seconds below a minute, then minutes.
// Null renders as '-', so a missing observation stays distinct from a measured 0ms.
export function formatTimingMs(ms: number | null | undefined): string {
  if (ms == null) return '-'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)}s`
  // Minute values round to whole seconds; sub-second precision is noise at this scale.
  const totalSec = Math.round(ms / 1000)
  return `${Math.floor(totalSec / 60)}m ${totalSec % 60}s`
}

// formatRequestTimingForExport writes every recorded field as raw named values.
// Field names stay unchanged so spreadsheets can tell durations from offsets,
// and absent observations are omitted; a missing timing object yields ''.
export function formatRequestTimingForExport(timing?: UsageLogTiming | null): string {
  if (!timing) return ''
  const fields = Object.entries(timing).filter(([, value]) => value != null)
  return fields.map(([key, value]) => `${key}=${value}${key.endsWith('_ms') ? 'ms' : ''}`).join('; ')
}
