import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileEditForm from '@/components/user/profile/ProfileEditForm.vue'
import type { User } from '@/api/identity.types'

// ProfileEditForm.spec.ts checks username editing through the production profile form with a
// controlled API response and auth store. The test protects the submitted field scope and
// publication of the returned profile after successful form submission.

// Hoisted request, notification, and auth-state mocks keep the production component and test
// assertions attached to the same mutable account response.
const { updateProfileMock, showSuccessMock, showErrorMock, authStoreState } = vi.hoisted(() => ({
  updateProfileMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  authStoreState: {
    user: null as User | null,
  },
}))

vi.mock('@/api', () => ({
  userAPI: {
    updateProfile: updateProfileMock,
  },
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authStoreState,
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
        if (key === 'profile.editProfile') return 'Edit profile'
        if (key === 'profile.username') return 'Username'
        if (key === 'profile.enterUsername') return 'Enter username'
        if (key === 'profile.updateProfile') return 'Update profile'
        if (key === 'profile.updating') return 'Updating...'
        if (key === 'profile.updateSuccess') return 'Profile updated'
        if (key === 'profile.updateFailed') return 'Update failed'
        if (key === 'profile.usernameRequired') return 'Username is required'
        return key
      },
    }),
  }
})

// createUser supplies a complete ordinary account fixture with optional scenario overrides.
// Callers replace binding, source, or balance fields while retaining profile fields required
// by the mounted component.
function createUser(overrides: Partial<User> = {}): User {
  return {
    id: 5,
    username: 'alice-new',
    email: 'alice@example.com',
    role: 'user',
    balance: 10,
    concurrency: 2,
    status: 'active',
    allowed_groups: null,
    balance_notify_enabled: true,
    balance_notify_threshold: null,
    balance_notify_extra_emails: [],
    created_at: '2026-04-20T00:00:00Z',
    updated_at: '2026-04-20T00:00:00Z',
    ...overrides,
  }
}

describe('ProfileEditForm', () => {
  beforeEach(() => {
    updateProfileMock.mockReset()
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
    authStoreState.user = null
  })

  // The user changes the username and submits the profile form. The test protects a
  // username-only request, publication of the returned account record, and successful update
  // feedback.
  it('updates username without sending email', async () => {
    const updatedUser = createUser()
    updateProfileMock.mockResolvedValue(updatedUser)

    const wrapper = mount(ProfileEditForm, {
      props: {
        initialUsername: 'alice',
      },
    })

    await wrapper.get('#username').setValue('alice-new')
    await wrapper.get('form').trigger('submit')

    expect(updateProfileMock).toHaveBeenCalledWith({ username: 'alice-new' })
    expect(authStoreState.user).toEqual(updatedUser)
    expect(showSuccessMock).toHaveBeenCalledWith('Profile updated')
    expect(showErrorMock).not.toHaveBeenCalled()
  })
})
