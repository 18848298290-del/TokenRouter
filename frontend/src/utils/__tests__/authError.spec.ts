import { describe, expect, it } from 'vitest'
import { buildAuthErrorMessage } from '@/utils/authError'

/**
 * authError.spec.ts checks authentication error precedence across nested
 * responses and flat errors. Each case provides a caller fallback and verifies
 * which available message wins, including the empty rejection case where no
 * server text can be extracted.
 */

describe('buildAuthErrorMessage', () => {
  /**
   * A rejection contains both detailed and general response messages. The
   * detailed text must win so authentication failures retain the most specific
   * supplied explanation.
   */
  it('prefers response detail message when available', () => {
    const message = buildAuthErrorMessage(
      {
        response: {
          data: {
            detail: 'detailed message',
            message: 'plain message',
          },
        },
      },
      { fallback: 'fallback' },
    )
    expect(message).toBe('detailed message')
  })

  /**
   * A response supplies only its general message without detail. The helper must
   * return that text before considering the caller fallback for the
   * authentication form.
   */
  it('falls back to response message when detail is unavailable', () => {
    const message = buildAuthErrorMessage(
      {
        response: {
          data: {
            message: 'plain message',
          },
        },
      },
      { fallback: 'fallback' },
    )
    expect(message).toBe('plain message')
  })

  /**
   * A rejection has a flat message without nested response data. The helper must
   * preserve that message so ordinary error objects remain useful to
   * authentication callers.
   */
  it('falls back to error.message when response payload is unavailable', () => {
    const message = buildAuthErrorMessage(
      {
        message: 'error message',
      },
      { fallback: 'fallback' },
    )
    expect(message).toBe('error message')
  })

  /**
   * An empty rejection supplies no recognized message fields at all. The caller
   * fallback must be returned rather than exposing an empty authentication
   * error.
   */
  it('uses fallback when no message can be extracted', () => {
    expect(buildAuthErrorMessage({}, { fallback: 'fallback' })).toBe('fallback')
  })
})
