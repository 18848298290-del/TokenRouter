import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

// GoogleOneTap.spec.ts checks prompt eligibility, credential exchange, and
// callback cleanup with a controllable Google SDK replacement. Tests distinguish
// authenticated sessions from pending registration and inspect token persistence,
// referral cleanup, local redirects, and visible failures.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const mocks = vi.hoisted(() => ({
  showGoogleOneTap: vi.fn(),
  cancelGoogleOneTap: vi.fn(),
  originSupported: true,
  credentialCallback: undefined as
    | undefined
    | ((response: {
        credential: string // Google credential JWT
      }) => Promise<void>),
  exchangeGoogleOneTap: vi.fn(),
  persistOAuthTokenContext: vi.fn(),
  resolveAffiliateCode: vi.fn(),
  storeOAuthAffiliateCode: vi.fn(),
  clearAllAffiliateCodes: vi.fn(),
  setToken: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  push: vi.fn(),
  routeQuery: {} as Record<string, unknown>,
  isAuthenticated: false,
})) // hoisted dependency and routing fixtures

vi.mock('@/utils/googleIdentity', () => ({
  // showGoogleOneTap captures the registered SDK callback before forwarding the
  // request to its mock. Tests invoke that callback directly to drive credential
  // exchange without loading the real browser identity SDK.
  showGoogleOneTap: (
    clientID: string,
    callback: (response: {
      credential: string // Google credential JWT
    }) => Promise<void>,
  ) => {
    mocks.credentialCallback = callback
    return mocks.showGoogleOneTap(clientID, callback)
  },
  // cancelGoogleOneTap records release of the component-owned Google prompt
  // callback.
  cancelGoogleOneTap: (...args: unknown[]) => mocks.cancelGoogleOneTap(...args),
  // isGoogleOneTapOriginSupported returns the controllable browser-origin
  // eligibility flag for prompting.
  isGoogleOneTapOriginSupported: () => mocks.originSupported,
}))

vi.mock('@/api/auth', () => ({
  // exchangeGoogleOneTap forwards component calls to the corresponding
  // assertion-controlled test mock.
  exchangeGoogleOneTap: (...args: unknown[]) => mocks.exchangeGoogleOneTap(...args),
  // persistOAuthTokenContext records refresh metadata published before
  // access-token installation.
  persistOAuthTokenContext: (...args: unknown[]) => mocks.persistOAuthTokenContext(...args),
}))

vi.mock('@/utils/oauthAffiliate', () => ({
  // resolveAffiliateCode records referral lookup arguments and returns the
  // configured attribution.
  resolveAffiliateCode: (...args: unknown[]) => mocks.resolveAffiliateCode(...args),
  // storeOAuthAffiliateCode records referral attribution retained for a subsequent
  // provider callback.
  storeOAuthAffiliateCode: (...args: unknown[]) => mocks.storeOAuthAffiliateCode(...args),
  // clearAllAffiliateCodes records attribution cleanup after the authentication
  // flow completes.
  clearAllAffiliateCodes: (...args: unknown[]) => mocks.clearAllAffiliateCodes(...args),
}))

vi.mock('@/utils/apiError', () => ({
  // extractI18nErrorMessage returns the fixture login-failure key for rejected
  // credential exchange.
  extractI18nErrorMessage: () => 'auth.loginFailed',
}))

vi.mock('@/stores', () => ({
  // useAuthStore exposes the mocked session state and authentication actions
  // consumed by the component.
  useAuthStore: () => ({
    // isAuthenticated reads the mutable authentication flag used by prompt
    // eligibility tests.
    get isAuthenticated() {
      return mocks.isAuthenticated
    },
    // setToken records which authenticated access token the callback installs in
    // the store.
    setToken: (...args: unknown[]) => mocks.setToken(...args),
  }),
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    // showSuccess records completion feedback delivered through the app toast
    // service.
    showSuccess: (...args: unknown[]) => mocks.showSuccess(...args),
    // showError records failure feedback delivered through the app toast service.
    showError: (...args: unknown[]) => mocks.showError(...args),
  }),
}))

