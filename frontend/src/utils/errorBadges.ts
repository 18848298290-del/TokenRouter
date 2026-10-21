import type { UsageRequestType } from '@/api/usage.types'

/**
 * errorBadges.ts maps request failures to badge classes, translation keys, and
 * table sort keys. Category helpers use the shared usage request kinds, while
 * numeric request types support older rows carrying a stream flag. Unknown
 * category values receive the generic display styling or label.
 *
 * Numeric classification distinguishes an absent type from an unsupported
 * explicit type. The stream flag fills in only the absent case, preventing it
 * from masking a stored value that this mapper cannot identify. Status colors
 * separately prioritize server errors and rate limiting, and fixed filter
 * choices remain available regardless of which statuses occur on the visible
 * page.
 */

/**
 * UsageRequestKind names the request categories shared with usage API rows.
 */
export type UsageRequestKind = UsageRequestType

/**
 * statusCodeBadgeClass selects severity colors from an HTTP status number. Server
 * errors are red, 429 is purple, other client errors are amber, and all remaining
 * values are gray.
 */
export function statusCodeBadgeClass(code: number): string {
  if (code >= 500) return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
  if (code === 429) return 'bg-purple-100 text-purple-800 dark:bg-purple-900 dark:text-purple-200'
  if (code >= 400) return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
  return 'bg-gray-100 text-gray-800 dark:bg-dark-700 dark:text-gray-200'
}

/**
 * requestTypeBadgeClass returns the shared request category badge colors. Cyber, live,
 * WebSocket, stream, and sync each have dedicated classes; remaining category values
 * use the amber unknown badge.
 */
export function requestTypeBadgeClass(kind: UsageRequestKind): string {
  if (kind === 'cyber') return 'bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200'
  if (kind === 'live')
    return 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900 dark:text-emerald-200'
  if (kind === 'ws_v2')
    return 'bg-violet-100 text-violet-800 dark:bg-violet-900 dark:text-violet-200'
  if (kind === 'stream') return 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
  if (kind === 'sync') return 'bg-gray-100 text-gray-800 dark:bg-dark-700 dark:text-gray-200'
  return 'bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200'
}

/**
 * requestTypeLabelKey returns the translation key for a request category. The caller
 * translates that key in its locale, and unrecognized values receive usage.unknown for
 * display.
 */
export function requestTypeLabelKey(kind: UsageRequestKind): string {
  if (kind === 'cyber') return 'usage.cyber'
  if (kind === 'live') return 'usage.live'
  if (kind === 'ws_v2') return 'usage.ws'
  if (kind === 'stream') return 'usage.stream'
  if (kind === 'sync') return 'usage.sync'
  return 'usage.unknown'
}

/**
 * numericRequestTypeKind converts stored numeric request types into display
 * categories using the optional legacy stream flag when needed. A nullish
 * numeric value permits the flag to supply sync or stream, while explicit
 * recognized numbers select their own category.
 *
 * An unsupported explicit number returns null even when the stream flag is
 * present. This preserves the distinction between missing type information and a
 * stored type this mapper does not recognize. When both inputs are absent, the
 * result also remains null for placeholder display.
 */
export function numericRequestTypeKind(
  requestType?: number | null,
  stream?: boolean | null,
): UsageRequestKind | null {
  /**
   * An absent numeric type permits the legacy stream flag to classify the request; an
   * explicit unsupported number stays unknown.
   */
  const rt = requestType ?? (stream == null ? 0 : stream ? 2 : 1)
  if (rt === 3) return 'ws_v2'
  if (rt === 5) return 'live'
  if (rt === 2) return 'stream'
  if (rt === 1) return 'sync'
  return null
}

/**
 * mapErrorSortKey converts the status column key into status_code. Every other key is
 * returned unchanged, allowing the table's remaining column names to pass directly to
 * its sort request.
 */
export function mapErrorSortKey(key: string): string {
  return key === 'status' ? 'status_code' : key
}

/**
 * COMMON_ERROR_STATUS_CODES supplies fixed status filter choices for error tables,
 * including codes absent from the currently displayed page.
 */
export const COMMON_ERROR_STATUS_CODES = [
  400, 401, 403, 404, 408, 413, 429, 499, 500, 502, 503, 504, 529,
]
