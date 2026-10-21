import { ref } from 'vue'
import { useAppStore } from '@/stores/app'
import { COPY_FEEDBACK_MS } from '@/constants/ui'
import { i18n } from '@/i18n'

// useClipboard.ts copies text through the browser clipboard API or a temporary textarea.
// Successful copies expose a brief reactive confirmation and a success toast, while an
// unsuccessful copy command produces an error toast for the caller.

// t translates clipboard feedback through the shared i18n instance when copy operations
// report their outcome.
const { t } = i18n.global

// isClipboardSupported checks clipboard availability together with the browser secure-context requirement.
function isClipboardSupported(): boolean {
  return !!(navigator.clipboard && window.isSecureContext)
}

// fallbackCopy copies multiline text through a temporary focused textarea and returns the
// copy command result. Its finally block removes the textarea even when execCommand
// throws; thrown errors propagate.
function fallbackCopy(text: string): boolean {
  const textarea = document.createElement('textarea')
  textarea.value = text
  textarea.setAttribute('readonly', 'true')
  textarea.style.cssText =
    'position:fixed;left:0;top:0;width:1px;height:1px;opacity:0;pointer-events:none'
  document.body.appendChild(textarea)
  textarea.focus({ preventScroll: true })
  textarea.select()
  textarea.setSelectionRange(0, textarea.value.length)
  try {
    return document.execCommand('copy')
    // Remove the temporary textarea after the command finishes or throws so later copies do
    // not retain hidden input nodes.
  } finally {
    document.body.removeChild(textarea)
  }
}

// useClipboard creates copy feedback state and a text-copy operation backed by the app
// store. Each successful operation schedules its own confirmation reset timer, which is
// not disposed on unmount.
export function useClipboard() {
  const appStore = useAppStore()
  // copied is local confirmation state; each successful call schedules its own reset
  // independently of subsequent copy attempts.
  const copied = ref(false)

  // copyToClipboard copies nonempty text and returns the browser command outcome.
  // Clipboard API rejection retries with the textarea path, whose exceptions propagate.
  // Success displays caller text or the translated default and schedules copied to reset;
  // empty input returns false immediately.
  const copyToClipboard = async (text: string, successMessage?: string): Promise<boolean> => {
    if (!text) return false

    let success: boolean

    if (isClipboardSupported()) {
      try {
        await navigator.clipboard.writeText(text)
        success = true
        // Clipboard permissions can reject a supported API call; retry through the command
        // path before reporting a failed copy.
      } catch {
        success = fallbackCopy(text)
      }
      // When the secure clipboard API is unavailable, the textarea command supplies the
      // browser-compatible copy attempt for this invocation.
    } else {
      success = fallbackCopy(text)
    }

    if (success) {
      copied.value = true
      appStore.showSuccess(successMessage || t('common.copiedToClipboard'))
      setTimeout(() => {
        copied.value = false
      }, COPY_FEEDBACK_MS)
    } else {
      appStore.showError(t('common.copyFailed'))
    }

    return success
  }

  return { copied, copyToClipboard }
}
