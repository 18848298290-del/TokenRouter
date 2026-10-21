import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OllamaCloudUsageCell from '../OllamaCloudUsageCell.vue'
import UsageProgressBar from '../UsageProgressBar.vue'
import type { Provider, OllamaCloudUsageState } from '@/api/admin/providers.types'

// OllamaCloudUsageCell.spec.ts checks Ollama Cloud usage windows and explicit
// refresh behavior for configured browser sessions. Provider snapshots drive
// percentage bars, and the refresh endpoint returns state that the cell
// displays and emits to its parent.

// refreshOllamaCloudUsage captures manual refresh calls and supplies
// replacement snapshots so the test can compare displayed bars with the
// emitted provider state.
const { refreshOllamaCloudUsage } = vi.hoisted(() => ({
  refreshOllamaCloudUsage: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      refreshOllamaCloudUsage,
    },
  },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// usageState returns a fresh configured Ollama Cloud snapshot with two
// windows.
const usageState = (): OllamaCloudUsageState => ({
  provider_id: 7,
  eligible: true,
  configured: true,
  auto_refresh_enabled: false,
  encryption_key_configured: true,
  snapshot: {
    status: 'ok',
    fetched_at: '2026-07-22T12:00:00Z',
    last_attempt_at: '2026-07-22T12:00:00Z',
    next_refresh_at: '2026-07-22T13:00:00Z',
    data: {
      plan: 'max',
      five_hour: { used_percent: 5.6, reset_at: '2026-07-23T03:00:00Z' },
      seven_day: { used_percent: 14.2, reset_at: '2026-07-29T00:00:00Z' },
      balance: '$0',
      models: [
        { model: 'gpt-oss:120b-cloud', window: 'five_hour', requests: 2 },
        { model: 'gpt-oss:120b-cloud', window: 'seven_day', requests: 12 },
      ],
    },
  },
})

// provider builds an OpenAI API Key record holding the supplied Ollama state.
// Omitting state creates a fresh default snapshot; passing state attaches that
// object directly for replacement-prop assertions.
const provider = (state = usageState()): Provider => ({
  id: 7,
  name: 'ollama',
  platform: 'openai',
  type: 'apikey',
  ollama_cloud_usage: state,
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-07-22T00:00:00Z',
  updated_at: '2026-07-22T00:00:00Z',
  schedulable: true,
  rate_limited_at: null,
  rate_limit_reset_at: null,
  overload_until: null,
  temp_unschedulable_until: null,
  temp_unschedulable_reason: null,
  session_window_start: null,
  session_window_end: null,
  session_window_status: null,
})

describe('OllamaCloudUsageCell', () => {
  beforeEach(() => {
    refreshOllamaCloudUsage.mockReset()
  })

  // A configured Ollama Cloud provider supplies five-hour and seven-day
  // snapshots. Both bars receive their percentages and reset timestamps, while
  // compact presentation hides plan, balance, and model details.
  it('renders native 5h and 7d windows with the configured-provider query action', () => {
    const wrapper = mount(OllamaCloudUsageCell, { props: { provider: provider() } })
    const cell = wrapper.get('[data-testid="ollama-cloud-usage-cell"]')
    expect(cell.classes()).toEqual(expect.arrayContaining(['min-w-0', 'max-w-full']))
    expect(cell.classes()).not.toContain('min-w-[12rem]')

    const bars = wrapper.findAllComponents(UsageProgressBar)
    expect(bars).toHaveLength(2)
    expect(bars[0].props()).toMatchObject({
      label: '5h',
      utilization: 5.6,
      resetsAt: '2026-07-23T03:00:00Z',
    })
    expect(bars[1].props()).toMatchObject({
      label: '7d',
      utilization: 14.2,
      resetsAt: '2026-07-29T00:00:00Z',
    })

    expect(wrapper.find('[data-testid="ollama-cloud-usage-details"]').exists()).toBe(false)
    const query = wrapper.get('[data-testid="ollama-cloud-usage-query"]')
    expect(query.classes()).toEqual(
      expect.arrayContaining(['text-primary-600', 'hover:bg-primary-50']),
    )
    expect(query.text()).toContain('admin.providers.usageWindow.activeQuery')
    expect(wrapper.text()).not.toContain('max')
    expect(wrapper.text()).not.toContain('$0')
    expect(wrapper.text()).not.toContain('gpt-oss:120b-cloud')
  })

  // The parent replaces an Ollama Cloud provider snapshot with a higher
  // five-hour percentage. The existing cell updates its first progress bar to
  // the replacement value.
  it('reacts to a provider snapshot update', async () => {
    const wrapper = mount(OllamaCloudUsageCell, { props: { provider: provider() } })
    const next = usageState()
    next.snapshot!.data!.five_hour!.used_percent = 43

    await wrapper.setProps({ provider: provider(next) })

    expect(wrapper.findAllComponents(UsageProgressBar)[0].props('utilization')).toBe(43)
  })

  // An explicit query returns updated Ollama Cloud usage state. The cell calls
  // the provider refresh endpoint, renders the new percentage, and emits the
  // complete returned state.
  it('queries through the edit-page refresh endpoint and emits the updated state', async () => {
    const next = usageState()
    next.snapshot!.data!.five_hour!.used_percent = 43
    refreshOllamaCloudUsage.mockResolvedValueOnce(next)
    const wrapper = mount(OllamaCloudUsageCell, { props: { provider: provider() } })

    await wrapper.get('[data-testid="ollama-cloud-usage-query"]').trigger('click')
    await flushPromises()

    expect(refreshOllamaCloudUsage).toHaveBeenCalledWith(7)
    expect(wrapper.findAllComponents(UsageProgressBar)[0].props('utilization')).toBe(43)
    expect(wrapper.emitted<OllamaCloudUsageState[]>('updated')?.[0]?.[0]).toEqual(next)
  })

  // An eligible Ollama Cloud provider has no configured browser session. The
  // cell hides its query action so no manual refresh control is offered for
  // that state.
  it('hides the query action until a browser session is configured', () => {
    const state = usageState()
    state.configured = false

    const wrapper = mount(OllamaCloudUsageCell, { props: { provider: provider(state) } })

    expect(wrapper.find('[data-testid="ollama-cloud-usage-query"]').exists()).toBe(false)
  })
})
