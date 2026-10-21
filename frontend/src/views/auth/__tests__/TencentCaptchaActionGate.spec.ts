import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'

// TencentCaptchaActionGate.spec.ts checks action-captcha ordering for password,
// passkey, and provider login. Controllable captcha and browser-location
// replacements expose proof forwarding, cancellation, and consent gating,
// including Aliyun compatibility fields and One Tap cancellation during password
// entry.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const loginMock = vi.fn() // test-controlled login mock fixture
const loginWithPasskeyMock = vi.fn() // test-controlled login with passkey mock fixture
const getPublicSettingsMock = vi.fn() // test-controlled get public settings mock fixture
const startOAuthLoginMock = vi.fn() // test-controlled start oauth login mock fixture
const verifyActionMock = vi.fn() // test-controlled verify action mock fixture
const captchaResetMock = vi.fn() // test-controlled captcha reset mock fixture
const oneTapCancelMock = vi.fn() // test-controlled one tap cancel mock fixture
const locationState = {
  href: 'http://localhost/login',
  protocol: 'http:',
  hostname: 'localhost',
} // test-controlled location state fixture

vi.mock('vue-router', () => ({
  // useRouter supplies observable navigation handlers for the mounted
  // authentication component.
  useRouter: () => ({
    currentRoute: { value: { query: {} } },
    push: vi.fn(),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    // useI18n supplies the translation fixture used by rendered authentication
    // labels.
    useI18n: () => ({
      // t returns translation keys directly for stable component text assertions.
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores', () => ({
  // useAuthStore exposes the mocked session state and authentication actions
  // consumed by the component.
  useAuthStore: () => ({
    // login forwards submitted password credentials to the configured
    // authentication mock.
    login: (...args: unknown[]) => loginMock(...args),
    // loginWithPasskey records the captcha proof supplied before passkey
    // authentication.
    loginWithPasskey: (...args: unknown[]) => loginWithPasskeyMock(...args),
  }),
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showWarning: vi.fn(),
  }),
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    // getPublicSettings returns the configured public policy response for the
    // current case.
    getPublicSettings: (...args: unknown[]) => getPublicSettingsMock(...args),
    // startOAuthLogin records provider requests and proof before returning the
    // authorization URL.
    startOAuthLogin: (...args: unknown[]) => startOAuthLoginMock(...args),
    // isTotp2FARequired returns the fixture result used to select authentication
    // behavior.
    isTotp2FARequired: () => false,
    // isWeChatWebOAuthEnabled disables the unrelated WeChat entry in these login
    // fixtures.
    isWeChatWebOAuthEnabled: () => false,
  }
})

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const CaptchaChallengeStub = defineComponent({
  // setup exposes the fixture controls expected by the parent component. The
  // rendered placeholder omits external SDK behavior, letting tests drive
  // verification, cancellation, or reset through the configured mock functions.
  setup(_, { expose }) {
    expose({
      verifyAction: verifyActionMock,
      reset: captchaResetMock,
    })
    return () => h('div')
  },
}) // test-controlled captcha challenge stub fixture

const OAuthButtonStub = defineComponent({
  emits: ['start'],
  // setup renders a minimal action control for the parent component test. Its
  // click emits the fixture event so the real parent handler can run without
  // mounting the full child implementation.
  setup(_, { emit }) {
    return () =>
      h('button', {
        type: 'button',
        'data-testid': 'oauth-start',
        // onClick emits the fixture action consumed by the parent handler.
        onClick: () =>
          emit('start', {
            provider: 'github',
            params: { redirect: '/dashboard' },
          }),
      })
  },
}) // test-controlled oauth button stub fixture

const GoogleOneTapStub = defineComponent({
  name: 'GoogleOneTap',
  props: {
    enabled: Boolean, // Parent One Tap eligibility flag.
    clientId: String, // Parent Google client identifier.
  },
  // setup exposes the fixture controls expected by the parent component. The
  // rendered placeholder omits external SDK behavior, letting tests drive
  // verification, cancellation, or reset through the configured mock functions.
  setup(props, { expose }) {
    expose({ cancelPrompt: oneTapCancelMock })
    return () =>
      h('div', {
        'data-testid': 'google-one-tap',
        'data-enabled': String(props.enabled),
        'data-client-id': props.clientId,
      })
  },
}) // test-controlled google one tap stub fixture

const LoginAgreementStub = defineComponent({
  name: 'LoginAgreementPrompt',
  emits: ['accept', 'reject', 'open'],
  // setup renders a minimal action control for the parent component test. Its
  // click emits the fixture event so the real parent handler can run without
  // mounting the full child implementation.
  setup(_, { emit }) {
    return () =>
      h('button', {
        type: 'button',
        'data-testid': 'accept-login-agreement',
        // onClick emits the fixture action consumed by the parent handler.
        onClick: () => emit('accept'),
      })
  },
}) // test-controlled login agreement stub fixture

// mountLogin renders the login page with controllable captcha and provider-action
// components. Callers flush settings promises before interacting with actions
// whose availability depends on the configured public settings.
function mountLogin() {
  return mount(LoginView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        RouterLink: true,
        TurnstileWidget: CaptchaChallengeStub,
        Icon: true,
        LoginAgreementPrompt: LoginAgreementStub,
        TotpLoginModal: true,
        GoogleOneTap: GoogleOneTapStub,
        EmailOAuthButtons: OAuthButtonStub,
        LinuxDoOAuthSection: true,
        DingTalkOAuthSection: true,
        OidcOAuthSection: true,
        WechatOAuthSection: true,
      },
    },
  })
}

