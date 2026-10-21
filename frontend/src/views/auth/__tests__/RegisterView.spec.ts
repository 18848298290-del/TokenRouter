import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '@/views/auth/RegisterView.vue'

// RegisterView.spec.ts checks registration referral fields, deferred invitation
// validation, and email-domain admission. Mounted forms use controlled endpoint
// responses so the assertions distinguish local rejection from backend quota
// enforcement and detect duplicate validation toasts.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const {
  pushMock,
  showSuccessMock,
  showErrorMock,
  registerMock,
  getPublicSettingsMock,
  validatePromoCodeMock,
  validateInvitationCodeMock,
  formatBalanceAmountMock,
  routeState,
} = vi.hoisted(() => ({
  pushMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  registerMock: vi.fn(),
  getPublicSettingsMock: vi.fn(),
  validatePromoCodeMock: vi.fn(),
  validateInvitationCodeMock: vi.fn(),
  formatBalanceAmountMock: vi.fn(),
  routeState: {
    query: {} as Record<string, unknown>,
  },
})) // hoisted dependency and routing fixtures

vi.mock('vue-router', () => ({
  // useRouter supplies observable navigation handlers for the mounted
  // authentication component.
  useRouter: () => ({
    push: pushMock,
  }),
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => routeState,
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    // useI18n supplies the translation fixture used by rendered authentication
    // labels.
    useI18n: () => ({
      // t returns translation keys directly for stable component text assertions.
      t: (key: string) => key,
      locale: { value: 'zh-CN' },
    }),
  }
})

vi.mock('@/stores', () => ({
  // useAuthStore exposes the mocked session state and authentication actions
  // consumed by the component.
  useAuthStore: () => ({
    // register records signup credentials and attribution for the registration
    // assertions.
    register: (...args: any[]) => registerMock(...args),
  }),
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    // showSuccess records completion feedback delivered through the app toast
    // service.
    showSuccess: (...args: any[]) => showSuccessMock(...args),
    // showError records failure feedback delivered through the app toast service.
    showError: (...args: any[]) => showErrorMock(...args),
  }),
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    // getPublicSettings returns the configured public policy response for the
    // current case.
    getPublicSettings: (...args: any[]) => getPublicSettingsMock(...args),
    // validatePromoCode returns the controlled validation response for the entered
    // promotion.
    validatePromoCode: (...args: any[]) => validatePromoCodeMock(...args),
    // validateInvitationCode returns the controlled validation response for the
    // entered invitation.
    validateInvitationCode: (...args: any[]) => validateInvitationCodeMock(...args),
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  // useBalanceDisplay supplies the promotion-bonus formatter used by the
  // registration page.
  useBalanceDisplay: () => ({
    // formatBalanceAmount renders promotion bonus values through the configured
    // display fixture.
    formatBalanceAmount: (...args: any[]) => formatBalanceAmountMock(...args),
  }),
}))

// Deferred exposes a promise and its settlement controls to asynchronous tests.
type Deferred<T> = {
  // promise, resolve, and reject belong to one deferred validation result. Tests
  // keep promise pending during intermediate assertions and settle that same
  // instance through its returned controls.
  promise: Promise<T> // pending controlled test promise

  resolve: (value: T) => void // test-controlled promise resolution

  reject: (reason?: unknown) => void // test-controlled promise rejection
}

// createDeferred returns a promise with externally controlled resolve and reject
// functions. Tests settle it after intermediate assertions to reproduce requests
// that remain pending while the user starts another action.
function createDeferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

// mountView renders registration with external layout and provider controls
// stubbed. It waits for initial promises before returning the wrapper so callers
// can interact with settings-dependent form fields.
async function mountView() {
  const wrapper = mount(RegisterView, {
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        LinuxDoOAuthSection: true,
        WechatOAuthSection: true,
        OidcOAuthSection: true,
        TurnstileWidget: true,
        Icon: true,
        RouterLink: { template: '<a><slot /></a>' },
        transition: false,
      },
    },
  })

  await flushPromises()
  return wrapper
}

