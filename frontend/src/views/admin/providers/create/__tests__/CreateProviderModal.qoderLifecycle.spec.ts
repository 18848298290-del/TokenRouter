import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import CreateProviderModal from '../CreateProviderModal.vue'

// CreateProviderModal.qoderLifecycle.spec.ts exercises authorization completion
// while polling, manual exchange, and component disposal overlap. Deferred API
// responses make completion order observable through provider creation calls and
// emitted events, with popup cleanup checked when the component releases ownership.

// api isolates provider creation and Qoder transport calls while leaving the production authorization composable active.
const api = vi.hoisted(() => ({
  create: vi.fn(),
  generateAuthUrl: vi.fn(),
  exchangeCode: vi.fn(),
  poll: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: { create: api.create, checkMixedChannelRisk: vi.fn() },
    settings: {
      getSettings: vi.fn().mockResolvedValue({ provider_quota_notify_enabled: false }),
      getWebSearchEmulationConfig: vi.fn().mockResolvedValue({ enabled: false, providers: [] }),
    },
    tlsFingerprintProfiles: { list: vi.fn().mockResolvedValue([]) },
    tlsFingerprintRouters: { list: vi.fn().mockResolvedValue([]) },
    qoder: api,
  },
}))
vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
    showWarning: vi.fn(),
  }),
}))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))

// OAuthFlow supplies the public child input fields consumed by the parent's manual exchange action during these races.
const OAuthFlow = defineComponent({
  emits: ['generate-url'],
  data: () => ({ authCode: 'code', oauthState: 'state', inputMethod: 'manual' }),
  methods: {
    // reset clears the child inputs when the parent leaves authorization.
    reset() {
      this.authCode = ''
    },
  },
  template: '<button data-test="generate" @click="$emit(\'generate-url\')">generate</button>',
})

// tokenInfo represents one successful Qoder exchange result reused by poll and manual-completion requests in each case.
const tokenInfo = { security_oauth_token: 'access-token', machine_id: 'machine', site: 'global' }

// deferred keeps an API response pending until the test releases it.
// Callers resolve the promise after the competing user action, then flush Vue's
// pending work before inspecting provider creation or emitted completion events.
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}

// openFlow mounts creation and enters the named Qoder authorization session.
// The returned wrapper owns its mounted component until automatic test cleanup;
// callers may unmount earlier to exercise disposal while transport responses remain pending.
async function openFlow() {
  const wrapper = mount(CreateProviderModal, {
    props: { show: true, proxies: [], groups: [] },
    global: {
      stubs: {
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        OAuthAuthorizationFlow: OAuthFlow,
        QuotaLimitCard: true,
        ModelWhitelistSelector: true,
      },
    },
  })
  await wrapper.get('[data-testid="create-provider-platform-qoder"]').trigger('click')
  await flushPromises()
  await wrapper.get('input[type="text"][required]').setValue('Qoder lifecycle')
  await wrapper.get('form#create-provider-form').trigger('submit.prevent')
  await flushPromises()
  await wrapper.get('[data-test="generate"]').trigger('click')
  await flushPromises()
  return wrapper
}

// exchange submits the manual completion action through the rendered footer.
// The click starts its API request; callers settle their deferred responses
// separately so tests can control which pending authorization operation completes first.
async function exchange(wrapper: Awaited<ReturnType<typeof openFlow>>) {
  const button = wrapper
    .findAll('button')
    .find((item) => item.text().includes('admin.providers.oauth.completeAuth'))
  expect(button).toBeDefined()
  await button!.trigger('click')
  await flushPromises()
}

enableAutoUnmount(afterEach)
useProtocolCatalogFixture()

