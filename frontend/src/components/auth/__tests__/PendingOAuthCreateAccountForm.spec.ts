import { defineComponent, h } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import PendingOAuthCreateAccountForm from '../PendingOAuthCreateAccountForm.vue'

// PendingOAuthCreateAccountForm.spec.ts checks the unfinished OAuth creation form
// with mocked settings and email-code endpoints. The cases cover emitted
// credentials, invitation and verification controls, consumed proofs, and retry
// gates without submitting a real account creation request.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const sendVerifyCode = vi.fn() // test-controlled send verify code fixture
const sendPendingOAuthVerifyCode = vi.fn() // test-controlled send pending oauth verify code fixture
const getPublicSettings = vi.fn() // test-controlled get public settings fixture
const showError = vi.fn() // test-controlled show error fixture
const turnstileReset = vi.fn() // test-controlled turnstile reset fixture
const verifyAction = vi.fn() // test-controlled verify action fixture

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

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    // sendVerifyCode records ordinary signup email delivery requests and their
    // proofs.
    sendVerifyCode: (...args: any[]) => sendVerifyCode(...args),
    // sendPendingOAuthVerifyCode records email delivery requests for the pending
    // provider session.
    sendPendingOAuthVerifyCode: (...args: any[]) => sendPendingOAuthVerifyCode(...args),
    // getPublicSettings returns the configured public policy response for the
    // current case.
    getPublicSettings: (...args: any[]) => getPublicSettings(...args),
  }
})

vi.mock('@/stores', () => ({
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    showError,
  }),
}))

