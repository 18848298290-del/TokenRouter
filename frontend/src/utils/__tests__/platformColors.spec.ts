import { describe, expect, it } from 'vitest'

import { platformLabel, usagePlatformLabel } from '../platformColors'

/**
 * platformColors.spec.ts tests platform naming in general and usage-specific
 * contexts. Qoder keeps its shared capitalization across both helpers, while the
 * anthropic identifier uses Anthropic generally and Claude on the usage display
 * path.
 */

describe('platformLabels', () => {
  /**
   * The qoder identifier is rendered by both general and usage-specific label helpers.
   * Both return Qoder with the same capitalization for consistent platform
   * presentation.
   */
  it('使用品牌规定的 Qoder 大小写', () => {
    expect(platformLabel('qoder')).toBe('Qoder')
    expect(usagePlatformLabel('qoder')).toBe('Qoder')
  })

  /**
   * The anthropic identifier is rendered in general and usage contexts. The general
   * helper returns Anthropic, while the usage-specific helper returns the Claude
   * product name.
   */
  it('用量页面继续使用 Claude 产品名', () => {
    expect(platformLabel('anthropic')).toBe('Anthropic')
    expect(usagePlatformLabel('anthropic')).toBe('Claude')
  })
})
