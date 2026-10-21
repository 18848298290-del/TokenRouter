import { describe, expect, it, beforeEach, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import type { Provider } from '@/api/admin/providers.types'
import ReAuthProviderModal from '../ReAuthProviderModal.vue'

// ReAuthProviderModal.spec.ts checks OpenAI reauthorization with an existing
// TLS router binding and provider error. The authorization stub exposes a
// manual code while spies distinguish credential merging from full updates and
// separate error-clearing requests.

// Authorization spies separate code exchange, credential building, credential
// merging, full provider updates, and error clearing so the test can detect
// the wrong persistence route.
const {
  showSuccessMock,
  showErrorMock,
  updateProviderMock,
  clearErrorMock,
  applyOAuthCredentialsMock,
  exchangeAuthCodeMock,
  buildCredentialsMock,
  buildExtraInfoMock,
} = vi.hoisted(() => ({
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  updateProviderMock: vi.fn(),
  clearErrorMock: vi.fn(),
  applyOAuthCredentialsMock: vi.fn(),
  exchangeAuthCodeMock: vi.fn(),
  buildCredentialsMock: vi.fn(),
  buildExtraInfoMock: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: showSuccessMock,
    showError: showErrorMock,
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

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      update: updateProviderMock,
      clearError: clearErrorMock,
      applyOAuthCredentials: applyOAuthCredentialsMock,
    },
  },
}))

vi.mock('@/composables/useProviderOAuth', () => ({
  useProviderOAuth: () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    buildExtraInfo: vi.fn(),
  }),
}))

vi.mock('@/composables/useOpenAIOAuth', () => ({
  useOpenAIOAuth: () => ({
    authUrl: { value: 'https://auth.example.test' },
    sessionId: { value: 'session-1' },
    oauthState: { value: 'state-1' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: exchangeAuthCodeMock,
    buildCredentials: buildCredentialsMock,
    buildExtraInfo: buildExtraInfoMock,
  }),
}))

vi.mock('@/composables/useGeminiOAuth', () => ({
  useGeminiOAuth: () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    state: { value: '' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    buildCredentials: vi.fn(),
  }),
}))

vi.mock('@/composables/useAntigravityOAuth', () => ({
  useAntigravityOAuth: () => ({
    authUrl: { value: '' },
    sessionId: { value: '' },
    state: { value: '' },
    loading: { value: false },
    error: { value: '' },
    resetState: vi.fn(),
    generateAuthUrl: vi.fn(),
    exchangeAuthCode: vi.fn(),
    buildCredentials: vi.fn(),
  }),
}))

// BaseDialogStub renders content and footer slots only while shown, allowing
// the real reauthorization component to expose its exchange action.
const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    // show controls slot mounting in this stub and defaults to hidden until
    // the parent dialog opens the corresponding content or confirmation.
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

// OAuthAuthorizationFlowStub publishes fixed manual-code inputs through its
// exposed instance so the parent can execute exchange and credential merge
// handlers.
const OAuthAuthorizationFlowStub = defineComponent({
  name: 'OAuthAuthorizationFlow',
  // setup exposes the manual authorization values consumed through the parent
  // component ref. The fixed code and state allow the test to inspect exchange
  // arguments without performing an external authorization flow.
  setup(_, { expose }) {
    expose({
      authCode: 'auth-code-1',
      oauthState: 'state-1',
      projectId: '',
      sessionKey: '',
      inputMethod: 'manual',
      reset: vi.fn(),
    })
    return {}
  },
  template: '<div data-testid="oauth-flow" />',
})

// openAIProvider returns an errored OpenAI OAuth fixture with proxy and TLS
// router bindings. Each call creates its credential and extra objects so tests
// can compare the returned active provider with fresh input.
function openAIProvider(): Provider {
  return {
    id: 101,
    name: 'OpenAI OAuth',
    platform: 'openai',
    type: 'oauth',
    credentials: {},
    extra: {
      tls_fingerprint_router_id: 9,
    },
    proxy_id: 12,
    concurrency: 1,
    priority: 0,
    status: 'error',
    error_message: 'old error',
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    tls_fingerprint_router_id: 9,
  }
}

describe('admin/provider/ReAuthProviderModal', () => {
  beforeEach(() => {
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
    updateProviderMock.mockReset()
    clearErrorMock.mockReset()
    applyOAuthCredentialsMock.mockReset()
    exchangeAuthCodeMock.mockReset()
    buildCredentialsMock.mockReset()
    buildExtraInfoMock.mockReset()

    exchangeAuthCodeMock.mockResolvedValue({
      access_token: 'new-access-token',
      refresh_token: 'new-refresh-token',
      email: 'user@example.test',
    })
    buildCredentialsMock.mockReturnValue({
      access_token: 'new-access-token',
      refresh_token: 'new-refresh-token',
    })
    buildExtraInfoMock.mockReturnValue({
      email: 'user@example.test',
    })
    applyOAuthCredentialsMock.mockResolvedValue({
      ...openAIProvider(),
      status: 'active',
      error_message: null,
    })
  })

  // An errored OpenAI OAuth provider has proxy and TLS router bindings.
  // Reauthorization forwards those bindings during exchange, merges
  // credentials, and emits the returned active provider without separate
  // updates.
  it('OpenAI 重新授权使用增量合并接口保留 TLS Router 绑定', async () => {
    const wrapper = mount(ReAuthProviderModal, {
      props: {
        show: true,
        provider: openAIProvider(),
      },
      global: {
        stubs: {
          BaseDialog: BaseDialogStub,
          OAuthAuthorizationFlow: OAuthAuthorizationFlowStub,
          Icon: true,
        },
      },
    })
    await flushPromises()

    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    expect(exchangeAuthCodeMock).toHaveBeenCalledWith('auth-code-1', 'session-1', 'state-1', 12, 9)
    expect(applyOAuthCredentialsMock).toHaveBeenCalledWith(101, {
      type: 'oauth',
      credentials: {
        access_token: 'new-access-token',
        refresh_token: 'new-refresh-token',
      },
      extra: {
        email: 'user@example.test',
      },
    })
    expect(updateProviderMock).not.toHaveBeenCalled()
    expect(clearErrorMock).not.toHaveBeenCalled()
    expect(wrapper.emitted('reauthorized')?.[0]?.[0]).toMatchObject({
      id: 101,
      status: 'active',
      error_message: null,
    })
  })
})