// enableAliyunCaptcha configures the login settings fixture for Aliyun action
// verification. It disables Tencent and Turnstile, provides a complete scene
// configuration, and supplies a matching verification parameter for submission
// assertions.
function enableAliyunCaptcha(): void {
  getPublicSettingsMock.mockResolvedValue({
    turnstile_enabled: false,
    turnstile_site_key: '',
    tencent_captcha_enabled: false,
    tencent_captcha_app_id: '',
    aliyun_captcha_enabled: true,
    aliyun_captcha_scene_id: 'scene-1',
    aliyun_captcha_prefix: 'prefix-1',
    aliyun_captcha_region: 'cn',
    backend_mode_enabled: false,
    password_reset_enabled: false,
    passkey_enabled: true,
    github_oauth_enabled: true,
    google_oauth_enabled: false,
  })
  verifyActionMock.mockResolvedValue({ token: 'aliyun-param-1', randstr: '' })
}

describe('Action captcha gate', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case.
  beforeEach(() => {
    loginMock.mockReset()
    loginWithPasskeyMock.mockReset()
    getPublicSettingsMock.mockReset()
    startOAuthLoginMock.mockReset()
    verifyActionMock.mockReset()
    captchaResetMock.mockReset()
    oneTapCancelMock.mockReset()
    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'tencent-app-id',
      backend_mode_enabled: false,
      password_reset_enabled: false,
      passkey_enabled: true,
      github_oauth_enabled: true,
      google_oauth_enabled: false,
    })
    loginMock.mockResolvedValue({})
    loginWithPasskeyMock.mockResolvedValue({})
    startOAuthLoginMock.mockResolvedValue({ authorize_url: 'https://github.example/authorize' })
    verifyActionMock.mockResolvedValue({ token: 'ticket-1', randstr: '@rand-1' })
    Object.defineProperty(window, 'PublicKeyCredential', {
      configurable: true,
      value: class PublicKeyCredential {},
    })
    locationState.href = 'http://localhost/login'
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: locationState,
    })
  })

  // The user submits valid password credentials with Tencent action captcha
  // enabled. The test checks a verification call and forwarding of its ticket and
  // random string to login.
  it('clicking login opens Tencent captcha before calling login', async () => {
    const wrapper = mountLogin()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginMock).toHaveBeenCalledWith(
      expect.objectContaining({
        tencent_captcha_ticket: 'ticket-1',
        tencent_captcha_randstr: '@rand-1',
      }),
    )
  })

  // The captcha replacement returns no proof after password submission. The page
  // must invoke verification once and stop before calling the authentication store
  // login method.
  it('does not call login when Tencent captcha is closed', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = mountLogin()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginMock).not.toHaveBeenCalled()
  })

  // The password form lacks valid required input while Tencent captcha is enabled.
  // Local validation must stop both the challenge display and the login request.
  it('does not open Tencent captcha when login form validation fails', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).not.toHaveBeenCalled()
    expect(loginMock).not.toHaveBeenCalled()
  })

  // The user chooses provider login with Tencent action captcha enabled. Its proof
  // must reach the start endpoint before browser navigation, and the widget must
  // reset afterward.
  it('starts OAuth through the Tencent gate before navigating', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('[data-testid="oauth-start"]').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(startOAuthLoginMock).toHaveBeenCalledWith(
      { provider: 'github', params: { redirect: '/dashboard' } },
      {
        tencent_captcha_ticket: 'ticket-1',
        tencent_captcha_randstr: '@rand-1',
      },
    )
    expect(locationState.href).toBe('https://github.example/authorize')
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  // The provider start action receives no proof from the captcha replacement. The
  // start endpoint must remain uncalled and the browser must stay on the login
  // URL.
  it('does not start OAuth when Tencent captcha is closed', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('[data-testid="oauth-start"]').trigger('click')
    await flushPromises()

    expect(startOAuthLoginMock).not.toHaveBeenCalled()
    expect(locationState.href).toBe('http://localhost/login')
  })

  // The user requests passkey login with Tencent action captcha enabled. The newly
  // supplied ticket and random string must reach passkey authentication before the
  // widget resets.
  it('passes a fresh Tencent proof to Passkey login', async () => {
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('button.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginWithPasskeyMock).toHaveBeenCalledWith({
      tencent_captcha_ticket: 'ticket-1',
      tencent_captcha_randstr: '@rand-1',
    })
    expect(captchaResetMock).toHaveBeenCalledOnce()
  })

  // The passkey action opens captcha but the replacement returns no proof. The
  // page must stop there without invoking the auth store passkey method.
  it('does not invoke Passkey when Tencent captcha is closed', async () => {
    verifyActionMock.mockResolvedValue(null)
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('button.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(loginWithPasskeyMock).not.toHaveBeenCalled()
  })

  // Public settings enable Aliyun action captcha for password login. The returned
  // verification parameter must travel through the compatible turnstile_token
  // field in the login payload.
  it('passes the Aliyun verification parameter to password login', async () => {
    enableAliyunCaptcha()
    const wrapper = mountLogin()
    await flushPromises()
    await wrapper.get('#email').setValue('user@example.com')
    await wrapper.get('#password').setValue('secret-123')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginMock).toHaveBeenCalledWith(
      expect.objectContaining({
        turnstile_token: 'aliyun-param-1',
        tencent_captcha_ticket: undefined,
        tencent_captcha_randstr: undefined,
      }),
    )
  })

  // Public settings enable Aliyun action captcha before provider authorization.
  // The start request must receive its verification parameter through
  // turnstile_token after the challenge succeeds.
  it('passes the Aliyun verification parameter to OAuth start', async () => {
    enableAliyunCaptcha()
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('[data-testid="oauth-start"]').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(startOAuthLoginMock).toHaveBeenCalledWith(
      { provider: 'github', params: { redirect: '/dashboard' } },
      { turnstile_token: 'aliyun-param-1' },
    )
  })

  // The passkey action uses the Aliyun challenge fixture. The authentication store
  // must receive the returned verification parameter through the compatible
  // turnstile_token request field.
  it('passes the Aliyun verification parameter to Passkey login', async () => {
    enableAliyunCaptcha()
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('button.btn-secondary.w-full').trigger('click')
    await flushPromises()

    expect(verifyActionMock).toHaveBeenCalledOnce()
    expect(loginWithPasskeyMock).toHaveBeenCalledWith({
      turnstile_token: 'aliyun-param-1',
    })
  })

  // The Google prompt is configured while current agreement consent is missing.
  // Its mounted enabled attribute must become true only after the agreement
  // component emits acceptance.
  it('enables One Tap only after the current login agreement is accepted', async () => {
    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: false,
      aliyun_captcha_enabled: false,
      backend_mode_enabled: false,
      password_reset_enabled: false,
      passkey_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: true,
      google_one_tap_enabled: true,
      google_oauth_client_id: 'google-client',
      login_agreement_enabled: true,
      login_agreement_mode: 'modal',
      login_agreement_revision: 'revision-1',
      login_agreement_documents: [
        {
          id: 'terms',
          title: 'Terms',
          content_md: 'Terms content',
        },
      ],
    })

    const wrapper = mountLogin()
    await flushPromises()
    expect(wrapper.get('[data-testid="google-one-tap"]').attributes('data-enabled')).toBe('false')

    await wrapper.get('[data-testid="accept-login-agreement"]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[data-testid="google-one-tap"]').attributes('data-enabled')).toBe('true')
  })

  // The user starts typing password-login credentials while One Tap is mounted.
  // The page must call the exposed prompt cancellation method as those fields
  // change.
  it('cancels One Tap when the user starts entering a password login', async () => {
    getPublicSettingsMock.mockResolvedValue({
      turnstile_enabled: false,
      tencent_captcha_enabled: false,
      aliyun_captcha_enabled: false,
      backend_mode_enabled: false,
      password_reset_enabled: false,
      passkey_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: true,
      google_one_tap_enabled: true,
      google_oauth_client_id: 'google-client',
    })
    const wrapper = mountLogin()
    await flushPromises()

    await wrapper.get('#email').setValue('user@example.com')
    expect(oneTapCancelMock).toHaveBeenCalled()
  })
})
