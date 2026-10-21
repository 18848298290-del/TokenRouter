import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import TotpSetupModal from '@/components/user/profile/TotpSetupModal.vue'
import TotpDisableDialog from '@/components/user/profile/TotpDisableDialog.vue'

// totp-timer-cleanup.spec.ts exercises TOTP setup and disabling through their production
// dialogs with controlled verification APIs. Interval spies isolate resend cooldown
// ownership from animation scheduling, while additional cases inspect error notifications
// and full-code autofill distribution into confirmation cells.

// Hoisted verification and mutation mocks provide deterministic TOTP responses while
// notification spies capture the production dialogs' user-facing result handling.
const mocks = vi.hoisted(() => ({
  showSuccess: vi.fn(),
  showError: vi.fn(),
  getVerificationMethod: vi.fn(),
  sendVerifyCode: vi.fn(),
  initiateSetup: vi.fn(),
  enable: vi.fn(),
  disable: vi.fn(),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mocks.showSuccess,
    showError: mocks.showError,
  }),
}))

vi.mock('@/api', () => ({
  totpAPI: {
    getVerificationMethod: mocks.getVerificationMethod,
    sendVerifyCode: mocks.sendVerifyCode,
    initiateSetup: mocks.initiateSetup,
    enable: mocks.enable,
    disable: mocks.disable,
  },
}))

// These cases inspect authentication resend intervals; icon animation-frame behavior belongs to the icon interaction tests.
vi.mock('@/components/icons/Icon.vue', () => ({
  default: { template: '<svg aria-hidden="true" />' },
}))

// flushPromises advances the short resolved API chains used by these dialog fixtures. It
// leaves interval callbacks under their spies so assertions can inspect ownership without
// counting animation frames.
const flushPromises = async () => {
  await Promise.resolve()
  await Promise.resolve()
}

