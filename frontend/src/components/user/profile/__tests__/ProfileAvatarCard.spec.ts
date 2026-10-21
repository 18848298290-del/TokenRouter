import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import ProfileAvatarCard from '@/components/user/profile/ProfileAvatarCard.vue'
import type { User } from '@/api/identity.types'

// ProfileAvatarCard.spec.ts exercises avatar upload and deletion through the production card
// with deterministic browser image services. Profile requests and notifications are mocked,
// while file selection, compression dispatch, payload generation, and shared auth-profile
// updates run in the component.

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

vi.mock('@/utils/apiError', () => ({
  extractApiErrorMessage: (error: unknown) => (error as Error).message || 'request failed',
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) => {
        if (key === 'profile.avatar.title') return 'Profile avatar'
        if (key === 'profile.avatar.description') return 'Upload and manage your avatar'
        if (key === 'profile.avatar.uploadAction') return 'Upload'
        if (key === 'profile.avatar.uploadHint')
          return 'Uploaded images are compressed to 20KB when possible'
        if (key === 'profile.avatar.saveSuccess') return 'Avatar updated'
        if (key === 'profile.avatar.deleteSuccess') return 'Avatar removed'
        if (key === 'profile.avatar.invalidType') return 'Please choose an image file'
        if (key === 'profile.avatar.gifTooLarge')
          return 'GIF avatars must already be 20KB or smaller'
        if (key === 'profile.avatar.compressTooLarge')
          return 'Unable to compress this image below 20KB'
        if (key === 'profile.avatar.compressFailed') return 'Failed to compress the selected image'
        if (key === 'profile.avatar.readFailed') return 'Failed to read the selected image'
        if (key === 'common.delete') return 'Delete'
        if (key === 'profile.avatar.compressedReady')
          return `Compressed from ${params?.from} to ${params?.to}`
        if (key === 'profile.avatar.sizeReady') return `Ready: ${params?.size}`
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
    username: 'alice',
    email: 'alice@example.com',
    avatar_url: null,
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

// flushAsyncWork drains the promise chain used by the synchronous browser-image mocks. Its
// fixed microtask turns let encoding, file reading, and profile update settle before
// assertions inspect the final response.
async function flushAsyncWork(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
  await Promise.resolve()
}

// Original browser constructors and document creation are retained so each test can restore
// the global image environment after installing compression substitutes.
const originalFileReader = globalThis.FileReader
const originalImage = globalThis.Image
const originalCreateElement = document.createElement.bind(document)

// installAvatarCompressionMocks replaces file reading, image decoding, and canvas encoding
// with deterministic successful substitutes. The encoded blob size is configurable, and
// afterEach restores browser globals and mocked document creation.
function installAvatarCompressionMocks(blobSize = 8 * 1024) {
  // MockFileReader synchronously produces recognizable image data URLs for avatar
  // preparation tests.
  class MockFileReader {
    result: string | ArrayBuffer | null = null // Produced image data URL.
    onload: ((this: FileReader, ev: ProgressEvent<FileReader>) => any) | null = null // Installed successful-read callback.
    onerror: ((this: FileReader, ev: ProgressEvent<FileReader>) => any) | null = null // Installed failed-read callback.
    error: DOMException | null = null // Optional browser read failure.

    // readAsDataURL selects the fixture data URL from the encoded blob MIME type and invokes
    // its load callback synchronously. The mock supports successful reads used by these
    // upload scenarios.
    readAsDataURL(blob: Blob) {
      if (blob.type === 'image/webp') {
        this.result =
          'data:image/webp;base64,' + Buffer.from('compressed-avatar').toString('base64')
      } else {
        this.result = 'data:image/png;base64,' + Buffer.from('original-avatar').toString('base64')
      }
      this.onload?.call(this as unknown as FileReader, new ProgressEvent('load'))
    }
  }

  // MockImage supplies fixed decoded dimensions and completes image loading synchronously.
  class MockImage {
    naturalWidth = 1200 // Decoded image width pixels.
    naturalHeight = 1200 // Decoded image height pixels.
    onload: (() => void) | null = null // Installed successful-read callback.
    onerror: (() => void) | null = null // Installed failed-read callback.

    // src accepts the requested image URL and immediately invokes the installed load
    // callback.
    set src(_value: string) {
      this.onload?.()
    }
  }

  globalThis.FileReader = MockFileReader as unknown as typeof FileReader
  globalThis.Image = MockImage as unknown as typeof Image
  vi.spyOn(document, 'createElement').mockImplementation(((
    tagName: string,
    options?: ElementCreationOptions,
  ) => {
    if (tagName === 'canvas') {
      return {
        width: 0,
        height: 0,
        getContext: () => ({
          clearRect: vi.fn(),
          drawImage: vi.fn(),
        }),
        toBlob: (callback: BlobCallback) => {
          callback(new Blob([new Uint8Array(blobSize)], { type: 'image/webp' }))
        },
      } as unknown as HTMLCanvasElement
    }
    return originalCreateElement(tagName, options)
  }) as typeof document.createElement)
}

describe('ProfileAvatarCard', () => {
  beforeEach(() => {
    updateProfileMock.mockReset()
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
    authStoreState.user = null
  })

  afterEach(() => {
    globalThis.FileReader = originalFileReader
    globalThis.Image = originalImage
    vi.restoreAllMocks()
  })

  // The profile avatar card mounts for an ordinary account record. The test protects the
  // file-based editing flow by checking that no manual avatar URL field is rendered.
  it('does not render a manual avatar input field', () => {
    authStoreState.user = createUser()

    const wrapper = mount(ProfileAvatarCard, {
      props: {
        user: authStoreState.user,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    expect(wrapper.find('[data-testid="profile-avatar-input"]').exists()).toBe(false)
  })

  // A selected PNG exceeds the upload byte target and browser encoding yields a fitting
  // WebP. The test protects submission of the compressed data URL and publication of the
  // returned profile.
  it('compresses an uploaded image that exceeds the 20KB target before uploading', async () => {
    installAvatarCompressionMocks()
    const updatedUser = createUser({
      avatar_url: 'data:image/webp;base64,Y29tcHJlc3NlZC1hdmF0YXI=',
    })
    updateProfileMock.mockResolvedValue(updatedUser)
    authStoreState.user = createUser()

    const wrapper = mount(ProfileAvatarCard, {
      props: {
        user: authStoreState.user,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const fileInput = wrapper.get('[data-testid="profile-avatar-file-input"]')
    Object.defineProperty(fileInput.element, 'files', {
      value: [new File([new Uint8Array(220 * 1024)], 'avatar.png', { type: 'image/png' })],
      configurable: true,
    })

    await fileInput.trigger('change')
    await flushAsyncWork()

    expect(updateProfileMock).toHaveBeenCalledWith({
      avatar_url: 'data:image/webp;base64,Y29tcHJlc3NlZC1hdmF0YXI=',
    })
    expect(authStoreState.user).toEqual(updatedUser)
    expect(showSuccessMock).toHaveBeenCalledWith('Avatar updated')
    expect(showErrorMock).not.toHaveBeenCalled()
  })

  // The avatar editor is embedded within another profile section when a large image is
  // selected. The test protects immediate compressed upload and shared-profile update in
  // that presentation mode.
  it('uploads the selected avatar immediately in embedded mode', async () => {
    installAvatarCompressionMocks()
    const updatedUser = createUser({
      avatar_url: 'data:image/webp;base64,Y29tcHJlc3NlZC1hdmF0YXI=',
    })
    updateProfileMock.mockResolvedValue(updatedUser)
    authStoreState.user = createUser()

    const wrapper = mount(ProfileAvatarCard, {
      props: {
        user: authStoreState.user,
        embedded: true,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const fileInput = wrapper.get('[data-testid="profile-avatar-file-input"]')
    Object.defineProperty(fileInput.element, 'files', {
      value: [new File([new Uint8Array(220 * 1024)], 'avatar.png', { type: 'image/png' })],
      configurable: true,
    })

    await fileInput.trigger('change')
    await flushAsyncWork()

    expect(updateProfileMock).toHaveBeenCalledWith({
      avatar_url: 'data:image/webp;base64,Y29tcHJlc3NlZC1hdmF0YXI=',
    })
    expect(authStoreState.user).toEqual(updatedUser)
    expect(showSuccessMock).toHaveBeenCalledWith('Avatar updated')
  })

  // An account has an existing avatar and the user requests deletion. The test protects the
  // empty-string clearing payload, returned null avatar, and successful removal feedback.
  it('deletes the current avatar', async () => {
    const updatedUser = createUser({ avatar_url: null })
    updateProfileMock.mockResolvedValue(updatedUser)
    authStoreState.user = createUser({ avatar_url: 'https://cdn.example.com/old.png' })

    const wrapper = mount(ProfileAvatarCard, {
      props: {
        user: authStoreState.user,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    await wrapper.get('[data-testid="profile-avatar-delete"]').trigger('click')

    expect(updateProfileMock).toHaveBeenCalledWith({ avatar_url: '' })
    expect(authStoreState.user?.avatar_url).toBeNull()
    expect(showSuccessMock).toHaveBeenCalledWith('Avatar removed')
  })
})
