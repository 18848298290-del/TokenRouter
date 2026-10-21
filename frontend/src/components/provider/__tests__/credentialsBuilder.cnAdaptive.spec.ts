import { describe, expect, it } from 'vitest'

import {
  cnSupportsNativeResponses,
  defaultCNAdaptiveBaseUrls,
} from '@/components/provider/form/providerEndpoints'

// credentialsBuilder.cnAdaptive.spec.ts tests CN protocol support and mode-specific
// endpoint presets. Exact address assertions distinguish Kimi and GLM billing modes,
// including GLM empty Responses slots, while DeepSeek supplies all three native text
// endpoint addresses.

describe('cnSupportsNativeResponses', () => {
  // The capability helper receives supported and unsupported CN platform names. It must
  // report Responses support only for DeepSeek and Kimi within the platforms exercised by
  // this test.
  it('is true for DeepSeek and Kimi only', () => {
    expect(cnSupportsNativeResponses('deepseek')).toBe(true)
    expect(cnSupportsNativeResponses('kimi')).toBe(true)
    expect(cnSupportsNativeResponses('zhipu')).toBe(false)
    expect(cnSupportsNativeResponses('openai')).toBe(false)
  })
})

describe('defaultCNAdaptiveBaseUrls', () => {
  // Kimi endpoint presets are requested for pay-as-you-go and coding provider modes. Each
  // mode must resolve the expected protocol-specific base addresses supplied by the
  // shared endpoint helper.
  it('resolves Kimi endpoints by provider mode', () => {
    expect(defaultCNAdaptiveBaseUrls('kimi', 'payg')).toEqual({
      chat_completions: 'https://api.moonshot.cn/v1',
      anthropic: 'https://api.moonshot.cn/anthropic',
      responses: 'https://api.moonshot.cn/v1',
    })
    expect(defaultCNAdaptiveBaseUrls('kimi', 'coding')).toEqual({
      chat_completions: 'https://api.kimi.com/coding/v1',
      anthropic: 'https://api.kimi.com/coding',
      responses: 'https://api.kimi.com/coding/v1',
    })
  })

  // GLM endpoint presets are requested for pay-as-you-go and coding provider modes. Each
  // mode must resolve its expected protocol-specific addresses without inventing a
  // Responses endpoint for the platform.
  it('resolves GLM endpoints by provider mode', () => {
    expect(defaultCNAdaptiveBaseUrls('zhipu', 'payg')).toEqual({
      chat_completions: 'https://open.bigmodel.cn/api/paas/v4',
      anthropic: 'https://open.bigmodel.cn/api/anthropic',
      responses: '',
    })
    expect(defaultCNAdaptiveBaseUrls('zhipu', 'coding')).toEqual({
      chat_completions: 'https://open.bigmodel.cn/api/coding/paas/v4',
      anthropic: 'https://open.bigmodel.cn/api/anthropic',
      responses: '',
    })
  })

  // DeepSeek exposes its supported native text protocol endpoints through the preset
  // helper. The returned defaults must include Chat Completions, Anthropic Messages, and
  // Responses addresses together.
  it('includes all three native DeepSeek endpoints', () => {
    expect(defaultCNAdaptiveBaseUrls('deepseek', 'payg')).toEqual({
      chat_completions: 'https://api.deepseek.com',
      anthropic: 'https://api.deepseek.com/anthropic',
      responses: 'https://api.deepseek.com',
    })
  })
})
