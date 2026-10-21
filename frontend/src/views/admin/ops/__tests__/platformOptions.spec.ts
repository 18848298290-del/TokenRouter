import { describe, expect, it } from 'vitest'
import { buildOpsPlatformOptions } from '../platformOptions'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

// platformOptions.spec.ts checks the platform options exposed by the Ops
// dashboard independently of group data. The helper receives the translated
// all-platforms label and returns execution-platform identities suitable for the
// toolbar selector.

describe('buildOpsPlatformOptions', () => {
  // The Ops selector receives a translated label for its unfiltered option. Its
  // remaining values must match the execution-platform catalog while group
  // metadata supplies no platform entries.
  it('执行平台目录独立于分组', () => {
    expect(buildOpsPlatformOptions('全部')).toEqual([
      { value: '', label: '全部' },
      ...CONCRETE_PLATFORM_OPTIONS,
    ])
  })
})
