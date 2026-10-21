import { describe, expect, it } from 'vitest'
import en from '@/i18n/locales/en'
import zh from '@/i18n/locales/zh'

// opsLocaleKeys.spec.ts checks the locale keys required by operational error
// views and administrator group controls. Both catalogs must contain these paths
// so error evidence, response attribution, recovery actions, and web-search
// pricing labels remain available after a language switch.

// flattenKeys returns dot-separated paths for leaf values in a nested catalog.
// The optional prefix extends an existing path, and arrays remain leaves because
// the tested message paths address object properties.
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
    // Operational views request each listed message from the English catalog.
    // Flattened catalog paths contain the requested key so the interface can
    // resolve its evidence or recovery label.
    it(`en locale has ${key}`, () => {
      const enKeys = flattenKeys(en)
      expect(enKeys).toContain(key)
    })
  }

  for (const key of requiredKeys) {
    // Operational views request each listed message from the Chinese catalog.
    // Flattened catalog paths contain the requested key so the interface can
    // resolve its evidence or recovery label.
    it(`zh locale has ${key}`, () => {
      const zhKeys = flattenKeys(zh)
      expect(zhKeys).toContain(key)
    })
  }
})

describe('groups locale key completeness', () => {
  // The group save flow needs an English failure message after an unsuccessful
  // update. Its expected catalog path remains present so the error can resolve a
  // translated label.
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
    // Group web-search pricing controls use the same message paths in both
    // languages. Each listed path exists in both catalogs so the form can switch
    // languages consistently.
    it(`en and zh locales both have ${key}`, () => {
      expect(flattenKeys(en)).toContain(key)
      expect(flattenKeys(zh)).toContain(key)
    })
  }
})
