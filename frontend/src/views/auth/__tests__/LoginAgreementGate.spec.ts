import { nextMotionFrame } from '@/__tests__/helpers/motion'
import { defineComponent, h } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import LoginView from '@/views/auth/LoginView.vue'
import RegisterView from '@/views/auth/RegisterView.vue'
import { LOGIN_AGREEMENT_STORAGE_KEY } from '@/utils/loginAgreement'

// LoginAgreementGate.spec.ts checks consent gating across password, passkey, and
// provider actions on login and registration pages. Real agreement controls and
// tooltips run with external authentication replacements, so tests observe input
// availability, revision persistence, and whether captcha or navigation starts.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const mocks = vi.hoisted(() => ({
  settings: vi.fn(),
  login: vi.fn(),
  register: vi.fn(),
  passkey: vi.fn(),
  startOAuth: vi.fn(),
  verify: vi.fn(),
  warning: vi.fn(),
  error: vi.fn(),
  location: { href: 'http://localhost/login', protocol: 'http:', hostname: 'localhost' },
})) // hoisted dependency and routing fixtures

vi.mock('vue-router', () => ({
  // useRouter supplies observable navigation handlers for the mounted
  // authentication component.
  useRouter: () => ({ currentRoute: { value: { query: {} } }, push: vi.fn() }),
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => ({ query: {} }),
}))
vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  // useI18n supplies the translation fixture used by rendered authentication
  // labels.
  useI18n: () => ({
    // t returns translation keys directly for stable component text assertions.
    t: (key: string) => key,
    locale: { value: 'zh' },
  }),
}))
vi.mock('@/stores', () => ({
  // useAuthStore exposes the mocked session state and authentication actions
  // consumed by the component.
  useAuthStore: () => ({
    login: mocks.login,
    register: mocks.register,
    loginWithPasskey: mocks.passkey,
    isAuthenticated: false,
  }),
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    showError: mocks.error,
    showWarning: mocks.warning,
    showSuccess: vi.fn(),
    publicSettingsLoaded: true,
  }),
}))
vi.mock('@/api/auth', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/auth')>()),
  getPublicSettings: mocks.settings,
  startOAuthLogin: mocks.startOAuth,
  // isWeChatWebOAuthEnabled disables the unrelated WeChat entry in these login
  // fixtures.
  isWeChatWebOAuthEnabled: () => false,
}))
vi.mock('@/composables/useBalanceDisplay', () => ({
  // useBalanceDisplay supplies the promotion-bonus formatter used by the
  // registration page.
  useBalanceDisplay: () => ({
    // formatBalanceAmount renders promotion bonus values through the configured
    // display fixture.
    formatBalanceAmount: (value: number) => String(value),
  }),
}))

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const CaptchaStub = defineComponent({
  // setup exposes the fixture controls expected by the parent component. The
  // rendered placeholder omits external SDK behavior, letting tests drive
  // verification, cancellation, or reset through the configured mock functions.
  setup(_, { expose }) {
    expose({ verifyAction: mocks.verify, reset: vi.fn() })
    return () => h('div')
  },
}) // test-controlled captcha stub fixture
const OneTapStub = defineComponent({
  // setup exposes the fixture controls expected by the parent component. The
  // rendered placeholder omits external SDK behavior, letting tests drive
  // verification, cancellation, or reset through the configured mock functions.
  setup(_, { expose }) {
    expose({ cancelPrompt: vi.fn() })
    return () => h('div')
  },
}) // test-controlled one tap stub fixture

const baseSettings = {
  registration_enabled: true,
  email_verify_enabled: false,
  invitation_code_enabled: false,
  registration_email_suffix_whitelist: [],
  turnstile_enabled: false,
  github_oauth_enabled: true,
  google_oauth_enabled: true,
  linuxdo_oauth_enabled: true,
  dingtalk_oauth_enabled: true,
  oidc_oauth_enabled: true,
  oidc_oauth_provider_name: 'OIDC',
  passkey_enabled: true,
  login_agreement_enabled: true,
  login_agreement_mode: 'checkbox',
  login_agreement_revision: 'current-revision',
  login_agreement_documents: [{ id: 'terms', title: '服务条款', content: '测试条款' }],
} // test-controlled base settings fixture

