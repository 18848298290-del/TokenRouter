import type { UsageRequestType } from '@/api/usage.types'

/**
 * usageRequestType.ts resolves usage request categories from current and legacy fields.
 * A recognized explicit category, including unknown, takes precedence over WebSocket
 * and stream flags. Legacy stream conversion omits categories whose classification does
 * not supply a definite stream boolean.
 *
 * The fields can coexist on a record, so their ordering determines which description
 * the user sees. An explicit unknown value is retained rather than replaced from
 * legacy flags. Conversion back to the stream filter deliberately returns null for
 * unknown, cyber, and live categories because this helper assigns no boolean to them.
 */

/**
 * UsageRequestTypeLike carries current category and legacy transport fields for
 * display.
 */
export interface UsageRequestTypeLike {
  /**
   * request_type, openai_ws_mode, and stream are checked in that order. A
   * recognized current type wins, including unknown; legacy flags participate only
   * when the type is unrecognized.
   */
  request_type?: string | null // stored request category
  stream?: boolean | null // legacy streaming flag
  openai_ws_mode?: boolean | null // legacy WebSocket flag
}

/**
 * VALID_REQUEST_TYPES enumerates accepted stored categories whose explicit values take
 * precedence over legacy transport flags during request classification.
 */
const VALID_REQUEST_TYPES = new Set<UsageRequestType>([
  'unknown',
  'sync',
  'stream',
  'ws_v2',
  'cyber',
  'live',
])

/**
 * isUsageRequestType recognizes exact string members of the accepted category set.
 */
export const isUsageRequestType = (value: unknown): value is UsageRequestType => {
  return typeof value === 'string' && VALID_REQUEST_TYPES.has(value as UsageRequestType)
}

/**
 * resolveUsageRequestType resolves the current category together with legacy
 * transport flags. A recognized request_type, including unknown, wins even when
 * either legacy flag would imply a different transport category.
 *
 * When the type is missing or unrecognized, a truthy openai_ws_mode selects
 * ws_v2 before the stream flag is consulted. The remaining rows become stream or
 * sync according to stream truthiness. This precedence lets older records supply
 * a category while retaining explicit categories carried by newer records.
 */
export const resolveUsageRequestType = (value: UsageRequestTypeLike): UsageRequestType => {
  if (isUsageRequestType(value.request_type)) {
    return value.request_type
  }
  /**
   * Unrecognized explicit categories consult the legacy transport flags, giving
   * WebSocket mode precedence because its traffic can also carry streaming content.
   */
  if (value.openai_ws_mode) {
    return 'ws_v2'
  }
  return value.stream ? 'stream' : 'sync'
}

/**
 * requestTypeToLegacyStream converts sync to false and stream or ws_v2 to true.
 * Missing, unknown, cyber, and live categories return null because this mapping does
 * not assign them a legacy stream value.
 */
export const requestTypeToLegacyStream = (
  requestType?: UsageRequestType | null,
): boolean | null | undefined => {
  /**
   * Cyber, live, and unknown categories do not select a legacy stream filter; returning
   * null leaves that dimension unspecified for consumers.
   */
  if (
    !requestType ||
    requestType === 'unknown' ||
    requestType === 'cyber' ||
    requestType === 'live'
  ) {
    return null
  }
  if (requestType === 'sync') {
    return false
  }
  return true
}
