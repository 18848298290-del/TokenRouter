import { describe, expect, it } from 'vitest'

import { numericRequestTypeKind, requestTypeLabelKey } from '@/utils/errorBadges'
import {
  isUsageRequestType,
  requestTypeToLegacyStream,
  resolveUsageRequestType,
} from '@/utils/usageRequestType'

/**
 * usageRequestType.spec.ts tests the live request category across string and
 * numeric usage helpers. The cases retain live despite a false legacy stream
 * flag and leave its reverse mapping to the legacy boolean unspecified.
 */

describe('usage request type utils', () => {
  /**
   * Live is supplied through current string and numeric request-type forms. Recognition
   * and label helpers retain the live category even when a legacy stream flag is false.
   */
  it('识别并展示 Live 请求类型', () => {
    expect(isUsageRequestType('live')).toBe(true)
    expect(resolveUsageRequestType({ request_type: 'live', stream: false })).toBe('live')
    expect(numericRequestTypeKind(5, false)).toBe('live')
    expect(requestTypeLabelKey('live')).toBe('usage.live')
  })

  /**
   * A live request category is converted into the legacy stream-filter dimension. The
   * result is null, leaving that boolean unspecified for a category with separate
   * display meaning.
   */
  it('不把 Live 回填为旧版流式维度', () => {
    expect(requestTypeToLegacyStream('live')).toBeNull()
  })
})
