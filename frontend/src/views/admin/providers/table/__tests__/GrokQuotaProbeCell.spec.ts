import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import GrokQuotaProbeCell from '../GrokQuotaProbeCell.vue'
import type { Provider } from '@/api/admin/providers.types'

// GrokQuotaProbeCell.spec.ts checks partial Grok quota probe results when
// billing data survives a failed fallback probe. The cell must surface the
// probe error and emit the retained billing observation so the parent can use
// both parts.

// queryQuota supplies a hybrid probe result where billing survives a fallback
// error, allowing emitted data and visible failure text to be inspected
// together.
const { queryQuota } = vi.hoisted(() => ({
  queryQuota: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: { queryQuota },
  },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params?.percent == null ? key : `${key}:${params.percent}`,
  }),
}))

// provider identifies the Grok OAuth account whose manual quota query is
// tested, with only the identity and capability fields required by the cell.
const provider = {
  id: 99,
  platform: 'grok',
  type: 'oauth',
} as Provider

describe('GrokQuotaProbeCell', () => {
  beforeEach(() => {
    queryQuota.mockReset()
  })

  // A quota query returns billing data together with a fallback probe error.
  // The cell displays that error and emits a result retaining both the billing
  // period and failure detail.
  it('keeps billing data while exposing a failed Free quota fallback', async () => {
    queryQuota.mockResolvedValue({
      source: 'hybrid_probe',
      billing: { period_type: 'weekly', usage_percent: null },
      headers_observed: false,
      reset_supported: false,
      fetched_at: 1,
      probe_error: 'upstream returned 402 for probe model "grok-4.5"',
    })
    const wrapper = mount(GrokQuotaProbeCell, { props: { provider } })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('upstream returned 402 for probe model "grok-4.5"')
    expect(wrapper.emitted('probed')?.[0]?.[0]).toMatchObject({
      billing: { period_type: 'weekly', usage_percent: null },
      probe_error: 'upstream returned 402 for probe model "grok-4.5"',
    })
  })
})
