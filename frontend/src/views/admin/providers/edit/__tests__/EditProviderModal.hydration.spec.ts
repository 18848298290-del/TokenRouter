import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import EditProviderModal from '../EditProviderModal.vue'

// EditProviderModal.hydration.spec.ts checks saved CN relay settings after Vue
// finishes provider hydration and its dependent watchers. Replacing the provider
// prop exercises a second hydration pass in the same dialog, protecting endpoint
// identity and masked-secret omission when the saved mode changes between providers.

// updateProvider captures the submitted patch while all supporting settings requests return local, deterministic configuration values.
const { updateProvider } = vi.hoisted(() => ({ updateProvider: vi.fn() }))
vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      update: updateProvider,
      checkMixedChannelRisk: vi.fn().mockResolvedValue({ has_risk: false }),
    },
    settings: {
      getSettings: vi.fn().mockResolvedValue({}),
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) },
  },
}))
vi.mock('@/api/admin/providers', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
  getUpstreamBalanceAlertStatus: vi.fn(),
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }),
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))

// provider builds a masked CN provider with custom endpoints for hydration.
// Each mode receives a distinct provider ID and relay URL, making any reset
// to defaults or leakage from the previously displayed provider observable in submission.
function provider(mode: 'payg' | 'coding') {
  const baseURL = `https://${mode}.relay.example/v1`
  return {
    id: mode === 'payg' ? 1 : 2,
    name: `GLM ${mode}`,
    notes: '',
    platform: 'zhipu',
    type: 'apikey',
    credentials: {
      provider_mode: mode,
      upstream_protocols: ['anthropic_messages', 'openai_chat_completions'],
      base_url: baseURL,
      api_base_urls: {
        chat_completions: baseURL,
        anthropic: `https://${mode}.relay.example/anthropic`,
      },
    },
    credentials_status: { has_api_key: true },
    extra: {},
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false,
  } as any
}

enableAutoUnmount(afterEach)
useProtocolCatalogFixture()

describe('provider form hydration across Vue watcher ticks', () => {
  beforeEach(() => {
    updateProvider.mockReset().mockResolvedValue({})
  })

  // A mounted CN form switches from one stored billing mode to another.
  // Saving after watcher ticks retains the second provider's custom endpoints and omits its masked API key from the patch.
  it.each(['payg', 'coding'] as const)(
    'hydrates %s endpoints after replacing the displayed provider',
    async (mode) => {
      const next = provider(mode)
      const wrapper = mount(EditProviderModal, {
        props: {
          show: true,
          provider: provider(mode === 'payg' ? 'coding' : 'payg'),
          proxies: [],
          groups: [],
        },
        global: {
          stubs: {
            BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
            Select: true,
            Icon: true,
            ProxySelector: true,
            GroupSelector: true,
            ModelWhitelistSelector: true,
          },
        },
      })
      await flushPromises()
      await wrapper.setProps({ provider: next })
      // Hydration guards remain active through the watcher flush, so delayed submission observes the values that survive dependent mode watchers.
      await nextTick()
      await flushPromises()
      await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
      await flushPromises()
      expect(updateProvider).toHaveBeenCalledTimes(1)
      expect(updateProvider.mock.calls[0]?.[0]).toBe(next.id)
      const patch = updateProvider.mock.calls[0]?.[1].credentials
      expect(patch).toMatchObject(next.credentials)
      expect(patch).not.toHaveProperty('api_key')
    },
  )
})