describe('TOTP 弹窗定时器清理', () => {
  // intervalSeed supplies distinct mock interval handles, and the interval spies let each
  // case match creation with the component's disposal call.
  let intervalSeed = 1000
  let setIntervalSpy: ReturnType<typeof vi.spyOn>
  let clearIntervalSpy: ReturnType<typeof vi.spyOn>

  beforeEach(() => {
    intervalSeed = 1000
    mocks.showSuccess.mockReset()
    mocks.showError.mockReset()
    mocks.getVerificationMethod.mockReset()
    mocks.sendVerifyCode.mockReset()
    mocks.initiateSetup.mockReset()
    mocks.enable.mockReset()
    mocks.disable.mockReset()

    mocks.getVerificationMethod.mockResolvedValue({ method: 'email' })
    mocks.sendVerifyCode.mockResolvedValue({ success: true })
    mocks.initiateSetup.mockResolvedValue({
      qr_code_url: 'otpauth://totp/TokenRouter:test?secret=ABC123',
      secret: 'ABC123',
      setup_token: 'setup-token',
    })
    mocks.enable.mockResolvedValue({ success: true })
    mocks.disable.mockResolvedValue({ success: true })

    setIntervalSpy = vi.spyOn(window, 'setInterval').mockImplementation(((
      handler: TimerHandler,
    ) => {
      void handler
      intervalSeed += 1
      return intervalSeed as unknown as number
    }) as typeof window.setInterval)
    clearIntervalSpy = vi.spyOn(window, 'clearInterval')
  })

  afterEach(() => {
    setIntervalSpy.mockRestore()
    clearIntervalSpy.mockRestore()
  })

  // The setup dialog successfully sends an email verification code before unmounting. The
  // test protects cleanup of its one-second resend interval by matching the stored timer
  // handle.
  it('TotpSetupModal 卸载时清理倒计时定时器', async () => {
    const wrapper = mount(TotpSetupModal)
    await flushPromises()

    const sendButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('profile.totp.sendCode'))

    expect(sendButton).toBeTruthy()
    await sendButton!.trigger('click')
    await flushPromises()

    // Dialog entrance motion can also schedule browser frames, so these assertions count the resend interval independently from animation scheduling.
    const cooldownCalls = setIntervalSpy.mock.calls
      .map((call, index) => ({ delay: call[1], id: setIntervalSpy.mock.results[index]?.value }))
      .filter((call) => call.delay === 1000)
    expect(cooldownCalls).toHaveLength(1)
    const timerId = cooldownCalls[0].id

    wrapper.unmount()

    expect(clearIntervalSpy).toHaveBeenCalledWith(timerId)
  })

  // The disable dialog successfully sends an email verification code before unmounting. The
  // test protects cleanup of its one-second resend interval independently from dialog motion
  // scheduling.
  it('TotpDisableDialog 卸载时清理倒计时定时器', async () => {
    const wrapper = mount(TotpDisableDialog)
    await flushPromises()

    const sendButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('profile.totp.sendCode'))

    expect(sendButton).toBeTruthy()
    await sendButton!.trigger('click')
    await flushPromises()

    // Dialog entrance motion can also schedule browser frames, so these assertions count the resend interval independently from animation scheduling.
    const cooldownCalls = setIntervalSpy.mock.calls
      .map((call, index) => ({ delay: call[1], id: setIntervalSpy.mock.results[index]?.value }))
      .filter((call) => call.delay === 1000)
    expect(cooldownCalls).toHaveLength(1)
    const timerId = cooldownCalls[0].id

    wrapper.unmount()

    expect(clearIntervalSpy).toHaveBeenCalledWith(timerId)
  })

  // Password verification reaches setup initiation and the API returns a specific failure
  // message. The test protects toast feedback and omission of duplicate inline error
  // content.
  it('TotpSetupModal 失败时改用 toast 并不渲染内联错误', async () => {
    mocks.getVerificationMethod.mockResolvedValue({ method: 'password' })
    mocks.initiateSetup.mockRejectedValue({
      response: { data: { message: 'setup failed' } },
    })

    const wrapper = mount(TotpSetupModal)
    await flushPromises()

    await wrapper.get('input[type="password"]').setValue('correct horse battery staple')
    await wrapper.get('button[type="button"].btn-primary').trigger('click')
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('setup failed')
    expect(wrapper.text()).not.toContain('setup failed')
    expect(wrapper.find('.bg-red-50').exists()).toBe(false)
  })

  // A password manager writes a complete six-digit code into the first confirmation cell.
  // The test protects distribution across visible cells and submission with the issued setup
  // token.
  it('TotpSetupModal 会拆分写入首格的完整 autofill 验证码', async () => {
    mocks.getVerificationMethod.mockResolvedValue({ method: 'password' })

    const wrapper = mount(TotpSetupModal)
    await flushPromises()

    await wrapper.get('input[type="password"]').setValue('correct horse battery staple')
    const firstNextButton = wrapper
      .findAll('button')
      .find((button) => button.text() === 'common.next')
    expect(firstNextButton).toBeTruthy()
    await firstNextButton!.trigger('click')
    await flushPromises()

    const secondNextButton = wrapper
      .findAll('button')
      .find((button) => button.text() === 'common.next')
    expect(secondNextButton).toBeTruthy()
    await secondNextButton!.trigger('click')
    await flushPromises()

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs[0].attributes('autocomplete')).toBe('one-time-code')
    expect(visibleInputs[0].attributes('pattern')).toBe('[0-9]{1,6}')

    const firstInput = visibleInputs[0].element as HTMLInputElement
    firstInput.value = '864209'
    await visibleInputs[0].trigger('change')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(visibleInputs.map((input) => (input.element as HTMLInputElement).value)).toEqual([
      '8',
      '6',
      '4',
      '2',
      '0',
      '9',
    ])
    expect(mocks.enable).toHaveBeenCalledWith({
      totp_code: '864209',
      setup_token: 'setup-token',
    })
  })

  // Password verification reaches TOTP disabling and the API returns a specific failure
  // message. The test protects toast feedback and omission of duplicate inline error
  // content.
  it('TotpDisableDialog 失败时改用 toast 并不渲染内联错误', async () => {
    mocks.getVerificationMethod.mockResolvedValue({ method: 'password' })
    mocks.disable.mockRejectedValue({
      response: { data: { message: 'disable failed' } },
    })

    const wrapper = mount(TotpDisableDialog)
    await flushPromises()

    await wrapper.get('input[type="password"]').setValue('correct horse battery staple')
    await wrapper.get('form').trigger('submit.prevent')
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('disable failed')
    expect(wrapper.text()).not.toContain('disable failed')
    expect(wrapper.find('.bg-red-50').exists()).toBe(false)
  })
})
