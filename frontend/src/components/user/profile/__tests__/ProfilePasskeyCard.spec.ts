import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import ProfilePasskeyCard from '@/components/user/profile/ProfilePasskeyCard.vue'

// ProfilePasskeyCard.spec.ts checks passkey credential loading against server enablement and
// expected settings races. Browser support is controlled and request results distinguish a
// disabled-feature response from an unrelated failure that still requires visible error
// feedback.

// Passkey service and notification mocks control browser support, feature-race responses,
// and ordinary failures without invoking a real WebAuthn ceremony.
const mocks = vi.hoisted(() => ({
  isSupported: vi.fn(),
  list: vi.fn(),
  register: vi.fn(),
  rename: vi.fn(),
  remove: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api', () => ({
  passkeyAPI: {
    isSupported: mocks.isSupported,
    list: mocks.list,
    register: mocks.register,
    rename: mocks.rename,
    remove: mocks.remove,
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mocks.showSuccess,
    showError: mocks.showError,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

// Disabled settings and a settings-change race suppress expected passkey errors, while unrelated server failures remain visible to the user.
describe('ProfilePasskeyCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mocks.isSupported.mockReturnValue(true)
    mocks.list.mockResolvedValue([])
  })

  // Public settings disable passkeys while browser WebAuthn support is available. The test
  // protects omission of credential requests and display of the feature-disabled explanation
  // without an error toast.
  it('does not request credentials while passkeys are disabled', async () => {
    const wrapper = mount(ProfilePasskeyCard, {
      props: { enabled: false },
      global: { stubs: { Icon: true } },
    })

    await flushPromises()

    expect(mocks.list).not.toHaveBeenCalled()
    expect(mocks.showError).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('profile.passkey.featureDisabled')
  })

  // Public settings initially allow loading but the API reports that passkeys became
  // disabled. The test protects silent handling of that expected feature-state race.
  it('silences PASSKEY_DISABLED returned during a settings race', async () => {
    mocks.list.mockRejectedValue({ code: 403, reason: 'PASSKEY_DISABLED' })

    mount(ProfilePasskeyCard, {
      props: { enabled: true },
      global: { stubs: { Icon: true } },
    })
    await flushPromises()

    expect(mocks.list).toHaveBeenCalledTimes(1)
    expect(mocks.showError).not.toHaveBeenCalled()
  })

  // The credential list request fails with an internal server error while passkeys are
  // enabled. The test protects visible error feedback for failures beyond the disabled-
  // feature case.
  it('shows a toast for real credential loading failures', async () => {
    mocks.list.mockRejectedValue({ code: 500, reason: 'INTERNAL_ERROR' })

    mount(ProfilePasskeyCard, {
      props: { enabled: true },
      global: { stubs: { Icon: true } },
    })
    await flushPromises()

    expect(mocks.showError).toHaveBeenCalledWith('profile.passkey.loadFailed')
  })
})
