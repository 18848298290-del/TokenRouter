import { mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ProviderTodayStatsCell from '../ProviderTodayStatsCell.vue'

// ProviderTodayStatsCell.spec.ts checks the currency labels on provider and
// user billing totals. A temporary application balance symbol distinguishes
// configurable user charges from provider costs, and cleanup restores the
// prior browser configuration after the assertion.

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))

// originalConfig retains the application configuration object before the test
// changes its balance symbol, allowing the cleanup hook to restore browser
// state afterward.
const originalConfig = window.__APP_CONFIG__

afterEach(() => {
  // Restore the captured application configuration after the currency
  // assertion so the custom balance symbol does not affect later formatting
  // tests.
  window.__APP_CONFIG__ = originalConfig
})

describe('ProviderTodayStatsCell', () => {
  // The application configures a custom balance symbol before rendering daily
  // totals. Provider billing keeps its dollar prefix while user billing uses
  // the configured symbol and formatted amount.
  it('提供商计费用美元，用户扣费用配置的余额符号', () => {
    window.__APP_CONFIG__ = {
      ...originalConfig,
      balance_unit_symbol: '🍥',
    }

    const wrapper = mount(ProviderTodayStatsCell, {
      props: {
        stats: { requests: 2224, tokens: 287860000, cost: 190.15, user_cost: 4754.97 },
      },
      global: { plugins: [createPinia()] },
    })

    expect(wrapper.text()).toContain('usage.providerBilled:$190.15')
    expect(wrapper.text()).toContain('usage.userBilled:🍥4,754.97')
  })
})
