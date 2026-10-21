import { describe, expect, it } from 'vitest'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

// platforms.spec.ts verifies the ordered provider-platform catalog used by shared
// administrator filters. An independent expected list protects catalog coverage and
// ordering. Comparing identifiers directly keeps translated labels and presentation
// details outside the provider-availability assertion.

// concretePlatforms records the expected shared platform order so the assertion catches
// both omitted providers and unintended changes to filter ordering.
const concretePlatforms = [
  'anthropic',
  'openai',
  'gemini',
  'antigravity',
  'qoder',
  'grok',
  'kimi',
  'zhipu',
  'deepseek',
  'jev',
]

describe('platform option catalogs', () => {
  // The shared catalog supplies concrete platform identifiers to administrator filters.
  // Its ordered values include every expected provider platform so a filter cannot
  // silently omit a supported choice.
  it('exposes every concrete provider platform', () => {
    expect(CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)).toEqual(concretePlatforms)
  })
})
