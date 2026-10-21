import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

vi.mock('@/i18n', () => ({
  i18n: {
    global: {
      t: (key: string) => key,
    },
  },
}))

// mockShowSuccess and mockShowError record clipboard notifications while the tests replace
// the actual application store.
const mockShowSuccess = vi.fn()
const mockShowError = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: mockShowSuccess,
    showError: mockShowError,
  }),
}))

import { useClipboard } from '@/composables/useClipboard'

// useClipboard.spec.ts checks browser clipboard paths and their visible feedback using
// mocked stores and browser capabilities. Fake timers expose confirmation expiry, and
// secure-context plus copy-command stubs distinguish API success, legacy copying, and
// reported failure.

describe('useClipboard', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    vi.clearAllMocks()

    Object.defineProperty(window, 'isSecureContext', { value: true, writable: true })
    Object.defineProperty(navigator, 'clipboard', {
      value: {
        writeText: vi.fn().mockResolvedValue(undefined),
      },
      writable: true,
      configurable: true,
    })
  })

  afterEach(() => {
    // Restore browser timers and remove the manually installed copy command after each
    // case so later clipboard capability tests remain isolated.
    vi.useRealTimers()

    if ('execCommand' in document) {
      delete (document as any).execCommand
    }
  })

  // The secure clipboard API resolves a nonempty text write successfully. The returned
  // copied state must change from its initial false value to true afterward.
  it('复制成功后 copied 变为 true', async () => {
    const { copied, copyToClipboard } = useClipboard()

    expect(copied.value).toBe(false)

    await copyToClipboard('hello')

    expect(copied.value).toBe(true)
  })

  // A successful copy schedules confirmation expiry while fake time remains under test
  // control. Advancing two seconds must clear copied after its initial successful state.
  it('copied 在 2 秒后自动恢复为 false', async () => {
    const { copied, copyToClipboard } = useClipboard()

    await copyToClipboard('hello')
    expect(copied.value).toBe(true)

    vi.advanceTimersByTime(2000)

    expect(copied.value).toBe(false)
  })

  // The caller supplies custom success text for a successful clipboard write. The app
  // store must receive that exact message for its success notification.
  it('复制成功时调用 showSuccess', async () => {
    const { copyToClipboard } = useClipboard()

    await copyToClipboard('hello', '已复制')

    expect(mockShowSuccess).toHaveBeenCalledWith('已复制')
  })

  // A successful copy has no caller-provided success message. The app store must receive
  // the translated default key supplied by the mocked i18n instance.
  it('无自定义消息时使用 i18n 默认消息', async () => {
    const { copyToClipboard } = useClipboard()

    await copyToClipboard('hello')

    expect(mockShowSuccess).toHaveBeenCalledWith('common.copiedToClipboard')
  })

  // An empty string is submitted while a working clipboard API is available. The operation
  // must return false, keep copied false, and avoid invoking the writer.
  it('空文本返回 false 且不复制', async () => {
    const { copyToClipboard, copied } = useClipboard()

    const result = await copyToClipboard('')

    expect(result).toBe(false)
    expect(copied.value).toBe(false)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
  })

  // The secure clipboard API rejects, but the legacy copy command reports success. The
  // operation must return true, set copied, and invoke the copy command.
  it('Clipboard API 失败时降级到 fallback', async () => {
    const writeTextMock = navigator.clipboard.writeText as any
    writeTextMock.mockRejectedValue(new Error('API failed'))

    const documentAny = document as any
    documentAny.execCommand = vi.fn().mockReturnValue(true)

    const { copyToClipboard, copied } = useClipboard()
    const result = await copyToClipboard('fallback text')

    expect(result).toBe(true)
    expect(copied.value).toBe(true)
    expect(document.execCommand).toHaveBeenCalledWith('copy')
  })

  // The browser is marked insecure while the legacy copy command succeeds. The operation
  // must use that command, report success, and leave the clipboard API unused.
  it('非安全上下文使用 fallback', async () => {
    Object.defineProperty(window, 'isSecureContext', { value: false, writable: true })

    const documentAny = document as any
    documentAny.execCommand = vi.fn().mockReturnValue(true)

    const { copyToClipboard, copied } = useClipboard()
    const result = await copyToClipboard('insecure context text')

    expect(result).toBe(true)
    expect(copied.value).toBe(true)
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()
    expect(document.execCommand).toHaveBeenCalledWith('copy')
  })

  // Both the clipboard write and legacy copy attempt fail to copy text. The operation must
  // return false, retain copied as false, and report an error toast.
  it('所有复制方式均失败时调用 showError', async () => {
    const writeTextMock = navigator.clipboard.writeText as any
    writeTextMock.mockRejectedValue(new Error('fail'))

    const documentAny = document as any
    documentAny.execCommand = vi.fn().mockReturnValue(false)

    const { copyToClipboard, copied } = useClipboard()
    const result = await copyToClipboard('text')

    expect(result).toBe(false)
    expect(copied.value).toBe(false)
    expect(mockShowError).toHaveBeenCalled()
  })
})
