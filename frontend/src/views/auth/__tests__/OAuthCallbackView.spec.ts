import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import OAuthCallbackView from '@/views/auth/OAuthCallbackView.vue'

// OAuthCallbackView.spec.ts checks generic callback display and email-provider
// callback recovery. Routing, clipboard, and API replacements expose forwarded
// query values, invitation completion payloads, error toasts, and suppression of
// manual copy fields on invalid email-provider visits.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const {
  routeState,
  locationState,
  routerReplaceMock,
  showErrorMock,
  showSuccessMock,
  setTokenMock,
  copyToClipboardMock,
  exchangePendingOAuthCompletionMock,
  apiPostMock,
} = vi.hoisted(() => ({
  routeState: {
    path: '/auth/callback',
    query: {} as Record<string, unknown>,
  },
  locationState: {
    current: {
      href: 'http://localhost/auth/callback',
      hash: '',
    } as {
      href: string // mock complete browser URL
      hash: string // mock URL fragment
    },
  },
  routerReplaceMock: vi.fn(),
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn(),
  setTokenMock: vi.fn(),
  copyToClipboardMock: vi.fn(),
  exchangePendingOAuthCompletionMock: vi.fn(),
  apiPostMock: vi.fn(),
})) // hoisted dependency and routing fixtures

vi.mock('vue-router', () => ({
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => routeState,
  // useRouter supplies observable navigation handlers for the mounted
  // authentication component.
  useRouter: () => ({
    // replace records callback destinations without changing the fixture browser
    // history.
    replace: (...args: any[]) => routerReplaceMock(...args),
  }),
}))

vi.mock('vue-i18n', () => ({
  // useI18n supplies the translation fixture used by rendered authentication
  // labels.
  useI18n: () => ({
    // t returns translation keys directly for stable component text assertions.
    t: (key: string) => key,
  }),
}))

vi.mock('@/stores', () => ({
  // useAuthStore exposes the mocked session state and authentication actions
  // consumed by the component.
  useAuthStore: () => ({
    // setToken records which authenticated access token the callback installs in
    // the store.
    setToken: (...args: any[]) => setTokenMock(...args),
  }),
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    // showError records failure feedback delivered through the app toast service.
    showError: (...args: any[]) => showErrorMock(...args),
    // showSuccess records completion feedback delivered through the app toast
    // service.
    showSuccess: (...args: any[]) => showSuccessMock(...args),
  }),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    // post records account-action paths and payloads through the configured API
    // mock.
    post: (...args: any[]) => apiPostMock(...args),
  },
  // buildApiUrl prefixes the fixture API root to callback endpoint paths. It
  // accepts paths with or without a leading slash so navigation assertions can
  // inspect the constructed browser address.
  buildApiUrl: (path: string) => `/api/v1${path.startsWith('/') ? path : `/${path}`}`,
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    // exchangePendingOAuthCompletion records adoption choices and returns the
    // configured continuation response.
    exchangePendingOAuthCompletion: (...args: any[]) => exchangePendingOAuthCompletionMock(...args),
    persistOAuthTokenContext: vi.fn(),
  }
})

vi.mock('@/composables/useClipboard', () => ({
  // useClipboard supplies the observable copy action used by manual callback
  // controls.
  useClipboard: () => ({
    // copyToClipboard forwards component calls to the corresponding
    // assertion-controlled test mock.
    copyToClipboard: (...args: any[]) => copyToClipboardMock(...args),
  }),
}))

