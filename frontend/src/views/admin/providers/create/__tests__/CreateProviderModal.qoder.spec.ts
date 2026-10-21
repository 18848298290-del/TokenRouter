import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import CreateProviderModal from '../CreateProviderModal.vue'

// CreateProviderModal.qoder.spec.ts exercises manual and OAuth Qoder creation
// through rendered dialog controls. Deferred requests preserve competing user
// actions so assertions can protect site identity, popup ownership, explicit
// model-policy edits, retry availability, and creation locking.

// Hoisted API spies isolate transport side effects and retain each submitted
// payload for assertions; beforeEach restores defaults between independent provider scenarios.
const {
  createProviderMock,
  generateQoderAuthUrlMock,
  exchangeQoderCodeMock,
  pollQoderAuthMock,
  getSettingsMock,
  getWebSearchEmulationConfigMock,
  listTLSProfilesMock,
  listTLSRoutersMock,
} = vi.hoisted(() => ({
  createProviderMock: vi.fn(),
  generateQoderAuthUrlMock: vi.fn(),
  exchangeQoderCodeMock: vi.fn(),
  pollQoderAuthMock: vi.fn(),
  getSettingsMock: vi.fn(),
  getWebSearchEmulationConfigMock: vi.fn(),
  listTLSProfilesMock: vi.fn(),
  listTLSRoutersMock: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      create: createProviderMock,
      checkMixedChannelRisk: vi.fn(),
    },
    settings: {
      getSettings: getSettingsMock,
      getWebSearchEmulationConfig: getWebSearchEmulationConfigMock,
    },
    tlsFingerprintProfiles: {
      list: listTLSProfilesMock,
    },
    tlsFingerprintRouters: {
      list: listTLSRoutersMock,
    },
    qoder: {
      generateAuthUrl: generateQoderAuthUrlMock,
      exchangeCode: exchangeQoderCodeMock,
      poll: pollQoderAuthMock,
    },
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

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// BaseDialogStub renders conditional dialog content and footer slots so tests
// can drive the same parent visibility and completion actions.
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
  emits: ['close'],
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

// ModelWhitelistSelectorStub emits explicit model selections from visible test
// buttons, allowing payload assertions to distinguish untouched state from
// edited or cleared policy.
const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    // modelValue receives the parent selection and defaults to an empty array; emitted
    // replacements are applied by the parent.
    modelValue: {
      type: Array,
      default: () => [],
    },
    // models supplies candidate choices from the parent; omission leaves an empty list for
    // this selector stub.
    models: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        type="button"
        data-testid="rewrite-to-qoder-defaults"
        @click="$emit('update:modelValue', ['claude-opus-4-6', 'auto'])"
      >
        rewrite qoder
      </button>
      <span data-testid="model-whitelist-value">
        {{ Array.isArray(modelValue) ? modelValue.join(',') : '' }}
      </span>
    </div>
  `,
})

// OAuthAuthorizationFlowStub exposes the authorization draft and public events
// consumed by the parent, keeping transport behavior in the production
// controller under test.
const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  props: {
    authUrl: { type: String, default: '' }, // current authorization URL
    sessionId: { type: String, default: '' }, // current session identity
    loading: { type: Boolean, default: false }, // active authorization operation
  },
  emits: ['generate-url'],
  data: () => ({
    authCode: 'http://localhost:1455/callback?code=code&state=state-value',
    oauthState: '',
    inputMethod: 'manual',
  }),
  methods: {
    // reset clears the child authorization code when the parent leaves authorization.
    reset() {
      this.authCode = ''
      this.oauthState = ''
      this.inputMethod = 'manual'
    },
  },
  template: `
    <div>
      <button type="button" data-testid="generate-qoder-auth-url" @click="$emit('generate-url')">
        generate
      </button>
      <span data-testid="qoder-oauth-session">{{ sessionId }}</span>
    </div>
  `,
})

// mountModal creates a dialog with isolated administrator APIs and shared
// control stubs. Callers drive its rendered form and own unmount cleanup after
// inspecting payloads or pending authorization behavior.
function mountModal() {
  return mount(CreateProviderModal, {
    props: {
      show: true,
      proxies: [],
      groups: [],
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        Select: true,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
        QuotaLimitCard: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub,
      },
    },
  })
}

// createDeferred exposes a promise resolver so tests control API settlement
// order. Resolve it after the competing user action, then flush pending Vue work
// before inspecting persistence and emitted events.
function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

// openQoderOAuthStep configures Qoder and advances the supplied wrapper to
// authorization. It leaves URL generation to the test so deferred generation and
// popup ownership can be arranged before requests start.
async function openQoderOAuthStep(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('[data-testid="create-provider-platform-qoder"]').trigger('click')
  await flushPromises()

  const nameInput = wrapper
    .findAll('input')
    .find(
      (input) => input.attributes('type') === 'text' && input.attributes('required') !== undefined,
    )
  expect(nameInput).toBeTruthy()
  await nameInput!.setValue('Qoder OAuth')
  await wrapper.get('form#create-provider-form').trigger('submit.prevent')
  await flushPromises()
}

// findButtonByText locates a rendered button by its visible translated label.
function findButtonByText(wrapper: ReturnType<typeof mountModal>, text: string) {
  const button = wrapper.findAll('button').find((candidate) => candidate.text().includes(text))
  expect(button).toBeTruthy()
  return button!
}

// fillQoderManualForm selects manual Qoder credentials and populates a valid
// default draft. Tests may alter site or policy afterward before submitting
// through the same rendered form entry point.
async function fillQoderManualForm(wrapper: ReturnType<typeof mountModal>) {
  await wrapper.get('[data-testid="create-provider-platform-qoder"]').trigger('click')
  await flushPromises()

  const inputs = wrapper.findAll('input')
  const nameInput = inputs.find(
    (input) => input.attributes('type') === 'text' && input.attributes('required') !== undefined,
  )
  expect(nameInput).toBeTruthy()
  await nameInput!.setValue('Qoder COSY')

  const manualButton = wrapper
    .findAll('button')
    .find((button) => button.text().includes('admin.providers.qoder.providerType.manualTitle'))
  expect(manualButton).toBeTruthy()
  await manualButton!.trigger('click')
  await flushPromises()

  const visibleInputs = wrapper.findAll('input')
  await visibleInputs
    .find((input) => input.attributes('placeholder') === 'dt-...')!
    .setValue('token')
  await visibleInputs
    .find((input) => input.attributes('placeholder') === 'machine_id')!
    .setValue('machine')
  await visibleInputs
    .find((input) => input.attributes('placeholder') === 'uid or aid')!
    .setValue('uid')
}

describe('CreateProviderModal Qoder model restriction', () => {
  beforeEach(() => {
    createProviderMock.mockReset()
    getSettingsMock.mockReset()
    getWebSearchEmulationConfigMock.mockReset()
    listTLSProfilesMock.mockReset()
    listTLSRoutersMock.mockReset()
    generateQoderAuthUrlMock.mockReset()
    exchangeQoderCodeMock.mockReset()
    pollQoderAuthMock.mockReset()

    createProviderMock.mockResolvedValue({})
    getSettingsMock.mockResolvedValue({ provider_quota_notify_enabled: false })
    getWebSearchEmulationConfigMock.mockResolvedValue({ enabled: false, providers: [] })
    listTLSProfilesMock.mockResolvedValue([])
    listTLSRoutersMock.mockResolvedValue([])
    pollQoderAuthMock.mockResolvedValue({ status: 'pending' })
    vi.spyOn(window, 'open').mockReturnValue(null)
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // The manual Qoder form displays generated mapping defaults without
  // administrator edits. Creation must omit both mapping and whitelist fields
  // so generated display state does not become persisted policy.
  it('does not persist generated Qoder model mappings on default manual create', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    const payload = createProviderMock.mock.calls[0]?.[0]
    expect(payload.platform).toBe('qoder')
    expect(payload.type).toBe('cosy')
    expect(payload.credentials.model_mapping).toBeUndefined()
    expect(payload.credentials.model_whitelist).toBeUndefined()
    expect(payload.credentials.site).toBe('global')
    expect(payload.credentials.refresh_mode).toBe('cosy')
  })

  // A manual Qoder draft supplies only a bootstrap PAT and no token fields.
  // The creation request must accept that credential path and preserve its
  // expected platform type.
  it('creates Qoder manual provider with PAT bootstrap without token fields', async () => {
    const wrapper = mountModal()
    await wrapper.get('[data-testid="create-provider-platform-qoder"]').trigger('click')
    await flushPromises()

    const inputs = wrapper.findAll('input')
    const nameInput = inputs.find(
      (input) => input.attributes('type') === 'text' && input.attributes('required') !== undefined,
    )
    expect(nameInput).toBeTruthy()
    await nameInput!.setValue('Qoder PAT')

    const manualButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.providers.qoder.providerType.manualTitle'))
    expect(manualButton).toBeTruthy()
    await manualButton!.trigger('click')
    await flushPromises()

    const patInput = wrapper
      .findAll('input')
      .find((input) => input.attributes('placeholder') === 'pat-...')
    expect(patInput).toBeTruthy()
    await patInput!.setValue('pat-123')

    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    const payload = createProviderMock.mock.calls[0]?.[0]
    expect(payload.platform).toBe('qoder')
    expect(payload.type).toBe('cosy')
    expect(payload.credentials).toMatchObject({ pat: 'pat-123' })
    expect(payload.credentials.site).toBe('global')
    expect(payload.credentials.refresh_mode).toBe('cosy')
    expect(payload.credentials.security_oauth_token).toBeUndefined()
    expect(payload.credentials.machine_id).toBeUndefined()
  })

  // The manual draft changes from the global Qoder site to China. Entered
  // credentials must remain intact while the persisted site reflects the new
  // selection.
  it('switches to the China site while preserving manual credentials', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)
    await wrapper.get('[data-testid="create-qoder-site-cn"]').trigger('click')
    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await flushPromises()

    const payload = createProviderMock.mock.calls[0]?.[0]
    expect(payload.credentials).toMatchObject({
      site: 'cn',
      refresh_mode: 'cosy',
      security_oauth_token: 'token',
      machine_id: 'machine',
    })
  })

  // Manual Qoder persistence remains pending after submission. Site selection
  // must stay locked until settlement so the response belongs to the same
  // configured credential site.
  it('locks the selected site while a manual provider creation request is pending', async () => {
    const deferred = createDeferred<Record<string, never>>()
    createProviderMock.mockReturnValueOnce(deferred.promise)
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await flushPromises()

    const globalButton = wrapper.get('[data-testid="create-qoder-site-global"]')
    const cnButton = wrapper.get('[data-testid="create-qoder-site-cn"]')
    expect(globalButton.attributes('disabled')).toBeDefined()
    expect(cnButton.attributes('disabled')).toBeDefined()
    await cnButton.trigger('click')
    expect(globalButton.attributes('aria-checked')).toBe('true')
    expect(createProviderMock).toHaveBeenCalledTimes(1)

    deferred.resolve({})
    await flushPromises()
    wrapper.unmount()
  })

  // Manual Qoder creation enables TLS fingerprinting without selecting a profile.
  // The submitted Extra contains the enabled flag alone, excluding both profile
  // and OpenAI router identifiers.
  it('persists Qoder TLS fingerprint settings on manual create without OpenAI router', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    await wrapper.get('[data-testid="create-qoder-tls-fingerprint-toggle"]').trigger('click')
    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    const payload = createProviderMock.mock.calls[0]?.[0]
    expect(payload.extra).toEqual({
      enable_tls_fingerprint: true,
    })
  })

  // The administrator explicitly edits Qoder mapping rows before manual
  // creation. Credentials must persist those mappings and an independent
  // whitelist rather than treating edited values as generated defaults.
  it('persists Qoder model_mapping after explicit mapping edit on manual create', async () => {
    const wrapper = mountModal()
    await fillQoderManualForm(wrapper)

    const addMappingButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.providers.addMapping'))
    expect(addMappingButton).toBeTruthy()
    await addMappingButton!.trigger('click')

    const requestInput = wrapper
      .findAll('input')
      .find((input) => input.attributes('placeholder') === 'admin.providers.requestModel')
    const targetInput = wrapper
      .findAll('input')
      .find((input) => input.attributes('placeholder') === 'admin.providers.actualModel')
    expect(requestInput).toBeTruthy()
    expect(targetInput).toBeTruthy()
    await requestInput!.setValue('glm-5.2')
    await targetInput!.setValue('gm51model')

    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    const payload = createProviderMock.mock.calls[0]?.[0]
    expect(payload.credentials.model_mapping).toEqual({ 'glm-5.2': 'gm51model' })
    expect(payload.credentials.model_whitelist).toEqual([])
  })

  // A generated Qoder session is abandoned before another site is selected.
  // Returning to authorization must not reuse the old site-bound session or
  // its callback state.
  it('discards a generated OAuth session after returning and switching sites', async () => {
    const deferred = createDeferred<{
      auth_url: string // browser authorization URL
      // session_id and state belong to the same generated authorization; resolving
      // this deferred response publishes the pair only while its request remains current.
      session_id: string // backend authorization identity
      state: string // callback correlation value
      expires_in: number // session lifetime seconds
      interval: number // poll interval seconds
    }>()
    generateQoderAuthUrlMock.mockReturnValueOnce(deferred.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    expect(generateQoderAuthUrlMock).toHaveBeenCalledWith({ site: 'global' })

    await findButtonByText(wrapper, 'common.back').trigger('click')
    await wrapper.get('[data-testid="create-qoder-site-cn"]').trigger('click')
    deferred.resolve({
      auth_url: 'https://qoder.com/old-session',
      session_id: 'old-session',
      state: 'old-state',
      expires_in: 600,
      interval: 2,
    })
    await flushPromises()

    expect(pollQoderAuthMock).not.toHaveBeenCalled()
    expect(createProviderMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // A Qoder code exchange is delayed while the administrator changes sites.
  // Its obsolete completion must not create a provider or publish success into
  // the replacement flow.
  it('does not create a provider when an exchange completes after switching sites', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2,
    })
    const deferred = createDeferred<{
      // Token, machine, and site fields identify the account returned by the delayed
      // exchange; the controller rejects them when the captured authorization is obsolete.
      security_oauth_token: string // authorized account token
      machine_id: string // authorized machine identifier
      site: 'global' // authorized account site
    }>()
    exchangeQoderCodeMock.mockReturnValueOnce(deferred.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(1)

    await findButtonByText(wrapper, 'common.back').trigger('click')
    await wrapper.get('[data-testid="create-qoder-site-cn"]').trigger('click')
    deferred.resolve({
      security_oauth_token: 'old-global-token',
      machine_id: 'old-machine',
      site: 'global',
    })
    await flushPromises()

    expect(createProviderMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // Token exchange succeeds while provider persistence remains unresolved.
  // Site controls and duplicate completion must remain disabled until the
  // existing creation request releases its ownership.
  it('keeps site switching and duplicate exchange disabled while provider creation is pending', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2,
    })
    exchangeQoderCodeMock.mockResolvedValue({
      security_oauth_token: 'global-token',
      machine_id: 'global-machine',
      site: 'global',
    })
    const createDeferredRequest = createDeferred<Record<string, never>>()
    createProviderMock.mockReturnValueOnce(createDeferredRequest.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    const backButton = findButtonByText(wrapper, 'common.back')
    expect(backButton.attributes('disabled')).toBeDefined()
    await backButton.trigger('click')
    expect(wrapper.find('[data-testid="create-qoder-site-cn"]').exists()).toBe(false)

    const exchangeButton = findButtonByText(wrapper, 'admin.providers.oauth.verifying')
    expect(exchangeButton.attributes('disabled')).toBeDefined()
    await exchangeButton.trigger('click')
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(1)
    expect(createProviderMock).toHaveBeenCalledTimes(1)

    createDeferredRequest.resolve({})
    await flushPromises()
    wrapper.unmount()
  })

  // The parent dialog requests closure while Qoder persistence is pending. The
  // creation flow must wait for success before publishing its refresh and
  // close events.
  it('ignores dialog close events until a pending Qoder provider creation succeeds', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2,
    })
    exchangeQoderCodeMock.mockResolvedValueOnce({
      security_oauth_token: 'global-token',
      machine_id: 'global-machine',
      site: 'global',
    })
    const createDeferredRequest = createDeferred<Record<string, never>>()
    createProviderMock.mockReturnValueOnce(createDeferredRequest.promise)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    expect(wrapper.getComponent(OAuthAuthorizationFlowStub).props('loading')).toBe(true)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    expect(generateQoderAuthUrlMock).toHaveBeenCalledTimes(1)
    wrapper.findAllComponents(BaseDialogStub)[0]!.vm.$emit('close')
    await flushPromises()
    expect(wrapper.emitted('close')).toBeUndefined()
    expect(wrapper.emitted('created')).toBeUndefined()

    createDeferredRequest.resolve({})
    await flushPromises()
    expect(wrapper.emitted('created')).toHaveLength(1)
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  // Qoder authorization succeeds but provider persistence fails. The same
  // session must remain available for a later manual retry after the creation
  // lock is released.
  it('keeps the current OAuth session retryable after provider creation fails', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2,
    })
    exchangeQoderCodeMock.mockResolvedValue({
      security_oauth_token: 'global-token',
      machine_id: 'global-machine',
      site: 'global',
    })
    createProviderMock.mockRejectedValueOnce(new Error('create failed')).mockResolvedValueOnce({})

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(createProviderMock).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')).toBeUndefined()
    expect(wrapper.get('[data-testid="qoder-oauth-session"]').text()).toBe('global-session')

    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    await flushPromises()
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(2)
    expect(createProviderMock).toHaveBeenCalledTimes(2)
    expect(wrapper.emitted('created')).toHaveLength(1)
    wrapper.unmount()
  })

  // Qoder token exchange succeeds while local creation rules reject the
  // payload. A corrected draft must retry within the current authorization
  // instead of remaining permanently locked.
  it('keeps the current OAuth session retryable after local creation validation fails', async () => {
    generateQoderAuthUrlMock.mockResolvedValueOnce({
      auth_url: 'https://qoder.com/device',
      session_id: 'global-session',
      state: 'state-value',
      expires_in: 600,
      interval: 2,
    })
    exchangeQoderCodeMock.mockResolvedValue({
      security_oauth_token: 'global-token',
      machine_id: 'global-machine',
      site: 'global',
    })

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    const setupState = (wrapper.vm as any).$?.setupState
    setupState.tempUnschedEnabled = true
    setupState.tempUnschedRules = []
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await flushPromises()
    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    await flushPromises()

    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(1)
    expect(createProviderMock).not.toHaveBeenCalled()
    expect(wrapper.emitted('created')).toBeUndefined()

    setupState.tempUnschedEnabled = false
    await findButtonByText(wrapper, 'admin.providers.oauth.completeAuth').trigger('click')
    await flushPromises()
    expect(exchangeQoderCodeMock).toHaveBeenCalledTimes(2)
    expect(createProviderMock).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('created')).toHaveLength(1)
    wrapper.unmount()
  })

  // Several authorization generations share a named browser window and resolve
  // out of order. Obsolete generation cleanup must not close the popup lease
  // currently owned by another flow.
  it('does not let an old generate request close a reused popup owned by a new flow', async () => {
    const firstGenerate = createDeferred<{
      auth_url: string // browser authorization URL
      // session_id and state belong to the same generated authorization; resolving
      // this deferred response publishes the pair only while its request remains current.
      session_id: string // backend authorization identity
      state: string // callback correlation value
      expires_in: number // session lifetime seconds
      interval: number // poll interval seconds
    }>()
    const secondGenerate = createDeferred<{
      auth_url: string // browser authorization URL
      // session_id and state belong to the same generated authorization; resolving
      // this deferred response publishes the pair only while its request remains current.
      session_id: string // backend authorization identity
      state: string // callback correlation value
      expires_in: number // session lifetime seconds
      interval: number // poll interval seconds
    }>()
    const thirdGenerate = createDeferred<{
      auth_url: string // browser authorization URL
      // session_id and state belong to the same generated authorization; resolving
      // this deferred response publishes the pair only while its request remains current.
      session_id: string // backend authorization identity
      state: string // callback correlation value
      expires_in: number // session lifetime seconds
      interval: number // poll interval seconds
    }>()
    generateQoderAuthUrlMock
      .mockReturnValueOnce(firstGenerate.promise)
      .mockReturnValueOnce(secondGenerate.promise)
      .mockReturnValueOnce(thirdGenerate.promise)

    const reusedPopup = {
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' },
    }
    const replacementPopup = {
      close: vi.fn(),
      focus: vi.fn(),
      location: { href: 'about:blank' },
    }
    vi.mocked(window.open)
      .mockReturnValueOnce(reusedPopup as unknown as Window)
      .mockReturnValueOnce(reusedPopup as unknown as Window)
      .mockReturnValueOnce(replacementPopup as unknown as Window)

    const wrapper = mountModal()
    await openQoderOAuthStep(wrapper)
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    await findButtonByText(wrapper, 'common.back').trigger('click')
    expect(reusedPopup.close).toHaveBeenCalledTimes(1)

    await wrapper.get('form#create-provider-form').trigger('submit.prevent')
    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    firstGenerate.resolve({
      auth_url: 'https://qoder.com/old-device',
      session_id: 'old-session',
      state: 'old-state',
      expires_in: 600,
      interval: 2,
    })
    await flushPromises()
    expect(reusedPopup.close).toHaveBeenCalledTimes(1)

    secondGenerate.resolve({
      auth_url: 'https://qoder.com/current-device',
      session_id: 'current-session',
      state: 'current-state',
      expires_in: 600,
      interval: 2,
    })
    await flushPromises()
    expect(reusedPopup.location.href).toBe('https://qoder.com/current-device')

    await wrapper.get('[data-testid="generate-qoder-auth-url"]').trigger('click')
    expect(reusedPopup.close).toHaveBeenCalledTimes(2)
    thirdGenerate.resolve({
      auth_url: 'https://qoder.com/replacement-device',
      session_id: 'replacement-session',
      state: 'replacement-state',
      expires_in: 600,
      interval: 2,
    })
    await flushPromises()
    expect(replacementPopup.location.href).toBe('https://qoder.com/replacement-device')

    wrapper.findAllComponents(BaseDialogStub)[0]!.vm.$emit('close')
    await flushPromises()
    expect(replacementPopup.close).toHaveBeenCalledTimes(1)
    wrapper.unmount()
  })
})

useProtocolCatalogFixture()