// fillRequiredFields enters valid email and password values through registration
// inputs.
async function fillRequiredFields(wrapper: ReturnType<typeof mount>) {
  await wrapper.get('#email').setValue('alice@example.com')
  await wrapper.get('#password').setValue('secret123')
}

describe('RegisterView', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation.
  beforeEach(() => {
    vi.useFakeTimers()
    pushMock.mockReset().mockResolvedValue(undefined)
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
    registerMock.mockReset().mockResolvedValue(undefined)
    getPublicSettingsMock.mockReset().mockResolvedValue({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: true,
      affiliate_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      wechat_oauth_open_enabled: false,
      wechat_oauth_mp_enabled: false,
      oidc_oauth_enabled: false,
      oidc_oauth_provider_name: 'OIDC',
      registration_email_suffix_whitelist: [],
    })
    validatePromoCodeMock.mockReset()
    validateInvitationCodeMock.mockReset()
    formatBalanceAmountMock
      .mockReset()
      .mockImplementation((amount: number) => `Balance ${amount.toFixed(2)}`)
    routeState.query = {}
    sessionStorage.clear()
  })

  // Restore the browser or timer fixtures changed by this case after its
  // assertions, keeping those process-level replacements from affecting the next
  // authentication scenario.
  afterEach(() => {
    vi.useRealTimers()
  })

  // Public settings enable optional referral attribution without mandatory
  // invitations. The registration page must label that field optional and place it
  // before the captcha controls.
  it('在 Turnstile 前展示可选推广邀请码输入框', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: false,
      affiliate_enabled: true,
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      oidc_oauth_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: false,
      registration_email_suffix_whitelist: [],
    })

    const wrapper = await mountView()
    const invitationField = wrapper.get('[data-testid="affiliate-invitation-field"]')
    const turnstile = wrapper.get('[data-testid="registration-turnstile"]')

    expect(invitationField.get('input').attributes('id')).toBe('affiliate_code')
    expect(invitationField.text()).toContain('common.optional')
    expect(
      invitationField.element.compareDocumentPosition(turnstile.element) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy()
  })

  // Public settings enable mandatory invitation codes. The registration page must
  // show the required invitation input and omit the separate optional
  // referral-invitation field from that layout.
  it('启用强制邀请码时不重复展示推广邀请码输入框', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: true,
      affiliate_enabled: true,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      oidc_oauth_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: false,
      registration_email_suffix_whitelist: [],
    })

    const wrapper = await mountView()

    expect(wrapper.find('[data-testid="affiliate-invitation-field"]').exists()).toBe(false)
    expect(wrapper.get('#invitation_code').exists()).toBe(true)
  })

  // Invitation validation is still pending when the user submits registration. The
  // endpoint must remain uncalled until the deferred validation succeeds, after
  // which the form can register and navigate.
  it('waits for a pending invitation validation before completing registration', async () => {
    const invitationValidation = createDeferred<{
      valid: boolean // server validation result flag
      error_code?: string // server validation failure code
    }>()
    validateInvitationCodeMock.mockReturnValueOnce(invitationValidation.promise)

    const wrapper = await mountView()
    await fillRequiredFields(wrapper)
    await wrapper.get('#invitation_code').setValue('INVITE123')

    // Hold invitation validation with an explicitly settled promise so the test
    // can submit registration before the validation response becomes available.
    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()

    expect(validateInvitationCodeMock).toHaveBeenCalledWith('INVITE123')

    const submitPromise = wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).not.toHaveBeenCalled()
    expect(showErrorMock).not.toHaveBeenCalled()

    invitationValidation.resolve({ valid: true })
    await flushPromises()
    await submitPromise
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith({
      email: 'alice@example.com',
      password: 'secret123',
      turnstile_token: undefined,
      promo_code: undefined,
      invitation_code: 'INVITE123',
      aff_code: undefined,
    })
    expect(showErrorMock).not.toHaveBeenCalled()
    expect(pushMock).toHaveBeenCalledWith('/dashboard')
  })

  // A pending invitation check fails while registration waits for its result. The
  // page must avoid registration and navigation and show only one validation error
  // toast.
  it('shows only one error toast when the invitation becomes invalid during submit', async () => {
    const invitationValidation = createDeferred<{
      valid: boolean // server validation result flag
      error_code?: string // server validation failure code
    }>()
    validateInvitationCodeMock.mockReturnValueOnce(invitationValidation.promise)

    const wrapper = await mountView()
    await fillRequiredFields(wrapper)
    await wrapper.get('#invitation_code').setValue('INVITE123')

    await vi.advanceTimersByTimeAsync(500)
    await flushPromises()

    const submitPromise = wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).not.toHaveBeenCalled()

    invitationValidation.resolve({
      valid: false,
      error_code: 'INVITATION_CODE_INVALID',
    })
    await flushPromises()
    await submitPromise
    await flushPromises()

    expect(registerMock).not.toHaveBeenCalled()
    expect(pushMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledTimes(1)
    expect(['auth.invitationCodeInvalid', 'auth.invitationCodeInvalidCannotRegister']).toContain(
      showErrorMock.mock.calls[0]?.[0],
    )
  })

  // Domain quota enforcement is enabled for registration. An address outside the
  // local whitelist must reach the backend registration call without an immediate
  // client-side rejection toast.
  it('submits a non-whitelist domain for backend quota enforcement', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: false,
      affiliate_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      oidc_oauth_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: false,
      registration_email_suffix_whitelist: ['@allowed.example'],
      registration_email_domain_quota_enabled: true,
    })

    const wrapper = await mountView()
    await wrapper.get('#email').setValue('first@custom.example')
    await wrapper.get('#password').setValue('secret123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'first@custom.example' }),
    )
    expect(showErrorMock).not.toHaveBeenCalled()
  })

  // The backend rejects signup because the email domain has reached its quota. The
  // registration page must expose the dedicated translated limit message in its
  // error toast.
  it('localizes the backend email domain quota error', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: false,
      affiliate_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      oidc_oauth_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: false,
      registration_email_suffix_whitelist: ['@allowed.example'],
      registration_email_domain_quota_enabled: true,
    })
    registerMock.mockRejectedValueOnce({
      reason: 'EMAIL_DOMAIN_REGISTRATION_LIMIT',
      message: 'raw backend message',
    })

    const wrapper = await mountView()
    await wrapper.get('#email').setValue('second@custom.example')
    await wrapper.get('#password').setValue('secret123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(showErrorMock).toHaveBeenCalledWith('auth.emailDomainRegistrationLimit')
  })

  // Quota enforcement is absent and the address is outside the whitelist. The page
  // must avoid registration, mark the email input invalid, and show allowed-suffix
  // feedback.
  it('rejects a non-whitelist domain locally when quota is disabled by default', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: false,
      affiliate_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      oidc_oauth_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: false,
      registration_email_suffix_whitelist: ['@allowed.example'],
    })

    const wrapper = await mountView()
    await wrapper.get('#email').setValue('first@custom.example')
    await wrapper.get('#password').setValue('secret123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('auth.emailSuffixNotAllowedWithAllowed')
    expect(wrapper.get('#email').classes()).toContain('input-error')
  })

  // Quota enforcement is disabled but the submitted address matches an allowed
  // suffix. The registration endpoint must receive that address after ordinary
  // required-field validation passes.
  it('submits a whitelisted domain when quota is disabled', async () => {
    getPublicSettingsMock.mockResolvedValueOnce({
      registration_enabled: true,
      email_verify_enabled: false,
      promo_code_enabled: false,
      invitation_code_enabled: false,
      affiliate_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
      site_name: 'TokenRouter',
      linuxdo_oauth_enabled: false,
      wechat_oauth_enabled: false,
      oidc_oauth_enabled: false,
      github_oauth_enabled: false,
      google_oauth_enabled: false,
      registration_email_suffix_whitelist: ['@allowed.example'],
    })

    const wrapper = await mountView()
    await wrapper.get('#email').setValue('user@allowed.example')
    await wrapper.get('#password').setValue('secret123')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'user@allowed.example' }),
    )
  })
})
