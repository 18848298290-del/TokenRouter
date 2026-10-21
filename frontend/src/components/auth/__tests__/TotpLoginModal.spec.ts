import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TotpLoginModal from '@/components/auth/TotpLoginModal.vue'

// TotpLoginModal.spec.ts checks segmented OTP input and parent-driven verification
// errors. Hidden autofill, visible autofill, and change-only events exercise the
// real component watcher so repeated notifications can be distinguished from
// distinct verification submissions.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const { showErrorMock } = vi.hoisted(() => ({
  showErrorMock: vi.fn(),
})) // hoisted dependency and routing fixtures

vi.mock('vue-i18n', () => ({
  // useI18n supplies the translation fixture used by rendered authentication
  // labels.
  useI18n: () => ({
    // t returns translation keys directly for stable component text assertions.
    t: (key: string) => key,
  }),
}))

vi.mock('@/stores', () => ({
  // useAppStore exposes the toast and settings fixtures consumed by the component.
  useAppStore: () => ({
    // showError records failure feedback delivered through the app toast service.
    showError: (...args: any[]) => showErrorMock(...args),
  }),
}))

describe('TotpLoginModal', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case.
  beforeEach(() => {
    showErrorMock.mockReset()
  })

  // The login parent reports a rejected one-time code through the exposed method.
  // The modal must show a toast without rendering the message or an inline red
  // panel.
  it('sends verification errors to toast and does not render inline red text', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
        userEmailMasked: 'u***@example.com',
      },
    })

    ;(
      wrapper.vm as unknown as {
        // setError exposes the modal error reset operation to test callers.
        setError: (message: string) => void
      }
    ).setError('Invalid code')
    await wrapper.vm.$nextTick()

    expect(showErrorMock).toHaveBeenCalledWith('Invalid code')
    expect(wrapper.text()).not.toContain('Invalid code')
    expect(wrapper.find('.bg-red-50').exists()).toBe(false)
  })

  // The hidden browser-autofill input receives a complete one-time code. All six
  // visible cells must update and the modal must emit the joined verification
  // value.
  it('fills visible code inputs from hidden one-time-code autofill input', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    const hiddenInput = wrapper.get('input[autocomplete="one-time-code"][aria-hidden="true"]')
    expect(hiddenInput.attributes('data-1p-ignore')).toBe('')
    await hiddenInput.setValue('12a3456')
    await wrapper.vm.$nextTick()

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs.map((input) => (input.element as HTMLInputElement).value)).toEqual([
      '1',
      '2',
      '3',
      '4',
      '5',
      '6',
    ])
    expect(wrapper.emitted('verify')).toEqual([['123456']])
  })

  // A password manager writes the entire OTP into the first visible cell. The test
  // checks autofill attributes, all six displayed digits, and the emitted
  // verification code.
  it('splits a complete autofilled code written to the first visible cell', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs[0].attributes('maxlength')).toBe('6')
    expect(visibleInputs[0].attributes('pattern')).toBe('[0-9]{1,6}')
    expect(visibleInputs[0].attributes('autocomplete')).toBe('one-time-code')
    await visibleInputs[0].setValue('654321')
    await wrapper.vm.$nextTick()

    expect(visibleInputs.map((input) => (input.element as HTMLInputElement).value)).toEqual([
      '6',
      '5',
      '4',
      '3',
      '2',
      '1',
    ])
    expect(wrapper.emitted('verify')).toEqual([['654321']])
  })

  // The autofill fixture updates the input through a change event without an input
  // event. The modal must distribute the complete code and emit verification.
  it('handles an autofill provider that only dispatches change', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    const firstInput = visibleInputs[0].element as HTMLInputElement
    firstInput.value = '246810'
    await visibleInputs[0].trigger('change')
    await wrapper.vm.$nextTick()

    expect(visibleInputs.map((input) => (input.element as HTMLInputElement).value)).toEqual([
      '2',
      '4',
      '6',
      '8',
      '1',
      '0',
    ])
    expect(wrapper.emitted('verify')).toEqual([['246810']])
  })

  // Both the hidden autofill input and visible first cell receive the same
  // complete OTP. The modal must emit only one verification event for that value.
  it('submits once when both autofill targets receive the same code', async () => {
    const wrapper = mount(TotpLoginModal, {
      props: {
        tempToken: 'temp-token',
      },
    })

    await wrapper.get('input[autocomplete="one-time-code"][aria-hidden="true"]').setValue('123456')
    await wrapper.findAll('[data-testid="totp-digit-input"]')[0].setValue('123456')
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('verify')).toEqual([['123456']])
  })
})
