import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import OidcCallbackView from '../OidcCallbackView.vue'

// OidcCallbackView.spec.ts checks Oidc callback progression with controlled
// authentication and pending-account responses. The mounted view exercises legacy
// fragments, invitation completion, profile adoption, account creation and
// binding, and TOTP transitions while assertions inspect persistence, requests,
// and navigation.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const replace = vi.fn() // test-controlled replace fixture
const showSuccess = vi.fn() // test-controlled show success fixture
const showError = vi.fn() // test-controlled show error fixture
const setToken = vi.fn() // test-controlled set token fixture
const setPendingAuthSession = vi.fn() // test-controlled set pending auth session fixture
const clearPendingAuthSession = vi.fn() // test-controlled clear pending auth session fixture
const exchangePendingOAuthCompletion = vi.fn() // test-controlled exchange pending oauth completion fixture
const completeOIDCOAuthRegistration = vi.fn() // test-controlled complete oidcoauth registration fixture
const getPublicSettings = vi.fn() // test-controlled get public settings fixture
const login2FA = vi.fn() // test-controlled login2fa fixture
const apiClientPost = vi.fn() // test-controlled api client post fixture
const sendVerifyCode = vi.fn() // test-controlled send verify code fixture
const sendPendingOAuthVerifyCode = vi.fn() // test-controlled send pending oauth verify code fixture

vi.mock('vue-router', () => ({
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => ({
    query: {},
  }),
  // useRouter supplies observable navigation handlers for the mounted
  // authentication component.
  useRouter: () => ({
    replace,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    // useI18n supplies the translation fixture used by rendered authentication
    // labels.
    useI18n: () => ({
      // t resolves the translation keys needed by this test fixture. Unhandled
      // keys remain visible as their identifiers so assertions can inspect copy
      // selection without loading locale catalogs.
      t: (key: string, params?: Record<string, string>) => {
        if (key === 'auth.oauthFlow.totpHint') {
          return `verify ${params?.account ?? ''}`.trim()
        }
        if (!params?.providerName) {
          return key
        }
        return `${key}:${params.providerName}`
      },
    }),
  }
})

vi.mock('@/stores', () => ({
  // useAuthStore exposes the mocked session state and authentication actions
  // consumed by the component.
  useAuthStore: () => ({
    setToken,
    setPendingAuthSession,
    clearPendingAuthSession,
  }),
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    showSuccess,
    showError,
  }),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    // post records account-action paths and payloads through the configured API
    // mock.
    post: (...args: any[]) => apiClientPost(...args),
  },
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    // exchangePendingOAuthCompletion records adoption choices and returns the
    // configured continuation response.
    exchangePendingOAuthCompletion: (...args: any[]) => exchangePendingOAuthCompletion(...args),
    // completeOIDCOAuthRegistration records provider invitation-completion
    // arguments and returns the configured account response.
    completeOIDCOAuthRegistration: (...args: any[]) => completeOIDCOAuthRegistration(...args),
    // getPublicSettings returns the configured public policy response for the
    // current case.
    getPublicSettings: (...args: any[]) => getPublicSettings(...args),
    // login2FA forwards the challenge credential and code to the verification
    // mock.
    login2FA: (...args: any[]) => login2FA(...args),
    // sendVerifyCode records ordinary signup email delivery requests and their
    // proofs.
    sendVerifyCode: (...args: any[]) => sendVerifyCode(...args),
    // sendPendingOAuthVerifyCode records email delivery requests for the pending
    // provider session.
    sendPendingOAuthVerifyCode: (...args: any[]) => sendPendingOAuthVerifyCode(...args),
  }
})