let wrapper: ReturnType<typeof mount> | undefined // test-controlled wrapper fixture
// mountPage renders the requested authentication page attached to document.body.
// It waits for startup promises and stores the wrapper for teardown, allowing
// tests to inspect teleported agreement reminders.
async function mountPage(page: 'login' | 'register') {
  wrapper = mount(page === 'login' ? LoginView : RegisterView, {
    attachTo: document.body,
    global: {
      stubs: {
        AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
        RouterLink: { template: '<a><slot /></a>' },
        GoogleOneTap: OneTapStub,
        TurnstileWidget: CaptchaStub,
        Icon: true,
      },
    },
  })
  await flushPromises()
  return wrapper
}

// hintVisible reports whether the teleported agreement reminder is currently
// displayed.
function hintVisible() {
  const hint = document.getElementById('login-agreement-hint')
  return !!hint && hint.style.display !== 'none'
}

describe.each(['login', 'register'] as const)('%s 协议提交门禁', (page) => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case. Clear
  // stored authentication or registration values before seeding the next
  // continuation. Replace browser globals with the controllable objects used by
  // the mounted view.
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    sessionStorage.clear()
    vi.stubGlobal('PublicKeyCredential', class {})
    vi.stubGlobal('location', mocks.location)
    mocks.location.href = `http://localhost/${page}`
    mocks.settings.mockResolvedValue({ ...baseSettings })
    mocks.verify.mockResolvedValue({ token: 'proof', randstr: 'random' })
    mocks.startOAuth.mockResolvedValue({ authorize_url: 'https://example.com/oauth' })
    mocks.login.mockResolvedValue({})
    mocks.register.mockResolvedValue({})
  })

  // Release this case component and browser fixtures after its assertions so later
  // tests cannot observe retained DOM, timers, or overridden global state.
  afterEach(() => {
    wrapper?.unmount()
    wrapper = undefined
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  // Checkbox consent is missing on both authentication pages. Inputs must remain
  // editable, and submitting incomplete fields must prompt for agreement before
  // validation or captcha starts.
  it('未勾选可填写，空表单与未完成验证码也优先提示协议', async () => {
    mocks.settings.mockResolvedValue({
      ...baseSettings,
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
    })
    const view = await mountPage(page)
    expect(view.get('#email').attributes('disabled')).toBeUndefined()
    expect(view.get('#password').attributes('disabled')).toBeUndefined()
    expect(view.get('button[type="submit"]').attributes('disabled')).toBeUndefined()
    expect(view.get('form').attributes('novalidate')).toBeDefined()
    await nextMotionFrame()
    expect(hintVisible()).toBe(false)
    await view.get('button[type="submit"]').trigger('click')
    await flushPromises()
    expect(hintVisible()).toBe(true)
    expect(mocks.error).not.toHaveBeenCalled()
    expect(mocks.warning).not.toHaveBeenCalled()
    expect(mocks.login).not.toHaveBeenCalled()
    expect(mocks.register).not.toHaveBeenCalled()
    await view.get('#login-agreement-consent').setValue(true)
    await nextMotionFrame()
    expect(hintVisible()).toBe(false)
    expect(view.get('button[type="submit"]').attributes('disabled')).toBeDefined()
  })

  // The user accepts checkbox consent, submits, and then withdraws consent.
  // Submission follows the accepted flow while withdrawal leaves inputs editable
  // without immediately displaying the reminder.
  it('勾选后按原流程提交，取消勾选不禁用输入或主动弹提示', async () => {
    const view = await mountPage(page)
    await view.get('#email').setValue('test@example.com')
    await view.get('#password').setValue('test-password')
    await view.get('form').trigger('submit')
    expect(hintVisible()).toBe(true)
    await view.get('#login-agreement-consent').setValue(true)
    await nextMotionFrame()
    expect(hintVisible()).toBe(false)
    await view.get('form').trigger('submit')
    await flushPromises()
    expect(page === 'login' ? mocks.login : mocks.register).toHaveBeenCalledTimes(1)
    await view.get('#login-agreement-consent').setValue(false)
    expect(view.get('#email').attributes('disabled')).toBeUndefined()
    await nextMotionFrame()
    expect(hintVisible()).toBe(false)
    expect(mocks.warning).not.toHaveBeenCalled()
  })

  // Each listed provider action starts without checkbox consent. The page must
  // show the agreement reminder before invoking captcha verification or requesting
  // a provider authorization URL.
  it.each(['GitHub', 'Google', 'auth.linuxdo.signIn', 'auth.oidc.signIn'])(
    '%s 在验证码和跳转前统一拦截',
    async (label) => {
      mocks.settings.mockResolvedValue({
        ...baseSettings,
        tencent_captcha_enabled: true,
        tencent_captcha_app_id: 'app-id',
      })
      const view = await mountPage(page)
      const button = view.findAll('button').find((candidate) => candidate.text().includes(label))!
      expect(button.exists()).toBe(true)
      expect(button.attributes('disabled')).toBeUndefined()
      await button.trigger('click')
      await flushPromises()
      expect(hintVisible()).toBe(true)
      expect(mocks.verify).not.toHaveBeenCalled()
      expect(mocks.startOAuth).not.toHaveBeenCalled()
      expect(mocks.location.href).toBe(`http://localhost/${page}`)
      document.body.dispatchEvent(new Event('touchstart', { bubbles: true }))
      await flushPromises()
      await nextMotionFrame()
      expect(hintVisible()).toBe(false)
      await button.trigger('click')
      expect(hintVisible()).toBe(true)
      await view.get('#login-agreement-consent').setValue(true)
      await button.trigger('click')
      await flushPromises()
      expect(mocks.verify).toHaveBeenCalledTimes(1)
      expect(mocks.startOAuth).toHaveBeenCalledTimes(1)
    },
  )

  // Storage contains consent for a particular agreement revision. The same
  // revision must allow authentication while a later revision requires a new
  // acceptance before the action proceeds.
  it('已有同版本同意记录直接放行，新版本需要重新同意', async () => {
    localStorage.setItem(LOGIN_AGREEMENT_STORAGE_KEY, JSON.stringify({ revision: 'old-revision' }))
    const view = await mountPage(page)
    await view.get('form').trigger('submit')
    expect(hintVisible()).toBe(true)
    view.unmount()
    localStorage.setItem(
      LOGIN_AGREEMENT_STORAGE_KEY,
      JSON.stringify({ revision: 'current-revision' }),
    )
    const acceptedView = await mountPage(page)
    expect((acceptedView.get('#login-agreement-consent').element as HTMLInputElement).checked).toBe(
      true,
    )
    expect(acceptedView.get('form').attributes('novalidate')).toBeUndefined()
  })

  // A previously stored consent record is revoked through the page. Remounting
  // must keep the agreement gate active so the older brand storage cannot silently
  // restore acceptance.
  it('撤回旧品牌同意记录后，重新进入页面仍需要同意', async () => {
    localStorage.setItem(
      'sub2api_login_agreement_consent',
      JSON.stringify({ revision: 'current-revision' }),
    )
    const view = await mountPage(page)
    expect((view.get('#login-agreement-consent').element as HTMLInputElement).checked).toBe(true)

    await view.get('#login-agreement-consent').setValue(false)
    view.unmount()

    const reopenedView = await mountPage(page)
    expect((reopenedView.get('#login-agreement-consent').element as HTMLInputElement).checked).toBe(
      false,
    )
    await reopenedView.get('form').trigger('submit')
    expect(hintVisible()).toBe(true)
    expect(mocks.login).not.toHaveBeenCalled()
    expect(mocks.register).not.toHaveBeenCalled()

    await reopenedView.get('#login-agreement-consent').setValue(true)
    reopenedView.unmount()
    const acceptedView = await mountPage(page)
    expect((acceptedView.get('#login-agreement-consent').element as HTMLInputElement).checked).toBe(
      true,
    )
  })

  // Public settings select modal agreement presentation with missing consent.
  // Entering the page must show that dialog and keep authentication actions gated
  // until acceptance occurs.
  it('条款弹窗模式仍在进入页面时显示并保持原有门禁', async () => {
    mocks.settings.mockResolvedValue({ ...baseSettings, login_agreement_mode: 'modal' })
    const view = await mountPage(page)
    expect(document.body.textContent).toContain('legal.updateNotice')
    expect(view.get('#email').attributes('disabled')).toBeDefined()
    expect(view.get('#password').attributes('disabled')).toBeDefined()
    expect(view.find('#login-agreement-consent').exists()).toBe(false)
  })

  if (page === 'login') {
    // Passkey and DingTalk actions start while checkbox consent is missing. Each
    // must prompt for agreement first and leave its authentication or
    // authorization endpoint uncalled.
    it('Passkey 与钉钉入口也先提示协议', async () => {
      const view = await mountPage(page)
      for (const label of ['auth.passkeySignIn', 'auth.dingtalk.signIn']) {
        const button = view.findAll('button').find((candidate) => candidate.text().includes(label))!
        await button.trigger('click')
        expect(hintVisible()).toBe(true)
        document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
        await flushPromises()
        await nextMotionFrame()
        expect(hintVisible()).toBe(false)
      }
      expect(mocks.passkey).not.toHaveBeenCalled()
      expect(mocks.startOAuth).not.toHaveBeenCalled()
    })
  }
})
