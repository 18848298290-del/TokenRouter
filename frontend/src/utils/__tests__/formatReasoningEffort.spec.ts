import { describe, expect, it } from 'vitest'

import { formatReasoningEffortMapping, reasoningEffortValuesEqual } from '@/utils/format'

/**
 * formatReasoningEffort.spec.ts tests normalized effort comparison and
 * requested-to-forwarded export labels. The cases cover separator aliases,
 * different requested and effective levels, and a historical row with only the
 * effective value recorded for display.
 */

describe('reasoning effort formatting', () => {
  /**
   * Two reasoning settings differ in case and separator spelling. Their normalized
   * values compare equal, protecting compatibility between x-high and X_HIGH usage
   * fields.
   */
  it('treats compatible separators as the same value', () => {
    expect(reasoningEffortValuesEqual('x-high', 'X_HIGH')).toBe(true)
  })

  /**
   * The requested and forwarded reasoning levels are different nonempty values. The
   * export label retains their direction with the requested label before the forwarded
   * label.
   */
  it('renders a requested-to-forwarded mapping', () => {
    expect(formatReasoningEffortMapping('max', 'xhigh')).toBe('Max → XHigh')
  })

  /**
   * A historical row has a forwarded reasoning level but no requested value. The
   * display uses the effective label alone and avoids adding a missing-side arrow.
   */
  it('falls back to the effective value for legacy rows', () => {
    expect(formatReasoningEffortMapping(undefined, 'high')).toBe('High')
  })
})
