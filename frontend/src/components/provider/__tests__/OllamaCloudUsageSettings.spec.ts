import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OllamaCloudUsageSettings from '../OllamaCloudUsageSettings.vue'
import type { Provider, OllamaCloudUsageState } from '@/api/admin/providers.types'

// OllamaCloudUsageSettings.spec.ts tests browser-session persistence and usage display
// for a saved provider. Mocked endpoints supply encryption availability, snapshot
// updates, and structured refresh errors so the cases can inspect emitted state and
// secret-input clearing.

// api supplies mocked Ollama usage and session endpoints whose responses each case
// controls before inspecting component state or emitted updates.
const api = vi.hoisted(() => ({
  getOllamaCloudUsage: vi.fn(),
  saveOllamaCloudUsageSession: vi.fn(),
  deleteOllamaCloudUsageSession: vi.fn(),
  setOllamaCloudUsageAutoRefresh: vi.fn(),
  refreshOllamaCloudUsage: vi.fn(),
}))
// notifications captures success and error feedback so tests can inspect the user-
// visible outcome of session mutations and manual usage refresh requests.
const notifications = vi.hoisted(() => ({ showSuccess: vi.fn(), showError: vi.fn() }))

vi.mock('@/api/admin', () => ({ adminAPI: { providers: api } }))
vi.mock('@/stores/app', () => ({ useAppStore: () => notifications }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (key === 'admin.providers.ollamaCloud.errors.OLLAMA_CLOUD_USAGE_REFRESH_RATE_LIMITED') {
          return `retry in ${params?.retry_after_seconds} seconds`
        }
        if (key === 'admin.providers.ollamaCloud.fiveHourShort') return '5h'
        if (key === 'admin.providers.ollamaCloud.sevenDayShort') return '7d'
        return key
      },
    }),
  }
})

// state creates a default Ollama usage state with caller-supplied field replacements.
// Each invocation returns a new outer object, while nested values supplied through
// overrides retain their references.
const state = (overrides: Partial<OllamaCloudUsageState> = {}): OllamaCloudUsageState => ({
  provider_id: 7,
  eligible: true,
  configured: false,
  auto_refresh_enabled: false,
  encryption_key_configured: true,
  ...overrides,
})

