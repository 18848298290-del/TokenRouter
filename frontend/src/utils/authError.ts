/**
 * authError.ts selects an authentication error message from nested response
 * data or a flat rejection. Response detail takes precedence over the general
 * response message, and the caller supplies a localized fallback when neither
 * response nor flat message is truthy.
 */

/**
 * APIErrorLike represents authentication rejection messages in flat or nested
 * response data.
 */
interface APIErrorLike {
  message?: string // displayable error message
  response?: {
    data?: {
      detail?: string // detailed response message
      message?: string // displayable error message
    } // response payload fields
  } // nested response wrapper
}

/**
 * extractErrorMessage reads response detail, response message, then the flat
 * message. Falsy fields fall through to an empty string, allowing
 * authentication callers to apply their own fallback text.
 */
function extractErrorMessage(error: unknown): string {
  const err = (error || {}) as APIErrorLike
  return err.response?.data?.detail || err.response?.data?.message || err.message || ''
}

/**
 * buildAuthErrorMessage returns the extracted authentication error or the
 * provided fallback. The fallback is used when all recognized message fields
 * are falsy, so callers supply the desired localized default.
 */
export function buildAuthErrorMessage(
  error: unknown,
  options: {
    fallback: string // caller fallback message
  },
): string {
  const { fallback } = options
  const message = extractErrorMessage(error)
  return message || fallback
}