describe('OidcCallbackView', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation.
  beforeEach(() => {
    replace.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    setToken.mockReset()
    setPendingAuthSession.mockReset()
    clearPendingAuthSession.mockReset()
    exchangePendingOAuthCompletion.mockReset()
    completeOIDCOAuthRegistration.mockReset()
    getPublicSettings.mockReset()
    login2FA.mockReset()
    apiClientPost.mockReset()
    sendVerifyCode.mockReset()
    sendPendingOAuthVerifyCode.mockReset()
    getPublicSettings.mockResolvedValue({
      oidc_oauth_provider_name: 'ExampleID',
      turnstile_enabled: false,
      turnstile_site_key: '',
    })
    window.location.hash = ''
    localStorage.clear()
  })

  // The callback fragment contains legacy access and refresh credentials. The view
  // must install that session and follow its destination without calling
  // pending-session exchange first.
  it('accepts the legacy fragment token success callback without pending-session exchange', async () => {
    window.location.hash =
      '#access_token=legacy-access-token&refresh_token=legacy-refresh-token&expires_in=3600&token_type=Bearer&redirect=%2Flegacy-dashboard'
    setToken.mockResolvedValue({})

    mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    expect(setToken).toHaveBeenCalledWith('legacy-access-token')
    expect(localStorage.getItem('refresh_token')).toBe('legacy-refresh-token')
    expect(localStorage.getItem('token_expires_at')).not.toBeNull()
    expect(showSuccess).toHaveBeenCalledWith('auth.loginSuccess')
    expect(replace).toHaveBeenCalledWith('/legacy-dashboard')
  })

  // The legacy fragment supplies an invitation requirement and explicit pending
  // token. Submission must use that token at provider completion without an
  // initial cookie-session exchange.
  it('accepts the legacy pending oauth invitation fragment without pending-session exchange', async () => {
    window.location.hash =
      '#error=invitation_required&pending_oauth_token=legacy-pending-token&redirect=%2Flegacy-invite'
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'legacy-access-token',
        refresh_token: 'legacy-refresh-token',
        expires_in: 3600,
        token_type: 'Bearer',
      },
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    expect(exchangePendingOAuthCompletion).not.toHaveBeenCalled()
    await wrapper.find('input[type="text"]').setValue('invite-code')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/oidc/complete-registration', {
      adopt_display_name: true,
      adopt_avatar: true,
      pending_oauth_token: 'legacy-pending-token',
      invitation_code: 'invite-code',
    })
    expect(setToken).toHaveBeenCalledWith('legacy-access-token')
    expect(replace).toHaveBeenCalledWith('/legacy-invite')
  })

  // The callback begins with a cookie-backed pending session. Its first exchange
  // must have no adoption argument so profile choices remain available for
  // explicit user confirmation.
  it('does not send adoption decisions during the initial exchange', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      redirect: '/dashboard',
      adoption_required: true,
    })
    setToken.mockResolvedValue({})

    mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(1)
    expect(exchangePendingOAuthCompletion).toHaveBeenCalledWith()
  })

  // The initial response requires confirmation of suggested profile details. The
  // view must defer token installation and navigation until the user submits
  // explicit name and avatar choices.
  it('waits for explicit adoption confirmation before finishing a non-invitation login', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        redirect: '/dashboard',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png',
      })
      .mockResolvedValueOnce({
        access_token: 'access-token',
        refresh_token: 'refresh-token',
        expires_in: 3600,
        redirect: '/dashboard',
      })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('OIDC Nick')
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    await checkboxes[0].setValue(false)

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenCalledTimes(2)
    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(1)
    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(2, {
      adoptDisplayName: false,
      adoptAvatar: true,
    })
    expect(setToken).toHaveBeenCalledWith('access-token')
    expect(replace).toHaveBeenCalledWith('/dashboard')
  })

  // The user confirms profile adoption and the next response completes account
  // binding. The view must show binding success and follow its profile redirect
  // without installing another access token.
  it('supports bind completion after adoption confirmation', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        redirect: '/dashboard',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png',
      })
      .mockResolvedValueOnce({
        redirect: '/profile',
      })

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()

    expect(exchangePendingOAuthCompletion).toHaveBeenNthCalledWith(2, {
      adoptDisplayName: true,
      adoptAvatar: true,
    })
    expect(setToken).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('profile.authBindings.bindSuccess')
    expect(replace).toHaveBeenCalledWith('/profile')
  })

  // Adoption confirmation returns another pending existing-account login step. The
  // callback must show the supplied binding email and keep success notifications
  // and final navigation uncalled.
  it('keeps rendering pending bind-login UI when adoption confirmation leads to another pending step', async () => {
    exchangePendingOAuthCompletion
      .mockResolvedValueOnce({
        redirect: '/profile',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png',
      })
      .mockResolvedValueOnce({
        step: 'bind_login_required',
        redirect: '/profile',
        email: 'existing@example.com',
        adoption_required: true,
        suggested_display_name: 'OIDC Nick',
        suggested_avatar_url: 'https://cdn.example/oidc.png',
      })

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()
    await wrapper.findAll('button')[0].trigger('click')
    await flushPromises()

    expect(showSuccess).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(
      (wrapper.get('[data-testid="oidc-bind-login-email"]').element as HTMLInputElement).value,
    ).toBe('existing@example.com')
  })

  // The initial exchange requires creating an account before authentication can
  // complete. The callback must record the provider, pending token representation,
  // and redirect for that continuation.
  it('persists a pending auth session when the oauth flow still needs account creation', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome',
    })

    mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    expect(setPendingAuthSession).toHaveBeenCalledWith({
      token: '',
      token_field: 'pending_oauth_token',
      provider: 'oidc',
      redirect: '/welcome',
    })
  })

  // An invitation response includes suggested profile details. The callback must
  // render both choices and send the user's selected adoption values when
  // completing the provider registration.
  it('renders adoption choices for invitation flow and submits the selected values', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'invitation_required',
      redirect: '/dashboard',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png',
    })
    completeOIDCOAuthRegistration.mockResolvedValue({
      access_token: 'access-token',
      refresh_token: 'refresh-token',
      expires_in: 3600,
      token_type: 'Bearer',
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[1].setValue(false)
    await wrapper.find('input[type="text"]').setValue('invite-code')
    await wrapper.find('button').trigger('click')

    expect(completeOIDCOAuthRegistration).toHaveBeenCalledWith(
      'invite-code',
      {
        adoptDisplayName: true,
        adoptAvatar: false,
      },
      undefined,
    )
  })

  // Invitation completion succeeds at HTTP level but returns an account-choice
  // step. The callback must render the remaining choices without installing a
  // token or navigating away.
  it('keeps the oauth flow active when complete-registration returns another pending step', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'invitation_required',
      redirect: '/dashboard',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png',
    })
    completeOIDCOAuthRegistration.mockResolvedValue({
      auth_result: 'pending_session',
      step: 'choose_account_action_required',
      redirect: '/dashboard',
      email: 'fresh@example.com',
      resolved_email: 'fresh@example.com',
      force_email_on_signup: true,
      adoption_required: true,
    })

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()
    await wrapper.find('input[type="text"]').setValue('invite-code')
    await wrapper.find('button').trigger('click')
    await flushPromises()

    expect(completeOIDCOAuthRegistration).toHaveBeenCalledWith(
      'invite-code',
      {
        adoptDisplayName: true,
        adoptAvatar: true,
      },
      undefined,
    )
    expect(setToken).not.toHaveBeenCalled()
    expect(replace).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('auth.oauthFlow.bindExistingAccount')
    expect(wrapper.text()).toContain('auth.oauthFlow.createNewAccount')
  })

  // The user completes a pending account draft and selects profile adoption
  // values. The creation request must carry those fields, and its authenticated
  // result must install a token before navigation.
  it('collects email, password, and verify code for pending oauth account creation and submits adoption decisions', async () => {
    getPublicSettings.mockResolvedValue({
      oidc_oauth_provider_name: 'ExampleID',
      invitation_code_enabled: true,
      turnstile_enabled: false,
      turnstile_site_key: '',
    })
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png',
    })
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'new-access-token',
        refresh_token: 'new-refresh-token',
        expires_in: 3600,
        token_type: 'Bearer',
      },
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[1].setValue(false)
    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('  new@example.com  ')
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-verify-code"]').setValue('246810')
    await wrapper.get('[data-testid="oidc-create-account-invitation-code"]').setValue(' INVITE123 ')
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/create-account', {
      email: 'new@example.com',
      password: 'secret-123',
      verify_code: '246810',
      invitation_code: 'INVITE123',
      adopt_display_name: true,
      adopt_avatar: false,
    })
    expect(setToken).toHaveBeenCalledWith('new-access-token')
    expect(replace).toHaveBeenCalledWith('/welcome')
  })

  // The creation endpoint reports that the submitted email already belongs to an
  // account. The callback must reopen existing-account authentication with that
  // email retained in its input.
  it('switches to bind-login when create-account returns EMAIL_EXISTS', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome',
    })
    apiClientPost.mockRejectedValue({
      response: {
        data: {
          reason: 'EMAIL_EXISTS',
          message: 'email already exists',
        },
      },
    })

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('existing@example.com')
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(
      (wrapper.get('[data-testid="oidc-bind-login-email"]').element as HTMLInputElement).value,
    ).toBe('existing@example.com')
  })

  // The pending creation endpoint rejects the submitted account draft. The
  // callback must report the failure through its toast path without duplicating
  // the message inside the form content.
  it('shows create-account failures through toast without inline error text', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome',
    })
    apiClientPost.mockRejectedValue(new Error('create failed'))

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('new@example.com')
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('create failed')
    expect(wrapper.text()).not.toContain('create failed')
  })

  // The callback displays its shared pending-account form and the user requests
  // verification. The pending email-code endpoint must receive the entered address
  // and expected proof fields.
  it('sends a verify code for pending oauth account creation', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'email_required',
      redirect: '/welcome',
    })
    sendPendingOAuthVerifyCode.mockResolvedValue({
      message: 'sent',
      countdown: 60,
    })

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    await wrapper.get('[data-testid="oidc-create-account-email"]').setValue('  new@example.com  ')
    await wrapper.get('[data-testid="oidc-create-account-send-code"]').trigger('click')
    await flushPromises()

    expect(sendPendingOAuthVerifyCode).toHaveBeenCalledWith({
      email: 'new@example.com',
    })
  })

  // The server requests password authentication for an existing account. The
  // callback must submit credentials and selected profile choices, then install
  // the returned session and follow its destination.
  it('shows bind-login form for existing account binding and submits credentials with adoption decisions', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'adopt_existing_user_by_email',
      redirect: '/profile/security',
      email: 'existing@example.com',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png',
    })
    apiClientPost.mockResolvedValue({
      data: {
        access_token: 'bind-access-token',
        refresh_token: 'bind-refresh-token',
        expires_in: 3600,
        token_type: 'Bearer',
      },
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[0].setValue(false)
    await wrapper.get('[data-testid="oidc-bind-login-email"]').setValue('existing@example.com')
    await wrapper.get('[data-testid="oidc-bind-login-password"]').setValue('secret-password')
    await wrapper.get('[data-testid="oidc-bind-login-submit"]').trigger('click')
    await flushPromises()

    expect(apiClientPost).toHaveBeenCalledWith('/auth/oauth/pending/bind-login', {
      email: 'existing@example.com',
      password: 'secret-password',
      adopt_display_name: false,
      adopt_avatar: true,
    })
    expect(setToken).toHaveBeenCalledWith('bind-access-token')
    expect(replace).toHaveBeenCalledWith('/profile/security')
  })

  // Existing-account password authentication returns a masked-email second-factor
  // challenge. The callback must wait for the code, send the temporary token with
  // it, and navigate only after authentication succeeds.
  it('handles bind-login 2FA challenge before redirecting', async () => {
    exchangePendingOAuthCompletion.mockResolvedValue({
      error: 'adopt_existing_user_by_email',
      redirect: '/profile',
      email: 'existing@example.com',
      adoption_required: true,
      suggested_display_name: 'OIDC Nick',
      suggested_avatar_url: 'https://cdn.example/oidc.png',
    })
    apiClientPost.mockResolvedValue({
      data: {
        requires_2fa: true,
        temp_token: 'temp-123',
        user_email_masked: 'o***g@example.com',
      },
    })
    login2FA.mockResolvedValue({
      access_token: '2fa-access-token',
    })
    setToken.mockResolvedValue({})

    const wrapper = mount(OidcCallbackView, {
      global: {
        stubs: {
          AuthLayout: { template: '<div><slot /></div>' },
          Icon: true,
          RouterLink: { template: '<a><slot /></a>' },
          transition: false,
        },
      },
    })

    await flushPromises()

    await wrapper.get('[data-testid="oidc-bind-login-password"]').setValue('secret-password')
    await wrapper.get('[data-testid="oidc-bind-login-submit"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('o***g@example.com')
    expect(login2FA).not.toHaveBeenCalled()

    await wrapper.get('[data-testid="oidc-bind-login-totp"]').setValue('123456')
    await wrapper.get('[data-testid="oidc-bind-login-totp-submit"]').trigger('click')
    await flushPromises()

    expect(login2FA).toHaveBeenCalledWith({
      temp_token: 'temp-123',
      totp_code: '123456',
    })
    expect(setToken).toHaveBeenCalledWith('2fa-access-token')
    expect(replace).toHaveBeenCalledWith('/profile')
  })
})