describe('OAuthCallbackView', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation.
  beforeEach(() => {
    routeState.path = '/auth/callback'
    routeState.query = {}
    locationState.current = {
      href: 'http://localhost/auth/callback',
      hash: '',
    }
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState.current,
    })
    routerReplaceMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    setTokenMock.mockReset()
    copyToClipboardMock.mockReset()
    exchangePendingOAuthCompletionMock.mockReset()
    apiPostMock.mockReset()
    window.sessionStorage.clear()
  })

  // The generic callback route receives code and state values. Its content must
  // expose localized copy actions and readonly fields containing those values for
  // manual consumers.
  it('renders localized callback copy actions', () => {
    routeState.query = {
      code: 'oauth-code',
      state: 'oauth-state',
    }

    const wrapper = mount(OAuthCallbackView)

    expect(wrapper.text()).toContain('auth.oauth.callbackTitle')
    expect(wrapper.text()).toContain('auth.oauth.callbackHint')
    expect(wrapper.text()).toContain('common.copy')
    expect(wrapper.find('input[value="oauth-code"]').exists()).toBe(true)
    expect(wrapper.find('input[value="oauth-state"]').exists()).toBe(true)
  })

  // The generic callback receives an error query value. The page must send that
  // message to the app toast without rendering duplicate red error content.
  it('sends callback errors to toast instead of rendering inline red text', () => {
    routeState.query = {
      error: 'oauth failed',
    }

    const wrapper = mount(OAuthCallbackView)

    expect(showErrorMock).toHaveBeenCalledWith('oauth failed')
    expect(wrapper.text()).not.toContain('oauth failed')
    expect(wrapper.find('.bg-red-50').exists()).toBe(false)
  })

  // The email-provider callback is visited without a usable pending completion.
  // The page must attempt exchange and show invalid-callback content while hiding
  // generic readonly copy fields.
  it('does not render manual copy fields for direct email oauth callback visits', async () => {
    routeState.path = '/auth/oauth/callback'
    exchangePendingOAuthCompletionMock.mockRejectedValue(new Error('pending session not found'))

    const wrapper = mount(OAuthCallbackView)
    await vi.dynamicImportSettled()

    expect(exchangePendingOAuthCompletionMock).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).toContain('auth.oauth.invalidCallbackTitle')
    expect(wrapper.text()).toContain('auth.oauth.invalidCallbackHint')
    expect(wrapper.find('input[readonly]').exists()).toBe(false)
  })

  // The email-provider callback fragment contains an authorization error. The page
  // must show its toast and invalid state without attempting exchange or exposing
  // manual copy fields.
  it('shows invalid callback state for email oauth fragment errors', async () => {
    routeState.path = '/auth/oauth/callback'
    locationState.current.hash = '#error=provider_error&error_description=denied'

    const wrapper = mount(OAuthCallbackView)
    await vi.dynamicImportSettled()

    expect(showErrorMock).toHaveBeenCalledWith('denied')
    expect(wrapper.text()).toContain('auth.oauth.invalidCallbackTitle')
    expect(wrapper.find('input[readonly]').exists()).toBe(false)
    expect(exchangePendingOAuthCompletionMock).not.toHaveBeenCalled()
  })

  // A remembered email provider returns code and state to the frontend route. The
  // page must forward those parameters to that provider's backend callback without
  // pending-session exchange.
  it('forwards frontend email oauth provider callbacks back to the backend callback endpoint', async () => {
    routeState.path = '/auth/oauth/callback'
    routeState.query = {
      code: 'provider-code',
      state: 'provider-state',
    }
    window.sessionStorage.setItem('email_oauth_pending_provider', 'google')

    mount(OAuthCallbackView)
    await vi.dynamicImportSettled()

    expect(locationState.current.href).toBe(
      '/api/v1/auth/oauth/google/callback?code=provider-code&state=provider-state',
    )
    expect(exchangePendingOAuthCompletionMock).not.toHaveBeenCalled()
  })

  // A pending email-provider signup requires an invitation and has stored referral
  // attribution. Completion must send that attribution with the password and
  // invitation, then install the returned token.
  it('submits stored affiliate code when completing invited email oauth registration', async () => {
    routeState.path = '/auth/oauth/callback'
    exchangePendingOAuthCompletionMock.mockResolvedValue({
      error: 'invitation_required',
      provider: 'google',
      redirect: '/dashboard',
      resolved_email: 'pending@example.com',
      invitation_required: true,
    })
    apiPostMock.mockResolvedValue({
      data: {
        access_token: 'token-1',
      },
    })
    window.sessionStorage.setItem('oauth_aff_code', 'REF456')

    const wrapper = mount(OAuthCallbackView)
    await vi.dynamicImportSettled()
    const passwordInputs = wrapper.findAll('input[type="password"]')
    await passwordInputs[0].setValue('secret-123')
    await passwordInputs[1].setValue('secret-123')
    const invitationInput = wrapper.find('input[type="text"]')
    await invitationInput.setValue('INVITE456')
    await wrapper.findAll('button').at(0)?.trigger('click')

    expect(apiPostMock).toHaveBeenCalledWith('/auth/oauth/google/complete-registration', {
      password: 'secret-123',
      invitation_code: 'INVITE456',
      aff_code: 'REF456',
    })
    expect(setTokenMock).toHaveBeenCalledWith('token-1')
  })
})