describe('Qoder authorization lifecycle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    Object.values(api).forEach((mock) => mock.mockReset())
    api.create.mockResolvedValue({})
    api.generateAuthUrl.mockResolvedValue({
      auth_url: 'https://qoder.example/device',
      session_id: 'session',
      state: 'state',
      expires_in: 600,
      interval: 2,
    })
    api.poll.mockResolvedValue({ status: 'pending' })
    api.exchangeCode.mockResolvedValue(tokenInfo)
    vi.spyOn(window, 'open').mockReturnValue(null)
  })

  afterEach(() => {
    vi.restoreAllMocks()
    vi.useRealTimers()
  })

  // A current completed poll creates its provider and emits completion once.
  // This positive case proves the transport fixture reaches the production authorization path used by stale-response tests.
  it('accepts a completed poll belonging to the active flow', async () => {
    api.poll.mockResolvedValueOnce({ status: 'completed', token_info: tokenInfo })
    const wrapper = await openFlow()
    expect(api.create).toHaveBeenCalledTimes(1)
    expect(api.create.mock.calls[0]?.[0].credentials.security_oauth_token).toBe('access-token')
    expect(wrapper.emitted('created')).toHaveLength(1)
  })

  // A running poll keeps manual completion disabled until its response arrives.
  // After a pending response releases the button, manual exchange owns completion and suspends further polling until its token is applied.
  it('serializes manual completion after an in-flight poll', async () => {
    const poll = deferred<{ status: string }>()
    const manual = deferred<typeof tokenInfo>()
    api.poll.mockReturnValueOnce(poll.promise)
    api.exchangeCode.mockReturnValueOnce(manual.promise)
    const wrapper = await openFlow()
    const verifying = wrapper
      .findAll('button')
      .find((item) => item.text().includes('admin.providers.oauth.verifying'))
    expect(verifying).toBeDefined()
    expect(verifying!.attributes('disabled')).toBeDefined()
    await verifying!.trigger('click')
    expect(api.exchangeCode).not.toHaveBeenCalled()
    poll.resolve({ status: 'pending' })
    await flushPromises()
    await exchange(wrapper)
    expect(api.exchangeCode).toHaveBeenCalledTimes(1)

    // Advancing beyond the server interval exposes accidental poll restart while manual exchange still owns the pending credentials.
    await vi.advanceTimersByTimeAsync(6000)
    expect(api.poll).toHaveBeenCalledTimes(1)
    expect(api.create).not.toHaveBeenCalled()
    manual.resolve({ ...tokenInfo, security_oauth_token: 'manual-token' })
    await flushPromises()
    expect(api.create).toHaveBeenCalledTimes(1)
    expect(api.create.mock.calls[0]?.[0].credentials.security_oauth_token).toBe('manual-token')
    expect(wrapper.emitted('created')).toHaveLength(1)
  })

  // Returning to the basic form and switching sites invalidates a pending global poll.
  // Its successful response cannot create a provider using credentials from the abandoned site.
  it('rejects a completed poll after the selected site changes', async () => {
    const poll = deferred<{ status: string; token_info: typeof tokenInfo }>()
    api.poll.mockReturnValueOnce(poll.promise)
    const wrapper = await openFlow()
    const back = wrapper.findAll('button').find((item) => item.text().includes('common.back'))
    expect(back).toBeDefined()
    await back!.trigger('click')
    await wrapper.get('[data-testid="create-qoder-site-cn"]').trigger('click')
    poll.resolve({ status: 'completed', token_info: tokenInfo })
    await flushPromises()
    expect(api.poll).toHaveBeenCalledTimes(1)
    expect(api.create).not.toHaveBeenCalled()
    expect(wrapper.emitted('created')).toBeUndefined()
  })

  // Unmounting a waiting flow closes its owned popup and invalidates pending polling.
  // A late successful response cannot create a provider or schedule another poll after disposal.
  it('releases the popup and rejects pending poll completion on unmount', async () => {
    const poll = deferred<{ status: string; token_info: typeof tokenInfo }>()
    api.poll.mockReturnValueOnce(poll.promise)
    const popup = { close: vi.fn(), focus: vi.fn(), location: { href: '' } }
    vi.mocked(window.open).mockReturnValueOnce(popup as unknown as Window)
    const wrapper = await openFlow()
    expect(popup.location.href).toBe('https://qoder.example/device')
    wrapper.unmount()
    expect(popup.close).toHaveBeenCalledTimes(1)
    poll.resolve({ status: 'completed', token_info: tokenInfo })
    await flushPromises()
    await vi.advanceTimersByTimeAsync(6000)
    expect(api.create).not.toHaveBeenCalled()
    expect(api.poll).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')).toBeUndefined()
  })

  // Manual exchange can remain pending when the user leaves the creation component.
  // Resolving that request after unmount cannot publish credentials or emit provider-created completion to its former parent.
  it('rejects pending manual exchange completion after unmount', async () => {
    const manual = deferred<typeof tokenInfo>()
    api.exchangeCode.mockReturnValueOnce(manual.promise)
    const wrapper = await openFlow()
    await exchange(wrapper)
    expect(api.exchangeCode).toHaveBeenCalledTimes(1)
    wrapper.unmount()
    manual.resolve(tokenInfo)
    await flushPromises()
    expect(api.create).not.toHaveBeenCalled()
    expect(wrapper.emitted('created')).toBeUndefined()
  })
})
