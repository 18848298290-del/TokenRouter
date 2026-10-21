import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

// UserCreateModal.spec.ts checks omitted and explicit numeric fields in administrator
// user creation. The mounted form submits through a mocked creation endpoint and
// renders its actual validation controls. Assertions distinguish blank defaults from
// zero and reject key limits outside the accepted integer range.

// The hoisted creation and notification spies capture user payloads and validation
// feedback without creating accounts through the server during tests.
const { createUserMock, showErrorMock, showSuccessMock } = vi.hoisted(() => ({
  createUserMock: vi.fn(), // User creation spy.
  showErrorMock: vi.fn(), // Error notification spy.
  showSuccessMock: vi.fn(), // Success notification spy.
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      create: createUserMock, // User creation substitute.
    }, // User endpoint substitutes.
  }, // Administrator API substitute.
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock, // Error notification spy.
    showSuccess: showSuccessMock, // Success notification spy.
  }), // Application notification-store substitute.
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key, // Translation key passthrough.
    }), // Translation composable substitute.
  }
})

import UserCreateModal from '../UserCreateModal.vue'

// BaseDialogStub renders visible form and footer slots so creation tests can submit the
// actual component's form while avoiding modal infrastructure.
const BaseDialogStub = defineComponent({
  name: 'BaseDialog', // Stub component name.
  props: {
    show: {
      type: Boolean, // Vue prop constructor.
      default: false, // Initial prop value.
    }, // Parent-controlled dialog visibility.
  }, // Accepted component props.
  template: '<div v-if="show"><slot /><slot name="footer" /></div>', // Rendered stub markup.
})

// mountModal opens the user creation form with the dialog and icon components stubbed.
// It returns the mounted wrapper for input and event assertions; the caller owns its
// eventual unmounting after component interactions finish.
function mountModal() {
  return mount(UserCreateModal, {
    props: {
      show: true, // Parent-controlled dialog visibility.
    }, // Accepted component props.
    global: {
      stubs: {
        BaseDialog: BaseDialogStub, // Dialog rendering substitute.
        Icon: true, // Icon rendering substitute.
      }, // Replaced child components.
    }, // Mount-wide component overrides.
  })
}

// fillRequiredFields enters the email and password used by creation tests.
async function fillRequiredFields(wrapper: ReturnType<typeof mountModal>) {
  const inputs = wrapper.findAll('input')
  await inputs[0].setValue('new-user@example.com')
  await inputs[1].setValue('strong-pass')
}

describe('UserCreateModal', () => {
  beforeEach(() => {
    createUserMock.mockReset()
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    createUserMock.mockResolvedValue({})
  })

  // The administrator fills required identity fields and leaves optional balance and
  // key-limit fields blank. The creation request must omit both fields so their server
  // defaults remain applicable.
  it('omits balance when the balance field is blank', async () => {
    const wrapper = mountModal()
    await fillRequiredFields(wrapper)

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createUserMock).toHaveBeenCalledTimes(1)
    expect(createUserMock).toHaveBeenCalledWith(
      expect.not.objectContaining({
        balance: expect.anything(), // Draft starting balance.
      }),
    )
    expect(createUserMock).toHaveBeenCalledWith(
      expect.not.objectContaining({
        api_key_limit: expect.anything(), // API-key creation limit.
      }),
    )
  })

  // The balance input receives an explicit zero before the creation form is submitted.
  // The endpoint must receive numeric zero rather than an omitted balance field.
  it('sends explicit zero balance', async () => {
    const wrapper = mountModal()
    await fillRequiredFields(wrapper)
    await wrapper.findAll('input')[3].setValue('0')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createUserMock).toHaveBeenCalledTimes(1)
    expect(createUserMock).toHaveBeenCalledWith(
      expect.objectContaining({
        balance: 0, // Draft starting balance.
      }),
    )
  })

  // The API-key limit input receives an explicit zero before creation is submitted. The
  // endpoint must receive numeric zero so the value survives optional-field
  // normalization.
  it('sends explicit zero API key limit', async () => {
    const wrapper = mountModal()
    await fillRequiredFields(wrapper)
    await wrapper.get('[data-test="api-key-limit-input"]').setValue('0')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createUserMock).toHaveBeenCalledTimes(1)
    expect(createUserMock).toHaveBeenCalledWith(
      expect.objectContaining({
        api_key_limit: 0, // API-key creation limit.
      }),
    )
  })

  // A negative API-key limit is entered with the required identity fields completed.
  // Validation must show the limit error and prevent the creation endpoint from being
  // called.
  it('rejects a negative API key limit', async () => {
    const wrapper = mountModal()
    await fillRequiredFields(wrapper)
    await wrapper.get('[data-test="api-key-limit-input"]').setValue('-1')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createUserMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.users.form.apiKeyLimitInvalid')
  })

  // The API-key limit exceeds the signed database integer range by one. The component
  // must show the limit validation error and suppress the creation request.
  it('rejects an API key limit above the database range', async () => {
    const wrapper = mountModal()
    await fillRequiredFields(wrapper)
    await wrapper.get('[data-test="api-key-limit-input"]').setValue('2147483648')

    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(createUserMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('admin.users.form.apiKeyLimitInvalid')
  })
})
