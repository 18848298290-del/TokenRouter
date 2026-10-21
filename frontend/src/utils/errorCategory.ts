/**
 * errorCategory.ts assigns user-facing error categories from recorded failure phases
 * and types. Phase comparison is case insensitive, request failures use a narrower type
 * mapping, and unrecognized combinations receive the other category for translation by
 * consumers.
 */

/**
 * mapErrorCategory returns the display category for a failure phase and type. Both
 * strings are lowercased without trimming; only request-phase failures inspect the
 * type, and unmatched combinations return other.
 */
export function mapErrorCategory(phase?: string | null, errType?: string | null): string {
  switch ((phase || '').toLowerCase()) {
    case 'auth':
      return 'auth'
    case 'routing':
      return 'service_unavailable'
    case 'provider_auth':
    case 'upstream':
    case 'network':
      return 'upstream'
    case 'internal':
      return 'internal'
    case 'request':
      switch ((errType || '').toLowerCase()) {
        case 'rate_limit_error':
          return 'rate_limit'
        case 'billing_error':
        case 'subscription_error':
          return 'quota'
        case 'invalid_request_error':
          return 'invalid_request'
        case 'cyber_policy':
          return 'cyber'
      }
  }
  return 'other'
}