vi.mock('vue-router', () => ({
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => ({ query: mocks.routeQuery }),
  // useRouter supplies observable navigation handlers for the mounted
  // authentication component.
  useRouter: () => ({
    // push records destinations requested after the authentication action completes.
    push: (...args: unknown[]) => mocks.push(...args),
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

import GoogleOneTap from '@/components/auth/GoogleOneTap.vue'

// mountOneTap mounts the Google prompt with the fixture client identifier.
// Prompting is enabled by default, while callers can pass false to exercise a
// disabled initial state.
function mountOneTap(enabled = true) {
  return mount(GoogleOneTap, {
    props: {
      enabled,
      clientId: 'google-client',
    },
  })
}

describe('GoogleOneTap', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation.
  beforeEach(() => {
    for (const mock of [
      mocks.showGoogleOneTap,
      mocks.cancelGoogleOneTap,
      mocks.exchangeGoogleOneTap,
      mocks.persistOAuthTokenContext,
      mocks.resolveAffiliateCode,
      mocks.storeOAuthAffiliateCode,
      mocks.clearAllAffiliateCodes,
      mocks.setToken,
      mocks.showSuccess,
      mocks.showError,
      mocks.push,
    ]) {
      mock.mockReset()
    }
    mocks.originSupported = true
    mocks.credentialCallback = undefined
    mocks.routeQuery = { redirect: '/usage', aff: 'AFF123', promo_code: 'PROMO123' }
    mocks.isAuthenticated = false
    mocks.showGoogleOneTap.mockResolvedValue(undefined)
    mocks.resolveAffiliateCode.mockReturnValue('AFF123')
    mocks.setToken.mockResolvedValue(undefined)
    mocks.push.mockResolvedValue(undefined)
    window.sessionStorage.clear()
  })

  // The Google callback returns a complete authenticated session. The test checks
  // exchange metadata, token-context persistence, access-token installation,
  // affiliate cleanup, and navigation to the requested local route.
  it('persists an authenticated response and follows the requested local redirect', async () => {
    const response = {
      status: 'authenticated',
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      token_type: 'Bearer',
    }
    mocks.exchangeGoogleOneTap.mockResolvedValue(response)

    mountOneTap()
    await flushPromises()
    expect(mocks.showGoogleOneTap).toHaveBeenCalledWith('google-client', expect.any(Function))

    await mocks.credentialCallback?.({ credential: 'google-credential' })
    await flushPromises()

    expect(mocks.exchangeGoogleOneTap).toHaveBeenCalledWith({
      credential: 'google-credential',
      redirect: '/usage',
      aff_code: 'AFF123',
      promo_code: 'PROMO123',
    })
    expect(mocks.persistOAuthTokenContext).toHaveBeenCalledWith(response)
    expect(mocks.setToken).toHaveBeenCalledWith('access-token')
    expect(mocks.clearAllAffiliateCodes).toHaveBeenCalledOnce()
    expect(mocks.push).toHaveBeenCalledWith('/usage')
  })

  // Google credential exchange reports that registration is required. The
  // component must remember Google and open the completion route without
  // persisting an authenticated token response.
  it('continues new users through the existing OAuth registration page', async () => {
    mocks.exchangeGoogleOneTap.mockResolvedValue({
      status: 'registration_required',
      redirect: '/dashboard',
    })

    mountOneTap()
    await flushPromises()
    await mocks.credentialCallback?.({ credential: 'google-credential' })
    await flushPromises()

    expect(window.sessionStorage.getItem('email_oauth_pending_provider')).toBe('google')
    expect(mocks.persistOAuthTokenContext).not.toHaveBeenCalled()
    expect(mocks.push).toHaveBeenCalledWith('/auth/oauth/callback')
  })

  // The Google prompt SDK rejects its display request. The component must leave
  // that failure silent so prompt unavailability does not produce a login error
  // toast.
  it('keeps ordinary login available when the SDK is unavailable', async () => {
    mocks.showGoogleOneTap.mockRejectedValue(new Error('blocked'))
    mountOneTap()
    await flushPromises()

    expect(mocks.showError).not.toHaveBeenCalled()
  })

  // The prompt supplies a credential whose backend exchange fails. The component
  // must show the translated login error and leave the requested destination
  // unopened.
  it('reports credential exchange errors without removing ordinary login', async () => {
    mocks.exchangeGoogleOneTap.mockRejectedValue(new Error('invalid credential'))
    mountOneTap()
    await flushPromises()
    await mocks.credentialCallback?.({ credential: 'google-credential' })
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('auth.loginFailed')
    expect(mocks.push).not.toHaveBeenCalled()
  })

  // The fixture separately marks authentication complete, disables the component,
  // and rejects origin support. Each condition must prevent any Google prompt
  // display request.
  it('does not prompt for authenticated, disabled, or insecure sessions', async () => {
    const disabled = mountOneTap(false)
    await flushPromises()
    expect(mocks.showGoogleOneTap).not.toHaveBeenCalled()
    disabled.unmount()

    mocks.isAuthenticated = true
    mountOneTap()
    await flushPromises()
    expect(mocks.showGoogleOneTap).not.toHaveBeenCalled()

    mocks.isAuthenticated = false
    mocks.originSupported = false
    mountOneTap()
    await flushPromises()
    expect(mocks.showGoogleOneTap).not.toHaveBeenCalled()
  })

  // An active One Tap component loses eligibility and is later unmounted. Both
  // lifecycle transitions must call the SDK cancellation path for its prompt
  // callback.
  it('cancels the browser prompt when disabled or unmounted', async () => {
    const wrapper = mountOneTap()
    await flushPromises()
    await wrapper.setProps({ enabled: false })
    expect(mocks.cancelGoogleOneTap).toHaveBeenCalled()

    mocks.cancelGoogleOneTap.mockClear()
    wrapper.unmount()
    expect(mocks.cancelGoogleOneTap).toHaveBeenCalled()
  })
})
