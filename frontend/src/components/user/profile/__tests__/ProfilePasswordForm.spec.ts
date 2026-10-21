import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ProfilePasswordForm from '@/components/user/profile/ProfilePasswordForm.vue'

// ProfilePasswordForm.spec.ts exercises password-form validation and API error feedback with
// controlled request results. The production form receives entered passwords, and assertions
// distinguish rejected local input from a submitted change that fails at the server.

// Hoisted password and notification spies distinguish local validation rejection from a
// server request failure while retaining the actual form event handlers.
const { changePasswordMock, showSuccessMock, showErrorMock } = vi.hoisted(() => ({
  changePasswordMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
}))

vi.mock('@/api', () => ({
  userAPI: {
    changePassword: changePasswordMock,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: showSuccessMock,
    showError: showErrorMock,
  }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => {
        const translations: Record<string, string> = {
          'profile.changePassword': 'Change Password',
          'profile.currentPassword': 'Current Password',
          'profile.newPassword': 'New Password',
          'profile.confirmNewPassword': 'Confirm New Password',
          'profile.passwordHint': 'Password must be at least 8 characters long',
          'profile.changingPassword': 'Changing...',
          'profile.changePasswordButton': 'Change Password',
          'profile.passwordsNotMatch': 'New passwords do not match',
          'profile.passwordTooShort': 'Password must be at least 8 characters long',
          'profile.passwordChangeSuccess': 'Password changed successfully',
          'profile.passwordChangeFailed': 'Failed to change password',
        }
        return translations[key] ?? key
      },
    }),
  }
})

describe('ProfilePasswordForm', () => {
  // The replacement password and confirmation differ before submission. The test protects a
  // validation toast, absence of an API request, and omission of duplicate inline error
  // text.
  it('shows validation failures as toast messages instead of inline errors', async () => {
    const wrapper = mount(ProfilePasswordForm)

    await wrapper.get('#old_password').setValue('old-password')
    await wrapper.get('#new_password').setValue('new-password')
    await wrapper.get('#confirm_password').setValue('different-password')
    await wrapper.get('form').trigger('submit.prevent')

    expect(changePasswordMock).not.toHaveBeenCalled()
    expect(showErrorMock).toHaveBeenCalledWith('New passwords do not match')
    expect(wrapper.find('.input-error-text').exists()).toBe(false)
  })

  // Matching replacement passwords reach an API that returns a specific failure detail. The
  // test protects the request arguments and toast-based display of that server message.
  it('shows API failures as toast messages', async () => {
    changePasswordMock.mockRejectedValue({
      response: { data: { detail: 'backend failure' } },
    })

    const wrapper = mount(ProfilePasswordForm)

    await wrapper.get('#old_password').setValue('old-password')
    await wrapper.get('#new_password').setValue('new-password')
    await wrapper.get('#confirm_password').setValue('new-password')
    await wrapper.get('form').trigger('submit.prevent')

    expect(changePasswordMock).toHaveBeenCalledWith('old-password', 'new-password')
    expect(showErrorMock).toHaveBeenCalledWith('backend failure')
    expect(wrapper.find('.input-error-text').exists()).toBe(false)
  })
})
