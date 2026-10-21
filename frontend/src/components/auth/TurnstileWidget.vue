<template>
  <div v-if="siteKey" class="turnstile-wrapper">
    <div ref="containerRef" class="turnstile-container"></div>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'

// TurnstileWidget.vue loads the Turnstile browser SDK and renders its challenge
// inside the authentication form. Public site key, theme, and size configure the
// widget, while verification, expiration, and SDK errors are forwarded to the
// parent form.
//
// The browser SDK and its global load callback are shared through window.
// Rendering releases an existing widget before clearing its container and
// creating a replacement, and a site-key change repeats rendering once the
// script has loaded. Removal errors are ignored during replacement and unmount
// so cleanup can finish. The exposed reset method keeps the instance mounted and
// asks the SDK to invalidate its current result.

// TurnstileRenderOptions describes widget appearance and callbacks supplied to
// the vendor browser SDK.
interface TurnstileRenderOptions {
  sitekey: string // Public Turnstile site key.
  callback: (token: string) => void // Successful verification token callback.
  'expired-callback'?: () => void // Verification token expiration callback.
  'error-callback'?: () => void // Widget verification error callback.
  theme?: 'light' | 'dark' | 'auto' // Widget color theme selection.
  size?: 'normal' | 'compact' | 'flexible' // Widget layout size selection.
}

// TurnstileAPI describes the browser methods used to create and manage challenge
// widgets.
interface TurnstileAPI {
  // render creates a vendor challenge in the supplied host and returns its widget
  // identifier. The component retains that identifier for reset and removal calls.
  render: (container: HTMLElement, options: TurnstileRenderOptions) => string // Create widget and return identifier.
  // reset invalidates the result of the identified vendor challenge while keeping
  // its host mounted. The component invokes it after the owning form requests
  // another verification attempt.
  reset: (widgetId?: string) => void // Clear current widget verification.
  // remove releases the identified vendor challenge and its SDK resources. The
  // component calls it before replacing the host contents or while unmounting
  // the authentication widget.
  remove: (widgetId?: string) => void // Remove vendor widget resources.
}

declare global {
  // Window exposes the optional vendor SDK and its global script-ready callback.
  interface Window {
    turnstile?: TurnstileAPI // Loaded vendor SDK instance.
    onTurnstileLoad?: () => void // Global script readiness callback.
  }
}

// props configures the public site key and applies the vendor auto theme and
// flexible sizing defaults.
const props = withDefaults(
  defineProps<{
    siteKey: string // Public Turnstile site key.
    theme?: 'light' | 'dark' | 'auto' // Widget color theme selection.
    size?: 'normal' | 'compact' | 'flexible' // Widget layout size selection.
  }>(),
  {
    theme: 'auto',
    size: 'flexible',
  },
)

// emit forwards proof availability, expiration, and verification failure so the
// owning form can manage submission state.
const emit = defineEmits<{
  (e: 'verify', token: string): void /* Authentication proof verification callback. */
  (e: 'expire'): void /* Current proof expiration notification. */
  (e: 'error'): void /* Verification failure notification event. */
}>()

// Widget references and script readiness connect SDK lifecycle operations to the
// container created by this component template.
const containerRef = ref<HTMLElement | null>(null)
const widgetId = ref<string | null>(null)
const scriptLoaded = ref(false)

// loadScript resolves when Turnstile is available and records script readiness
// for key changes. An existing script receives the global load callback; a newly
// inserted script rejects if its network load fails.
const loadScript = (): Promise<void> => {
  return new Promise((resolve, reject) => {
    if (window.turnstile) {
      scriptLoaded.value = true
      resolve()
      return
    }

    // Reuse an existing SDK script and install its load callback so this widget
    // can render after script initialization.
    const existingScript = document.querySelector('script[src*="turnstile"]')
    if (existingScript) {
      window.onTurnstileLoad = () => {
        scriptLoaded.value = true
        resolve()
      }
      return
    }

    const script = document.createElement('script')
    script.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=onTurnstileLoad'
    script.async = true
    script.defer = true

    window.onTurnstileLoad = () => {
      scriptLoaded.value = true
      resolve()
    }

    script.onerror = () => {
      reject(new Error('Failed to load Turnstile script'))
    }

    document.head.appendChild(script)
  })
}

// renderWidget replaces the mounted challenge when the SDK, container, and site
// key exist. The previous widget is removed and its container cleared before
// rendering the new configured challenge with parent-facing events.
const renderWidget = () => {
  if (!window.turnstile || !containerRef.value || !props.siteKey) {
    return
  }

  // Release the old vendor widget before clearing its container so replacement
  // rendering can own the same host element.
  if (widgetId.value) {
    try {
      window.turnstile.remove(widgetId.value)
    } catch {
      // Ignore vendor removal failures because cleanup still clears local
      // ownership or completes component unmount after an SDK error.
    }
    widgetId.value = null
  }

  // Clear container
  containerRef.value.innerHTML = ''

  widgetId.value = window.turnstile.render(containerRef.value, {
    sitekey: props.siteKey,
    callback: (token: string) => {
      emit('verify', token)
    },
    'expired-callback': () => {
      emit('expire')
    },
    'error-callback': () => {
      emit('error')
    },
    theme: props.theme,
    size: props.size,
  })
}

// reset asks the loaded SDK to clear the current widget verification result.
// Calls before widget creation or after instance removal return without
// requesting any vendor operation.
const reset = () => {
  if (window.turnstile && widgetId.value) {
    window.turnstile.reset(widgetId.value)
  }
}

// Expose reset method to parent
defineExpose({ reset })

onMounted(async () => {
  if (!props.siteKey) {
    return
  }

  try {
    await loadScript()
    renderWidget()
  } catch (error) {
    console.error('Failed to initialize Turnstile:', error)
    emit('error')
  }
})

onUnmounted(() => {
  if (window.turnstile && widgetId.value) {
    try {
      window.turnstile.remove(widgetId.value)
    } catch {
      // Ignore vendor removal failures because cleanup still clears local
      // ownership or completes component unmount after an SDK error.
    }
  }
})

// Re-render when siteKey changes
watch(
  () => props.siteKey,
  (newKey) => {
    if (newKey && scriptLoaded.value) {
      renderWidget()
    }
  },
)
</script>

<style scoped>
.turnstile-wrapper {
  width: 100%;
}

.turnstile-container {
  width: 100%;
  min-height: 65px;
}

/* Make the Turnstile iframe fill the container width */
.turnstile-container :deep(iframe) {
  width: 100% !important;
}
</style>