describe('PendingOAuthCreateAccountForm', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case.
  beforeEach(() => {
    sendVerifyCode.mockReset()
    sendPendingOAuthVerifyCode.mockReset()
    getPublicSettings.mockReset()
    showError.mockReset()
    turnstileReset.mockReset()
    verifyAction.mockReset()
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: false,
      turnstile_site_key: '',
    })
  })

  // The user sends an email code and then submits pending account creation. The
  // fixture supplies distinct action proofs and checks separate verification and
  // reset calls.
  it('acquires separate proofs for pending OAuth send-code and create-account', async () => {
    getPublicSettings.mockResolvedValue({
      email_verify_enabled: true,
      turnstile_enabled: false,
      turnstile_site_key: '',
      tencent_captcha_enabled: true,
      tencent_captcha_app_id: 'tencent-app-id',
    })
    sendPendingOAuthVerifyCode.mockResolvedValue({ countdown: 0 })
    verifyAction
      .mockResolvedValueOnce({ token: 'ticket-1', randstr: '@rand-1' })
      .mockResolvedValueOnce({ token: 'ticket-2', randstr: '@rand-2' })
    const CaptchaChallengeStub = defineComponent({
      // setup exposes the fixture controls expected by the parent component. The
      // rendered placeholder omits external SDK behavior, letting tests drive
      // verification, cancellation, or reset through the configured mock
      // functions.
      setup(_, { expose }) {
        expose({ verifyAction, reset: turnstileReset })
        return () => h('div')
      },
    })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'oidc',
        initialEmail: 'user@example.com',
        isSubmitting: false,
      },
      global: {
        stubs: { TurnstileWidget: CaptchaChallengeStub },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="oidc-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="oidc-create-account-verify-code"]').setValue('246810')
    await wrapper.get('[data-testid="oidc-create-account-send-code"]').trigger('click')
    await flushPromises()
    await wrapper.get('[data-testid="oidc-create-account-submit"]').trigger('click')
    await flushPromises()

    expect(verifyAction).toHaveBeenCalledTimes(2)
    expect(sendPendingOAuthVerifyCode).toHaveBeenCalledWith({
      email: 'user@example.com',
      tencent_captcha_ticket: 'ticket-1',
      tencent_captcha_randstr: '@rand-1',
    })
    expect(wrapper.emitted('submit')).toEqual([
      [
        expect.objectContaining({
          tencentCaptchaTicket: 'ticket-2',
          tencentCaptchaRandstr: '@rand-2',
        }),
      ],
    ])
    expect(turnstileReset).toHaveBeenCalledTimes(2)
  })

  // The pending creation form receives an email, password, and verification code.
  // Submission must trim the address and code while preserving the password in its
  // emitted payload.
  it('emits trimmed email, password, and verify code on submit', async () => {
    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        providerName: 'LinuxDo',
        testIdPrefix: 'linuxdo',
        initialEmail: 'prefill@example.com',
        isSubmitting: false,
      },
    })

    await wrapper
      .get('[data-testid="linuxdo-create-account-email"]')
      .setValue('  user@example.com  ')
    await wrapper.get('[data-testid="linuxdo-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="linuxdo-create-account-verify-code"]').setValue(' 246810 ')
    await wrapper.get('form').trigger('submit.prevent')

    expect(wrapper.emitted('submit')).toEqual([
      [
        {
          email: 'user@example.com',
          password: 'secret-123',
          verifyCode: '246810',
        },
      ],
    ])
  })

  // The pending creation form is mounted with the translation fixture. Its
  // creation and existing-account actions must display their translation keys
  // through the component labels.
  it('renders action labels through i18n keys', () => {
    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'linuxdo',
        initialEmail: '',
        isSubmitting: false,
      },
    })

    expect(wrapper.text()).toContain('auth.createAccount')
    expect(wrapper.text()).toContain('auth.alreadyHaveAccount')
  })

  // Public settings disable email verification for the pending signup. The code
  // input and send button must disappear, and the emitted creation payload must
  // carry an empty verification code.
  it('hides email verification controls when public settings disable email verification', async () => {
    getPublicSettings.mockResolvedValue({
      email_verify_enabled: false,
      turnstile_enabled: false,
      turnstile_site_key: '',
    })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'linuxdo',
        initialEmail: 'prefill@example.com',
        isSubmitting: false,
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="linuxdo-create-account-password"]').setValue('secret-123')
    await wrapper.get('form').trigger('submit.prevent')

    expect(wrapper.find('[data-testid="linuxdo-create-account-verify-code"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="linuxdo-create-account-send-code"]').exists()).toBe(false)
    expect(wrapper.emitted('submit')).toEqual([
      [
        {
          email: 'prefill@example.com',
          password: 'secret-123',
          verifyCode: '',
        },
      ],
    ])
  })

  // Public settings require an invitation for pending signup. The form must expose
  // invitation entry and include its trimmed value in the emitted creation
  // payload.
  it('shows and emits invitation code when invitation-only signup is enabled', async () => {
    getPublicSettings.mockResolvedValue({
      invitation_code_enabled: true,
      email_verify_enabled: true,
      turnstile_enabled: false,
      turnstile_site_key: '',
    })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        providerName: 'LinuxDo',
        testIdPrefix: 'linuxdo',
        initialEmail: 'prefill@example.com',
        isSubmitting: false,
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="linuxdo-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="linuxdo-create-account-verify-code"]').setValue('246810')
    await wrapper
      .get('[data-testid="linuxdo-create-account-invitation-code"]')
      .setValue(' INVITE123 ')
    await wrapper.get('form').trigger('submit.prevent')

    expect(wrapper.emitted('submit')).toEqual([
      [
        {
          email: 'prefill@example.com',
          password: 'secret-123',
          verifyCode: '246810',
          invitationCode: 'INVITE123',
        },
      ],
    ])
  })

  // The user requests a code for an address with surrounding whitespace. The
  // pending OAuth verification endpoint must receive the trimmed email in its
  // request.
  it('sends a verify code for the trimmed email value', async () => {
    sendPendingOAuthVerifyCode.mockResolvedValue({
      message: 'sent',
      countdown: 60,
    })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        providerName: 'LinuxDo',
        testIdPrefix: 'linuxdo',
        initialEmail: '',
        isSubmitting: false,
      },
    })

    await wrapper
      .get('[data-testid="linuxdo-create-account-email"]')
      .setValue('  user@example.com  ')
    await wrapper.get('[data-testid="linuxdo-create-account-send-code"]').trigger('click')
    await flushPromises()

    expect(sendPendingOAuthVerifyCode).toHaveBeenCalledWith({
      email: 'user@example.com',
    })
  })

  // The pending email-code endpoint rejects the request with an error. The form
  // must send that message to the app toast without duplicating it inside its
  // content.
  it('shows send-code failures via toast without rendering inline error text', async () => {
    sendPendingOAuthVerifyCode.mockRejectedValue(new Error('send failed'))

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'linuxdo',
        initialEmail: '',
        isSubmitting: false,
      },
    })

    await wrapper.get('[data-testid="linuxdo-create-account-email"]').setValue('user@example.com')
    await wrapper.get('[data-testid="linuxdo-create-account-send-code"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('send failed')
    expect(wrapper.text()).not.toContain('send failed')
  })

  // Sending the pending email code fails after a proof is supplied. The widget
  // must reset and submission must remain disabled until another proof becomes
  // available.
  it('consumes the captcha proof when sending a verify code fails', async () => {
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
    })
    sendPendingOAuthVerifyCode.mockRejectedValue(new Error('send failed'))

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'oidc',
        initialEmail: 'user@example.com',
        isSubmitting: false,
      },
      global: {
        stubs: {
          TurnstileWidget: {
            template:
              '<button data-testid="turnstile-verify" @click="$emit(\'verify\', \'proof-token\')">verify</button>',
            methods: { reset: turnstileReset },
          },
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="turnstile-verify"]').trigger('click')
    await wrapper.get('[data-testid="oidc-create-account-send-code"]').trigger('click')
    await flushPromises()

    expect(turnstileReset).toHaveBeenCalledOnce()
    expect(
      wrapper.get('[data-testid="oidc-create-account-send-code"]').attributes('disabled'),
    ).toBeDefined()
  })

  // Turnstile is enabled while the email draft is already present. Sending must
  // remain gated until verification supplies a token, which then reaches the
  // pending endpoint.
  it('requires a turnstile token before sending a verify code when turnstile is enabled', async () => {
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
    })
    sendPendingOAuthVerifyCode.mockResolvedValue({
      message: 'sent',
      countdown: 60,
    })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        providerName: 'LinuxDo',
        testIdPrefix: 'linuxdo',
        initialEmail: '',
        isSubmitting: false,
      },
      global: {
        stubs: {
          TurnstileWidget: {
            template:
              '<button data-testid="turnstile-verify" @click="$emit(\'verify\', \'turnstile-token\')">verify</button>',
            methods: { reset: vi.fn() },
          },
        },
      },
    })

    await flushPromises()
    await wrapper
      .get('[data-testid="linuxdo-create-account-email"]')
      .setValue('  user@example.com  ')

    expect(
      wrapper.get('[data-testid="linuxdo-create-account-send-code"]').attributes('disabled'),
    ).toBeDefined()

    await wrapper.get('[data-testid="turnstile-verify"]').trigger('click')
    await wrapper.get('[data-testid="linuxdo-create-account-send-code"]').trigger('click')
    await flushPromises()

    expect(sendPendingOAuthVerifyCode).toHaveBeenCalledWith({
      email: 'user@example.com',
      turnstile_token: 'turnstile-token',
    })
  })

  // The pending creation form has valid credentials but no Turnstile proof. Both
  // disabled-button and implicit-form paths must wait until a new proof enables
  // submission.
  it('requires a turnstile token before submitting when turnstile is enabled', async () => {
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
    })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'linuxdo',
        initialEmail: 'user@example.com',
        isSubmitting: false,
      },
      global: {
        stubs: {
          TurnstileWidget: {
            template:
              '<button data-testid="turnstile-verify" @click="$emit(\'verify\', \'turnstile-token\')">verify</button>',
            methods: { reset: turnstileReset },
          },
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="linuxdo-create-account-password"]').setValue('secret-123')

    expect(
      wrapper.get('[data-testid="linuxdo-create-account-submit"]').attributes('disabled'),
    ).toBeDefined()

    // Submit the form directly to bypass button disabling. The handler still needs
    // to reject the absent proof before emitting an account-creation request.
    await wrapper.get('form').trigger('submit.prevent')
    expect(wrapper.emitted('submit')).toBeUndefined()

    await wrapper.get('[data-testid="turnstile-verify"]').trigger('click')

    expect(
      wrapper.get('[data-testid="linuxdo-create-account-submit"]').attributes('disabled'),
    ).toBeUndefined()

    await wrapper.get('[data-testid="linuxdo-create-account-submit"]').trigger('click')

    expect(wrapper.emitted('submit')).toEqual([
      [
        {
          email: 'user@example.com',
          password: 'secret-123',
          verifyCode: '',
          turnstileToken: 'turnstile-token',
          invitationCode: undefined,
        },
      ],
    ])
  })

  // Sending an email code consumes the prepared Turnstile proof. The form must
  // reject another submission until a replacement proof arrives and then emit that
  // replacement token.
  it('blocks submit again after sending a verify code consumes the turnstile proof', async () => {
    getPublicSettings.mockResolvedValue({
      turnstile_enabled: true,
      turnstile_site_key: 'site-key',
    })
    sendPendingOAuthVerifyCode.mockResolvedValue({ message: 'sent', countdown: 60 })

    const wrapper = mount(PendingOAuthCreateAccountForm, {
      props: {
        testIdPrefix: 'linuxdo',
        initialEmail: 'user@example.com',
        isSubmitting: false,
      },
      global: {
        stubs: {
          TurnstileWidget: {
            template:
              '<button data-testid="turnstile-verify" @click="$emit(\'verify\', \'turnstile-token\')">verify</button>',
            methods: { reset: turnstileReset },
          },
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="linuxdo-create-account-password"]').setValue('secret-123')
    await wrapper.get('[data-testid="turnstile-verify"]').trigger('click')
    await wrapper.get('[data-testid="linuxdo-create-account-send-code"]').trigger('click')
    await flushPromises()

    // Code delivery consumes this proof and resets the widget. Submission must
    // remain unavailable until a later verification callback supplies another
    // token.
    expect(turnstileReset).toHaveBeenCalled()
    expect(
      wrapper.get('[data-testid="linuxdo-create-account-submit"]').attributes('disabled'),
    ).toBeDefined()

    await wrapper.get('form').trigger('submit.prevent')
    expect(wrapper.emitted('submit')).toBeUndefined()

    // A new verification callback restores the guarded action. Its replacement
    // token must be the value emitted in the next account-creation payload.
    await wrapper.get('[data-testid="turnstile-verify"]').trigger('click')
    await wrapper.get('[data-testid="linuxdo-create-account-submit"]').trigger('click')

    expect(wrapper.emitted('submit')?.[0]?.[0]).toMatchObject({
      turnstileToken: 'turnstile-token',
    })
  })
})
