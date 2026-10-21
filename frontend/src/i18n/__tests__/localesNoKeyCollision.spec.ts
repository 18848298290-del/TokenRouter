import { describe, expect, it } from 'vitest'

import enAdminProviders from '../locales/en/admin/providers'
import enAdminPricing from '../locales/en/admin/pricing'
import enAdminOps from '../locales/en/admin/ops'
import enAdminOverview from '../locales/en/admin/overview'
import enAdminResources from '../locales/en/admin/resources'
import enAdminSettings from '../locales/en/admin/settings'
import enCommon from '../locales/en/common'
import enDashboard from '../locales/en/dashboard'
import enLanding from '../locales/en/landing'
import enMisc from '../locales/en/misc'
import zhAdminProviders from '../locales/zh/admin/providers'
import zhAdminPricing from '../locales/zh/admin/pricing'
import zhAdminOps from '../locales/zh/admin/ops'
import zhAdminOverview from '../locales/zh/admin/overview'
import zhAdminResources from '../locales/zh/admin/resources'
import zhAdminSettings from '../locales/zh/admin/settings'
import zhCommon from '../locales/zh/common'
import zhDashboard from '../locales/zh/dashboard'
import zhLanding from '../locales/zh/landing'
import zhMisc from '../locales/zh/misc'

// localesNoKeyCollision.spec.ts checks catalog namespace assembly and selected
// provider translation paths. Object spreads can replace earlier keys silently,
// so tests detect overlaps and verify that threshold controls and scheduler
// diagnostics resolve their expected message locations.

// Modules maps catalog fragment names to their exported top-level message
// entries.
type Modules = Record<string, Record<string, unknown>>

// collisions reports keys shared by named catalog fragments using the first
// owning fragment. Results include both fragment names so a failed spread-
// assembly check identifies which dictionaries would overwrite each other.
function collisions(modules: Modules): string[] {
  const seen = new Map<string, string>()
  const out: string[] = []
  for (const [name, mod] of Object.entries(modules)) {
    for (const key of Object.keys(mod)) {
      const prev = seen.get(key)
      if (prev) {
        out.push(`"${key}" in both ${prev} and ${name}`)
      } else {
        seen.set(key, name)
      }
    }
  }
  return out
}

// roots groups the root catalog fragments checked for overlapping translation
// namespaces in each supported language.
const roots: Record<string, Modules> = {
  zh: { landing: zhLanding, common: zhCommon, dashboard: zhDashboard, misc: zhMisc },
  en: { landing: enLanding, common: enCommon, dashboard: enDashboard, misc: enMisc },
}

// admins groups administrator catalog fragments checked for duplicate namespaces
// before their exported entries are combined.
const admins: Record<string, Modules> = {
  zh: {
    overview: zhAdminOverview,
    channels: zhAdminPricing,
    providers: zhAdminProviders,
    resources: zhAdminResources,
    ops: zhAdminOps,
    settings: zhAdminSettings,
  },
  en: {
    overview: enAdminOverview,
    channels: enAdminPricing,
    providers: enAdminProviders,
    resources: enAdminResources,
    ops: enAdminOps,
    settings: enAdminSettings,
  },
}

describe.each(Object.keys(roots))('locale %s spread assembly', (locale) => {
  // Root catalog fragments are merged through object spreads in each language.
  // Their top-level keys remain disjoint so one fragment cannot silently replace
  // another fragment during assembly.
  it('root modules have no overlapping top-level keys', () => {
    expect(collisions(roots[locale])).toEqual([])
  })

  // The catalog adds administrator messages through an explicit admin namespace.
  // Root fragments do not define that namespace, preventing an ordinary spread
  // from replacing administrator message content.
  it('root modules do not shadow the explicit "admin" namespace', () => {
    for (const [name, mod] of Object.entries(roots[locale])) {
      expect(Object.keys(mod), `module ${name} must not define "admin"`).not.toContain('admin')
    }
  })

  // Administrator catalog fragments are merged through object spreads in each
  // language. Their top-level keys remain distinct so provider, pricing,
  // settings, and other sections survive assembly.
  it('admin modules have no overlapping top-level keys', () => {
    expect(collisions(admins[locale])).toEqual([])
  })
})
describe('provider scheduling threshold locale hierarchy', () => {
  // Provider threshold form labels are consumed directly below the providers
  // namespace. Both catalogs retain those keys outside status so the controls
  // resolve text at their expected paths.
  it.each([
    ['zh', zhAdminProviders.providers],
    ['en', enAdminProviders.providers],
  ] as const)('%s keeps threshold form keys outside status', (_locale, messages) => {
    const record = messages as unknown as Record<string, unknown>
    const status = record.status as Record<string, unknown>
    const keys = [
      'providerSchedulingThresholdOverride',
      'providerSchedulingThresholdOverrideHint',
      'providerSchedulingThresholdOverrideValue',
      'providerSchedulingThresholdOverrideDisabledHint',
    ]

    for (const key of keys) {
      expect(record).toHaveProperty(key)
      expect(status).not.toHaveProperty(key)
    }
  })
})
describe('advanced scheduler diagnostic locale keys', () => {
  const settingKeys = [
    'sticky_weighted_enabled',
    'subscription_priority_enabled',
    'ewma_error_rate_alpha',
    'ewma_ttft_alpha',
    'sticky_escape_enabled',
    'sticky_escape_ttft_ms',
    'sticky_escape_error_rate',
    'lb_top_k',
    'weight_priority',
    'weight_load',
    'weight_queue',
    'weight_error_rate',
    'weight_ttft',
    'weight_reset',
    'weight_quota_headroom',
    'weight_previous_response',
    'weight_session_sticky',
  ]

  // Scheduler diagnostics identify settings through backend setting names. Both
  // catalogs provide text for every listed name so those diagnostic rows can
  // display translated settings labels.
  it.each([
    ['zh', zhAdminProviders.providers],
    ['en', enAdminProviders.providers],
  ] as const)('%s translates every diagnostic setting', (_locale, messages) => {
    const settingNames = (
      messages as unknown as {
        advancedSchedulerScore: { settingNames: Record<string, unknown> }
      }
    ).advancedSchedulerScore.settingNames

    for (const key of settingKeys) {
      expect(settingNames[key], `${_locale} missing setting name ${key}`).toEqual(
        expect.any(String),
      )
      expect(settingNames[key]).not.toBe(
        `admin.providers.advancedSchedulerScore.settingNames.${key}`,
      )
    }
  })
})
