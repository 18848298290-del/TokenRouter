import { describe, expect, it } from 'vitest'
import {
  areDefaultLatencyBucketBoundaries,
  defaultLatencyBucketBoundaries,
  normalizeLatencyBucketBoundaries,
  parseLatencyBucketBoundaries,
  serializeLatencyBucketBoundaries,
} from '../latencyBuckets'

// latencyBuckets.spec.ts checks duration-boundary validation and browser-query
// serialization using direct helper calls. Valid arrays round-trip through
// decimal text, invalid configurations return null, and editable defaults do not
// share their array with subsequent callers.

describe('latencyBuckets', () => {
  // A valid five-boundary query uses ascending decimal milliseconds. Parsing
  // must produce the expected numeric sequence and serialization must
  // reconstruct the same comma-separated parameter text.
  it('解析并序列化合法的 URL 分桶参数', () => {
    const boundaries = parseLatencyBucketBoundaries('1000,2000,5000,10000,20000')
    expect(boundaries).toEqual([1000, 2000, 5000, 10000, 20000])
    expect(serializeLatencyBucketBoundaries(boundaries!)).toBe('1000,2000,5000,10000,20000')
  })

  // Candidate boundaries violate count, order, integer syntax, positivity, or
  // the maximum duration. Each rejected input must return null so callers can
  // select a valid preference or default.
  it('拒绝数量、顺序、整数和最大值不合法的边界', () => {
    expect(parseLatencyBucketBoundaries('100,200')).toBeNull()
    expect(parseLatencyBucketBoundaries('100,200,200,1000,2000')).toBeNull()
    expect(parseLatencyBucketBoundaries('100,200.5,500,1000,2000')).toBeNull()
    expect(parseLatencyBucketBoundaries('1e2,200,500,1000,2000')).toBeNull()
    expect(parseLatencyBucketBoundaries('100,200,500,1000,86400001')).toBeNull()
    expect(normalizeLatencyBucketBoundaries([0, 200, 500, 1000, 2000])).toBeNull()
  })

  // A caller mutates the returned default boundary array after confirming its
  // identity. A later call must still return the standard sequence in
  // independent array storage.
  it('返回独立的默认值副本', () => {
    const boundaries = defaultLatencyBucketBoundaries()
    expect(areDefaultLatencyBucketBoundaries(boundaries)).toBe(true)
    boundaries[0] = 999
    expect(defaultLatencyBucketBoundaries()).toEqual([100, 200, 500, 1000, 2000])
  })
})
