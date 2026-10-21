import { CREATIVE_RUN_TERMINAL_STATUSES, type CreativeRun } from '@/api/creative'

/**
 * creativeRunTime.ts formats elapsed generation time for the composer and
 * history panel. Backend timestamps can use seconds or milliseconds, so both
 * views normalize their endpoints through the same threshold before displaying
 * elapsed time. Active tasks use the supplied current clock, while terminal
 * tasks require an observed completion or cancellation time.
 *
 * The end time and status interact: a terminal run with no endpoint displays an
 * empty string instead of continuing to count. Negative intervals are clamped
 * to zero, fractional seconds are discarded, and completed hours add a third
 * display field. These choices keep both views consistent as polling changes a
 * task from active to terminal.
 */

/**
 * creativeTimestampToMs normalizes a finite server timestamp to browser
 * milliseconds. Missing and nonfinite values return null; timestamps below one
 * trillion are interpreted as seconds and multiplied by one thousand.
 */
export function creativeTimestampToMs(timestamp: number | null | undefined): number | null {
  if (timestamp == null || !Number.isFinite(timestamp)) return null
  /**
   * The threshold distinguishes contemporary Unix seconds from millisecond
   * timestamps.
   */
  return timestamp < 1e12 ? timestamp * 1000 : timestamp
}

/**
 * formatCreativeRunElapsed formats the interval between a run start and its
 * applicable endpoint. The start uses started_at when present and otherwise
 * created_at; terminal runs use completed_at or cancelled_at, while active runs
 * use now in milliseconds. Missing endpoints return an empty string. The
 * interval is clamped before formatting so reversed timestamps display zero
 * elapsed time, and whole hours extend the usual minutes-and-seconds
 * representation.
 */
export function formatCreativeRunElapsed(run: CreativeRun, now: number): string {
  const startedAt = creativeTimestampToMs(run.started_at ?? run.created_at)
  if (startedAt == null) return ''
  const endedAt = CREATIVE_RUN_TERMINAL_STATUSES.includes(run.status)
    ? creativeTimestampToMs(run.completed_at ?? run.cancelled_at)
    : now
  if (endedAt == null) return ''
  /**
   * Discard partial seconds and clamp reversed clock observations to zero before
   * deriving all displayed elapsed-time components.
   */
  const totalSeconds = Math.max(0, Math.floor((endedAt - startedAt) / 1000))
  const seconds = totalSeconds % 60
  const minutes = Math.floor(totalSeconds / 60) % 60
  const hours = Math.floor(totalSeconds / 3600)
  /**
   * pad formats one elapsed-time component with at least two decimal digits.
   */
  const pad = (value: number): string => String(value).padStart(2, '0')
  return hours > 0
    ? `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`
    : `${pad(minutes)}:${pad(seconds)}`
}
