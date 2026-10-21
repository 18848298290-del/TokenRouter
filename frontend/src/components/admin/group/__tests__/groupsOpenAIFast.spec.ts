import { describe, expect, it } from 'vitest'
import { normalizeGroupOpenAIFastPolicy } from '../groupsOpenAIFast'
import en from '@/i18n/locales/en/admin/overview'
import zh from '@/i18n/locales/zh/admin/overview'

// groupsOpenAIFast.spec.ts checks normalized group Fast policies and bilingual labels for the
// free-Fast setting. Stored policy values remain available to provider execution, while missing
// or unsupported input resolves to the form default used during submission.

describe('groupsOpenAIFast', () => {
  // Supported group Fast policies and an unknown value pass through normalization. Recognized
  // policies remain unchanged and the unsupported value selects the follow-request default for
  // storage.
  it('分组保存策略，执行时按提供商能力应用', () => {
    expect(normalizeGroupOpenAIFastPolicy('force_ultrafast')).toBe('force_ultrafast')
    expect(normalizeGroupOpenAIFastPolicy('unknown')).toBe('follow_request')
  })

  // The administrator billing setting is read from both locale catalogs. English and Chinese
  // labels describe free Fast behavior so the corresponding controls expose matching
  // user-facing meaning.
  it('提供免费 Fast 的中英文管理文案', () => {
    expect(zh.groups.openaiFast).toMatchObject({
      title: expect.any(String),
      force: expect.any(String),
      hint: expect.stringContaining('Ultra Fast'),
      free: expect.any(String),
      freeHint: expect.stringContaining('Standard'),
    })
    expect(en.groups.openaiFast).toMatchObject({
      title: expect.any(String),
      force: expect.any(String),
      hint: expect.stringContaining('Ultra Fast'),
      free: expect.any(String),
      freeHint: expect.stringContaining('Standard'),
    })
  })
})
