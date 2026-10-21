import { describe, expect, it } from 'vitest'

import enProviders from '@/i18n/locales/en/admin/providers'
import enPricing from '@/i18n/locales/en/admin/pricing'
import zhProviders from '@/i18n/locales/zh/admin/providers'
import zhPricing from '@/i18n/locales/zh/admin/pricing'

// modelRulesCopy.spec.ts checks provider model-rule and billing-source
// terminology across the bilingual catalogs. The assertions distinguish final
// upstream model allowlists, provider mapping, and pricing lookup choices so
// descriptions keep routing permissions separate from billing configuration.

describe('model routing copy', () => {
  // Provider model rules present mapping and final-model allowlists in both
  // languages. The checked labels and empty-allowlist hints describe those
  // controls consistently across the provider editor.
  it('keeps the provider rule wording aligned in Chinese and English', () => {
    expect(zhProviders.providers.modelRestriction).toBe('提供商模型规则（可选）')
    expect(zhProviders.providers.modelWhitelist).toBe('最终模型白名单')
    expect(zhProviders.providers.modelMapping).toBe('提供商模型映射')
    expect(zhProviders.providers.supportsAllModels).toBe('留空不限制模型')
    expect(zhProviders.providers.modelRestrictionCombinedHint).toContain('留空不限制模型')
    expect(zhProviders.providers.syncUpstreamModelsNoChanges).toContain('最终模型白名单')

    expect(enProviders.providers.modelRestriction).toBe('Provider Model Rules (Optional)')
    expect(enProviders.providers.modelWhitelist).toBe('Final Model Whitelist')
    expect(enProviders.providers.modelMapping).toBe('Provider Model Mapping')
    expect(enProviders.providers.supportsAllModels).toBe('An empty whitelist allows any model ID')
    expect(enProviders.providers.syncUpstreamModelsNoChanges).toContain('final model whitelist')
  })

  // Billing source choices select the model identifier used for pricing lookup.
  // Their descriptions retain group permissions as a separate rule and identify
  // requested, group-mapped, and final-upstream sources.
  it('keeps billing sources separate from group model permissions', () => {
    expect(zhPricing.pricing.form.billingModelSourceGroupMapped).toBe('分组映射后的模型（默认）')
    expect(zhPricing.pricing.form.billingModelSourceRequested).toBe('客户端请求模型')
    expect(zhPricing.pricing.form.billingModelSourceUpstream).toBe('提供商最终上游模型')
    expect(zhPricing.pricing.form.billingModelSourceHintRequested).toContain(
      '模型权限由分组白名单决定',
    )
    expect(enPricing.pricing.form.billingModelSourceHintUpstream).toBe(
      'Look up prices using the final upstream model.',
    )
  })
})
