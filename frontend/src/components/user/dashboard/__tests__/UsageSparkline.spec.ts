import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'

import UsageSparkline from '../UsageSparkline.vue'

// UsageSparkline.spec.ts checks the production sparkline geometry from exact rendered path
// coordinates. It covers zero-based scaling, single-bucket visibility, and null observations
// so changes to the small chart retain its bucket positions and empty-data behavior.

// pathOf mounts a supplied series and returns its rendered SVG path data. Callers use inputs
// with present points; empty-series behavior is inspected directly through the component
// wrapper.
const pathOf = (values: Array<number | null>) =>
  mount(UsageSparkline, { props: { values } }).find('path').attributes('d')

describe('UsageSparkline', () => {
  // A three-point series contains zero, a midpoint, and its maximum. The test checks the
  // exact padded SVG coordinates and evenly spaced bucket positions.
  it('按最大值等比缩放，最高点贴近顶部，0 贴近底部', () => {
    expect(pathOf([0, 5, 10])).toBe('M0.00 30.00 L50.00 16.00 L100.00 2.00')
  })

  // The series contains either all zeros or one nonzero bucket. The test protects a visible
  // horizontal line for both cases and the correct vertical level.
  it('全为 0 时画贴底平线，只有一个时段时画成横线', () => {
    expect(pathOf([0, 0])).toBe('M0.00 30.00 L100.00 30.00')
    expect(pathOf([3])).toBe('M0 2.00 L100 2.00')
  })

  // Null observations separate two present values, followed by entirely absent input cases.
  // The test protects gap-spanning paths and omission of paths when no observations exist.
  it('跳过 null 并连接前后时段，没有数据时不画线', () => {
    expect(pathOf([1, null, 1])).toBe('M0.00 2.00 L100.00 2.00')
    expect(
      mount(UsageSparkline, { props: { values: [null, null] } })
        .find('path')
        .exists(),
    ).toBe(false)
    expect(
      mount(UsageSparkline, { props: { values: [] } })
        .find('path')
        .exists(),
    ).toBe(false)
  })
})
