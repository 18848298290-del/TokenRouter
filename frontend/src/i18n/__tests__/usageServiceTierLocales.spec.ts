import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

// usageServiceTierLocales.spec.ts checks bilingual service-tier labels used in
// usage tooltips. The translated heading and shared tier names remain available
// under the usage namespace, letting requests display priority, ultrafast, flex,
// and standard tiers consistently.

describe('usage service tier locale keys', () => {
  // The Chinese usage tooltip distinguishes its translated heading from shared
  // service-tier names. The catalog retains every supported label so recorded
  // request tiers can be displayed consistently.
  it('contains zh labels for service tier tooltip', () => {
    expect(zh.usage.serviceTier).toBe('服务档位')
    expect(zh.usage.serviceTierPriority).toBe('Fast')
    expect(zh.usage.serviceTierUltrafast).toBe('Ultrafast')
    expect(zh.usage.serviceTierFlex).toBe('Flex')
    expect(zh.usage.serviceTierStandard).toBe('Standard')
  })

  // The English usage tooltip distinguishes its heading from the listed service-
  // tier names. The catalog retains every supported label so recorded request
  // tiers can be displayed consistently.
  it('contains en labels for service tier tooltip', () => {
    expect(en.usage.serviceTier).toBe('Service tier')
    expect(en.usage.serviceTierPriority).toBe('Fast')
    expect(en.usage.serviceTierUltrafast).toBe('Ultrafast')
    expect(en.usage.serviceTierFlex).toBe('Flex')
    expect(en.usage.serviceTierStandard).toBe('Standard')
  })
})