// detailedState creates a successful Ollama snapshot containing plan and usage-window
// observations. The optional plan replaces the displayed plan name, allowing refresh
// tests to distinguish the initial and updated snapshots.
const detailedState = (plan = 'max'): OllamaCloudUsageState =>
  state({
    configured: true,
    snapshot: {
      status: 'ok',
      fetched_at: '2026-07-22T12:00:00Z',
      last_attempt_at: '2026-07-22T12:00:00Z',
      next_refresh_at: '2026-07-22T13:00:00Z',
      data: {
        plan,
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

// provider creates the provider fixture with the supplied Ollama usage state. The default
// creates a fresh base state, while callers may provide a detailed or
// unavailable-encryption variant for the case.
const provider = (usage: OllamaCloudUsageState = state()): Provider => ({
  id: 7,
  name: 'ollama',
  platform: 'anthropic',
  type: 'apikey',
  ollama_cloud_usage: usage,
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

describe('OllamaCloudUsageSettings', () => {
  beforeEach(() => {
    Object.values(api).forEach((mock) => mock.mockReset())
    notifications.showSuccess.mockReset()
    notifications.showError.mockReset()
  })

  // The provider already includes usage state before a browser session is saved. Mounting
  // must avoid another read, and successful persistence must clear the visible secret and
  // emit refreshed state.
  it('uses the existing provider state without an immediate duplicate GET', async () => {
    api.saveOllamaCloudUsageSession.mockResolvedValueOnce(state({ configured: true }))
    const wrapper = mount(OllamaCloudUsageSettings, { props: { provider: provider() } })
    await flushPromises()

    const input = wrapper.get('#ollama-cloud-session')
    await input.setValue('wos-session=browser-secret')
    await wrapper.get('[data-testid="ollama-cloud-session-save"]').trigger('click')
    await flushPromises()

    expect(api.getOllamaCloudUsage).not.toHaveBeenCalled()
    expect(api.saveOllamaCloudUsageSession).toHaveBeenCalledWith(7, 'wos-session=browser-secret')
    expect((input.element as HTMLTextAreaElement).value).toBe('')
    expect(wrapper.text()).not.toContain('browser-secret')
    expect(wrapper.emitted('updated')?.at(-1)?.[0]).toMatchObject({ configured: true })
  })

  // The server reports that its persistent encryption key is unavailable for session
  // storage. Entering a secret must leave save disabled and must never invoke the session
  // endpoint.
  it('fails closed when the persistent encryption key is unavailable', async () => {
    const wrapper = mount(OllamaCloudUsageSettings, {
      props: { provider: provider(state({ encryption_key_configured: false })) },
    })
    await flushPromises()
    await wrapper.get('#ollama-cloud-session').setValue('wos-session=secret')

    expect(
      wrapper.get('[data-testid="ollama-cloud-session-save"]').attributes('disabled'),
    ).toBeDefined()
    expect(wrapper.text()).toContain('admin.providers.ollamaCloud.encryptionKeyRequired')
    expect(api.saveOllamaCloudUsageSession).not.toHaveBeenCalled()
  })

  // A configured provider changes its automatic refresh switch in the editor. The
  // component must invoke the dedicated provider endpoint with the selected boolean value
  // after that interaction.
  it('updates the provider-level automatic refresh switch through its dedicated endpoint', async () => {
    api.setOllamaCloudUsageAutoRefresh.mockResolvedValueOnce(
      state({ configured: true, auto_refresh_enabled: true }),
    )
    const wrapper = mount(OllamaCloudUsageSettings, {
      props: { provider: provider(state({ configured: true })) },
    })
    await flushPromises()

    await wrapper.get('[data-testid="ollama-cloud-auto-refresh"]').trigger('click')
    await flushPromises()
    expect(api.setOllamaCloudUsageAutoRefresh).toHaveBeenCalledWith(7, true)
  })

  // A detailed snapshot includes plan, balance, model counts, and refresh status. The
  // editor must show those observations and replace the displayed plan after a successful
  // manual refresh.
  it('keeps plan, balance, model, status, and manual refresh details in the edit settings', async () => {
    api.refreshOllamaCloudUsage.mockResolvedValueOnce(detailedState('pro'))
    const wrapper = mount(OllamaCloudUsageSettings, {
      props: { provider: provider(detailedState()) },
    })
    await flushPromises()

    const details = wrapper.get('[data-testid="ollama-cloud-usage-details"]')
    expect(details.text()).toContain('max')
    expect(details.text()).toContain('$0')
    expect(details.text()).toContain('5h gpt-oss:120b-cloud: 2')
    expect(details.text()).toContain('7d gpt-oss:120b-cloud: 12')
    expect(details.text()).toContain('admin.providers.ollamaCloud.ok')

    await wrapper.get('[data-testid="ollama-cloud-refresh"]').trigger('click')
    await flushPromises()

    expect(api.refreshOllamaCloudUsage).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-testid="ollama-cloud-usage-details"]').text()).toContain('pro')
    expect(notifications.showSuccess).toHaveBeenCalled()
  })

  // A manual refresh fails with a structured rate-limit response from the server. The
  // editor must display the translated retry interval supplied by that response in error
  // feedback.
  it('shows the structured manual refresh limit from the edit settings', async () => {
    api.refreshOllamaCloudUsage.mockRejectedValueOnce({
      status: 429,
      reason: 'OLLAMA_CLOUD_USAGE_REFRESH_RATE_LIMITED',
      metadata: { retry_after_seconds: '18' },
    })
    const wrapper = mount(OllamaCloudUsageSettings, {
      props: { provider: provider(detailedState()) },
    })

    await wrapper.get('[data-testid="ollama-cloud-refresh"]').trigger('click')
    await flushPromises()

    expect(notifications.showError).toHaveBeenCalledWith('retry in 18 seconds')
  })
})
