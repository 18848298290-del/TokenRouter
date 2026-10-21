import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import EditProviderModal from '../EditProviderModal.vue'

// EditProviderModal.grokUpstream.spec.ts exercises Grok OAuth forwarding controls
// through the mounted provider form. Endpoint presets, header overrides, and
// client-tool caching have distinct defaults, so submitted API patches verify
// that saved values and explicit opt-outs survive unrelated edits.

// These API replacements capture submitted updates and isolate the modal from
// network calls while retaining the provider form's asynchronous save lifecycle.
const { updateProviderMock, checkMixedChannelRiskMock } = vi.hoisted(() => ({
  updateProviderMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      update: updateProviderMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock,
    },
    settings: {
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
      getSettings: vi.fn().mockResolvedValue({}),
    },
    tlsFingerprintProfiles: {
      list: vi.fn().mockResolvedValue([]),
    },
  },
}))

vi.mock('@/api/admin/providers', () => ({
  getAntigravityDefaultModelMapping: vi.fn(),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// BaseDialogStub renders the form and footer slots only while show is true. This
// reproduces reopen behavior while avoiding the production dialog transition and teleport
// lifecycle in form tests.
const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    // show hides the stub slots when false; its false default keeps omitted visibility from
    // exposing dialog controls.
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

// buildGrokOAuthProvider builds a masked Grok OAuth response for forwarding tests.
// Supplied credential fields override its nonsecret defaults, and extra fields remain
// caller-controlled so each cache policy can be tested.
function buildGrokOAuthProvider(
  credentials: Record<string, unknown> = {},
  extra: Record<string, unknown> = {},
) {
  return {
    id: 5,
    name: 'Grok OAuth',
    notes: '',
    platform: 'grok',
    type: 'oauth',
    credentials: {
      expires_at: '2027-01-01T00:00:00Z',
      token_type: 'Bearer',
      ...credentials,
    },
    credentials_status: { has_access_token: true, has_refresh_token: true },
    extra,
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

// mountModal mounts the Grok editor with its real forwarding controls. Unrelated selectors
// are stubbed so endpoint, header, and cache interactions exercise the component without
// loading external catalogs.
function mountModal(provider: any) {
  return mount(EditProviderModal, {
    props: {
      show: true,
      provider,
      proxies: [],
      groups: [],
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: true,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: true,
      },
    },
  })
}

describe('EditProviderModal Grok OAuth upstream config', () => {
  beforeEach(() => {
    updateProviderMock.mockReset()
    checkMixedChannelRiskMock.mockReset()
    checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
  })

  // A Grok OAuth provider initially uses its default CLI gateway. Enabling custom forwarding
  // and entering a relay writes that address into the submitted credentials.
  it('enabling the custom base URL toggle and saving persists base_url', async () => {
    const provider = buildGrokOAuthProvider({ base_url: 'https://cli-chat-proxy.grok.com/v1' })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)
    const toggle = wrapper.get('[data-testid="grok-custom-base-url-toggle"]')
    await toggle.trigger('click')

    const input = wrapper.get('[data-testid="grok-custom-base-url-input"]')
    await input.setValue('https://my-relay.example.com')

    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.base_url).toBe('https://my-relay.example.com')
  })

  // A Grok OAuth administrator selects the public xAI API host manually. The custom endpoint
  // validator accepts the HTTP URL and persists it as the forwarding address.
  it('accepts the official API host as a manual endpoint switch and persists it', async () => {
    const provider = buildGrokOAuthProvider({ base_url: 'https://cli-chat-proxy.grok.com/v1' })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)

    await wrapper.get('[data-testid="grok-custom-base-url-toggle"]').trigger('click')
    await wrapper.get('[data-testid="grok-custom-base-url-input"]').setValue('https://api.x.ai/v1')

    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.base_url).toBe('https://api.x.ai/v1')
  })

  // A saved regional xAI endpoint differs from the default CLI gateway. Hydration enables
  // the custom endpoint control and displays the stored address for editing.
  it('echoes a stored official API endpoint with the toggle on', async () => {
    const provider = buildGrokOAuthProvider({ base_url: 'https://us-west-2.api.x.ai/v1' })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)

    const input = wrapper.get('[data-testid="grok-custom-base-url-input"]')
    expect((input.element as HTMLInputElement).value).toBe('https://us-west-2.api.x.ai/v1')
  })

  // A Grok administrator enables custom forwarding and chooses a preset chip. The endpoint
  // input receives the selected official API address without requiring manual entry.
  it('fills the input from an endpoint preset chip', async () => {
    const provider = buildGrokOAuthProvider({ base_url: 'https://cli-chat-proxy.grok.com/v1' })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)

    await wrapper.get('[data-testid="grok-custom-base-url-toggle"]').trigger('click')
    const presets = wrapper.findAll('[data-testid="grok-base-url-preset"]')
    expect(presets.length).toBe(5)
    await presets[1].trigger('click')
    const input = wrapper.get('[data-testid="grok-custom-base-url-input"]')
    expect((input.element as HTMLInputElement).value).toBe('https://api.x.ai/v1')
  })

  // A Grok provider opens with a saved custom relay address. The enabled input displays that
  // address and an untouched submission keeps it in credentials.
  it('loads an existing custom base_url with the toggle on and keeps it on save', async () => {
    const provider = buildGrokOAuthProvider({ base_url: 'https://my-relay.example.com' })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)

    const input = wrapper.get('[data-testid="grok-custom-base-url-input"]')
    expect((input.element as HTMLInputElement).value).toBe('https://my-relay.example.com')

    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.base_url).toBe('https://my-relay.example.com')
  })

  // Stored Grok credentials include several outbound header overrides. Saving without
  // touching that editor retains the enabled flag and every configured header value in the
  // submitted copy.
  it('keeps stored header overrides intact on an untouched save', async () => {
    const provider = buildGrokOAuthProvider({
      header_override_enabled: true,
      header_overrides: {
        'user-agent': 'grok-pager/0.2.93',
        'x-grok-client-identifier': 'grok-pager',
        'x-grok-client-version': '0.2.93',
        'x-xai-token-auth': 'xai-grok-cli',
      },
    })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)

    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.credentials?.header_override_enabled).toBe(true)
    expect(payload?.credentials?.header_overrides).toEqual({
      'user-agent': 'grok-pager/0.2.93',
      'x-grok-client-identifier': 'grok-pager',
      'x-grok-client-version': '0.2.93',
      'x-xai-token-auth': 'xai-grok-cli',
    })
  })

  // Client-tool caching is an OAuth-specific Grok setting. The mounted control appears for
  // Grok OAuth but stays absent for Grok API keys and OpenAI OAuth.
  it('shows the client-tool cache switch only for Grok OAuth providers', () => {
    const grokOAuthWrapper = mountModal(buildGrokOAuthProvider())
    expect(grokOAuthWrapper.find('[data-testid="grok-client-tool-cache-toggle"]').exists()).toBe(
      true,
    )

    const grokAPIKeyWrapper = mountModal({
      ...buildGrokOAuthProvider(),
      type: 'apikey',
      credentials: { api_key: 'xai-test', base_url: 'https://api.x.ai/v1' },
    })
    expect(grokAPIKeyWrapper.find('[data-testid="grok-client-tool-cache-toggle"]').exists()).toBe(
      false,
    )

    const openAIOAuthWrapper = mountModal({
      ...buildGrokOAuthProvider(),
      platform: 'openai',
    })
    expect(openAIOAuthWrapper.find('[data-testid="grok-client-tool-cache-toggle"]').exists()).toBe(
      false,
    )
  })

  // A Grok provider has enabled client-tool caching and unrelated extra fields. Disabling
  // the toggle writes explicit false while preserving those other fields in the update.
  it('loads and disables client-tool caching while preserving unrelated extra fields', async () => {
    const provider = buildGrokOAuthProvider(
      {},
      {
        grok_client_tool_cache_enabled: true,
        custom_setting: 'keep-me',
      },
    )
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)
    const toggle = wrapper.get('[data-testid="grok-client-tool-cache-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('true')

    await toggle.trigger('click')
    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.extra?.custom_setting).toBe('keep-me')
    expect(payload?.extra?.grok_client_tool_cache_enabled).toBe(false)
  })

  // Grok cache configuration is missing from a provider's extras. The form treats absence as
  // enabled and persists explicit true while retaining unrelated saved values.
  it('defaults client-tool caching on when the setting is missing and persists explicit true', async () => {
    const provider = buildGrokOAuthProvider({}, { custom_setting: 'keep-me' })
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)
    const toggle = wrapper.get('[data-testid="grok-client-tool-cache-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('true')

    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.extra).toMatchObject({
      grok_client_tool_cache_enabled: true,
      custom_setting: 'keep-me',
    })
  })

  // A Grok provider has explicitly opted out of client-tool caching. Opening and saving the
  // unchanged form keeps false instead of restoring the missing-field default.
  it('keeps an explicit false opt-out when saving an untouched provider', async () => {
    const provider = buildGrokOAuthProvider(
      {},
      {
        grok_client_tool_cache_enabled: false,
        custom_setting: 'keep-me',
      },
    )
    updateProviderMock.mockResolvedValue(provider)

    const wrapper = mountModal(provider)
    const toggle = wrapper.get('[data-testid="grok-client-tool-cache-toggle"]')
    expect(toggle.attributes('aria-checked')).toBe('false')

    await wrapper.get('form#edit-provider-form').trigger('submit.prevent')
    await vi.waitFor(() => expect(updateProviderMock).toHaveBeenCalledTimes(1))

    const payload = updateProviderMock.mock.calls[0]?.[1]
    expect(payload?.extra).toMatchObject({
      grok_client_tool_cache_enabled: false,
      custom_setting: 'keep-me',
    })
  })

  // A malformed Grok cache setting contains the string true instead of a boolean. Hydration
  // displays the switch as disabled to match the backend's accepted value rules.
  it('shows malformed cache settings as disabled so the UI matches the fail-closed backend', () => {
    const provider = buildGrokOAuthProvider(
      {},
      {
        grok_client_tool_cache_enabled: 'true',
        custom_setting: 'keep-me',
      },
    )

    const wrapper = mountModal(provider)
    expect(
      wrapper.get('[data-testid="grok-client-tool-cache-toggle"]').attributes('aria-checked'),
    ).toBe('false')
  })
})

useProtocolCatalogFixture()
