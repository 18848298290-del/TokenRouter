import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useStepUp } from '@/composables/useStepUp'
import TotpStepUpDialog from '@/components/auth/TotpStepUpDialog.vue'

// TotpStepUpDialog.spec.ts checks a complete autofilled OTP delivered to the first
// visible cell. A deferred step-up endpoint keeps verification pending long enough
// to inspect digit distribution before success closes the supplied controller.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const mocks = vi.hoisted(() => ({
  stepUp: vi.fn(),
  showError: vi.fn(),
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
    showError: (...args: unknown[]) => mocks.showError(...args),
  }),
}))

vi.mock('@/api', () => ({
  totpAPI: {
    // stepUp forwards the completed OTP to the deferred verification mock.
    stepUp: (...args: unknown[]) => mocks.stepUp(...args),
  },
}))

describe('TotpStepUpDialog', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case.
  beforeEach(() => {
    mocks.stepUp.mockReset()
    mocks.showError.mockReset()
    mocks.stepUp.mockResolvedValue({ success: true })
  })

  // A full OTP arrives in the first step-up cell while verification stays pending.
  // The test checks digit distribution before settling the request and closing the
  // controller.
  it('splits a complete autofilled code in the visible first cell', async () => {
    // Keep verification pending while inspecting the cells, because successful
    // completion clears the OTP draft before the controller closes its dialog.
    let resolveStepUp: (() => void) | undefined
    mocks.stepUp.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          resolveStepUp = resolve
        }),
    )

    const controller = useStepUp()
    controller.visible.value = true
    const wrapper = mount(TotpStepUpDialog, {
      props: { controller },
    })

    const visibleInputs = wrapper.findAll('[data-testid="totp-digit-input"]')
    expect(visibleInputs[0].attributes('autocomplete')).toBe('one-time-code')
    expect(visibleInputs[0].attributes('pattern')).toBe('[0-9]{1,6}')

    const firstInput = visibleInputs[0].element as HTMLInputElement
    firstInput.value = '135790'
    await visibleInputs[0].trigger('change')
    await vi.waitFor(() => expect(mocks.stepUp).toHaveBeenCalledWith('135790'))

    expect(visibleInputs.map((input) => (input.element as HTMLInputElement).value)).toEqual([
      '1',
      '3',
      '5',
      '7',
      '9',
      '0',
    ])
    resolveStepUp?.()
    await flushPromises()
    expect(controller.visible.value).toBe(false)
  })
})
