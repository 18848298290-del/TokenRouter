import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

// gatewaySettingsLocales.spec.ts checks bilingual gateway settings terminology
// and platform sections. The tests compare section keys, scope descriptions, and
// removed controls so language catalogs describe the settings exposed by the
// gateway configuration interface.

describe('gateway settings locale copy', () => {
  // Gateway metadata expects a specific platform section sequence in each
  // catalog. English and Chinese expose the same section keys so the settings
  // navigation receives matching labels.
  it('keeps the platform section labels aligned across locales', () => {
    const expectedSections = [
      'general',
      'anthropic',
      'openai',
      'grok',
      'antigravity',
      'ollamaCloud',
    ]

    expect(Object.keys(zh.admin.settings.gatewaySections).filter((key) => key !== 'label')).toEqual(
      expectedSections,
    )
    expect(Object.keys(en.admin.settings.gatewaySections).filter((key) => key !== 'label')).toEqual(
      expectedSections,
    )
  })

  // Gateway rectifier and prompt replacement settings apply across eligible
  // request flows. Both catalogs describe their global scope and retain the
  // supported WebSocket reference in the replacement explanation.
  it('states the cross-platform scope of rectifier and prompt replacement settings', () => {
    expect(zh.admin.settings.rectifier.description).toContain('全局')
    expect(zh.admin.settings.rectifier.description).toContain('适用的网关重试链路')
    expect(en.admin.settings.rectifier.description).toContain('global')
    expect(en.admin.settings.rectifier.description).toContain('eligible gateway retry flows')
    expect(zh.admin.settings.userPromptReplacement.description).toContain('全局')
    expect(zh.admin.settings.userPromptReplacement.description).toContain('受支持的网关入站请求')
    expect(en.admin.settings.userPromptReplacement.description).toContain('Globally')
    expect(en.admin.settings.userPromptReplacement.description).toContain(
      'supported gateway requests',
    )
    expect(zh.admin.settings.userPromptReplacement.description).toContain(
      'OpenAI Responses WebSocket',
    )
    expect(en.admin.settings.userPromptReplacement.description).toContain(
      'OpenAI Responses WebSocket',
    )
  })

  // Gateway settings dictionaries omit CCH signing labels and hints. The test
  // checks both catalogs so neither language supplies text for these retired
  // controls.
  it('does not expose retired CCH signing copy', () => {
    expect(zh.admin.settings.gatewayForwarding).not.toHaveProperty('cchSigning')
    expect(zh.admin.settings.gatewayForwarding).not.toHaveProperty('cchSigningHint')
    expect(en.admin.settings.gatewayForwarding).not.toHaveProperty('cchSigning')
    expect(en.admin.settings.gatewayForwarding).not.toHaveProperty('cchSigningHint')
  })

  // Gateway settings dictionaries omit Grok cross-client mapping labels and
  // hints. The test checks both catalogs so these retired controls have no
  // message keys in either language.
  it('omits retired Grok cross-client mapping controls', () => {
    for (const copy of [zh.admin.settings.gatewayForwarding, en.admin.settings.gatewayForwarding]) {
      expect(copy).not.toHaveProperty('grokCrossClientMap')
      expect(copy).not.toHaveProperty('grokCrossClientMapHint')
    }
  })
})
