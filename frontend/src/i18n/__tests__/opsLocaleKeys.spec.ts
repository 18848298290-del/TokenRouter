import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

// opsLocaleKeys.spec.ts checks the locale keys required by operational error
// views and related administrator controls. Both source catalogs must contain
// these entries so translated summaries, response attribution, and recovery
// actions remain available when users change the interface language.

function flattenKeys(obj: Record<string, any>, prefix = ''): string[] {
  const keys: string[] = []
  for (const [k, v] of Object.entries(obj)) {
    const fullKey = prefix ? `${prefix}.${k}` : k
    if (typeof v === 'object' && v !== null && !Array.isArray(v)) {
      keys.push(...flattenKeys(v, fullKey))
    } else {
      keys.push(fullKey)
    }
  }
  return keys
}

describe('ops locale key completeness', () => {
  // These keys cover error evidence and recovery controls shared by both source
  // catalogs, including the format-only summaries used for HTML and JSON responses.
  const requiredKeys = [
    'admin.ops.result',
    'admin.ops.timeRange.custom',
    'admin.ops.customTimeRange.startTime',
    'admin.ops.customTimeRange.endTime',
    'admin.ops.errorDetail.upstreamStatus',
    'admin.ops.errorDetail.summary',
    'admin.ops.errorDetail.summarySource',
    'admin.ops.errorDetail.requestInfo',
    'admin.ops.errorDetail.upstreamAttempts',
    'admin.ops.errorDetail.attemptsLoadFailed',
    'admin.ops.errorDetail.copyPayload',
    'admin.ops.errorDetail.summaryFormats.html',
    'admin.ops.errorDetail.summaryFormats.json',
    'admin.ops.errorDetail.summaryFormats.empty',
    'admin.ops.errorDetail.payloads.message',
    'admin.ops.errorDetail.diagnosticPayloads',
    'admin.ops.errorDetail.payloads.client',
    'admin.ops.errorDetail.payloads.upstream_message',
    'admin.ops.errorDetail.payloads.upstream_detail',
    'admin.ops.errorDetail.payloads.upstream_events',
  ]

  for (const key of requiredKeys) {
    it(`en locale has ${key}`, () => {
      const enKeys = flattenKeys(en)
      expect(enKeys).toContain(key)
    })
  }

  for (const key of requiredKeys) {
    it(`zh locale has ${key}`, () => {
      const zhKeys = flattenKeys(zh)
      expect(zhKeys).toContain(key)
    })
  }
})

describe('groups locale key completeness', () => {
  it('en locale has admin.groups.failedToSave', () => {
    const enKeys = flattenKeys(en)
    expect(enKeys).toContain('admin.groups.failedToSave')
  })

  const webSearchPricingKeys = [
    'admin.groups.webSearchPricing.title',
    'admin.groups.webSearchPricing.pricePerCall',
    'admin.groups.webSearchPricing.pricePerCallHint',
    'admin.groups.webSearchPricing.finalPricePreview',
  ]

  for (const key of webSearchPricingKeys) {
    it(`en and zh locales both have ${key}`, () => {
      expect(flattenKeys(en)).toContain(key)
      expect(flattenKeys(zh)).toContain(key)
    })
  }
})
