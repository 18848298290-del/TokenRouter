<template>
  <div v-if="sceneId && prefix" class="aliyun-captcha-wrapper">
    <button
      :id="buttonId"
      type="button"
      class="aliyun-captcha-button"
      :class="state === 'verified' ? 'aliyun-captcha-button--verified' : ''"
      :disabled="state === 'verified'"
    >
      <Icon
        name="checkCircle"
        size="sm"
        :animate-on-hover="false"
        v-if="state === 'verified'"
        class="aliyun-captcha-icon"
        aria-hidden="true"
      />
      <Icon name="shieldCheck" size="sm" v-else class="aliyun-captcha-icon" aria-hidden="true" />
      <span>{{ buttonText }}</span>
    </button>
    <div :id="elementId"></div>
  </div>
</template>

<script setup lang="ts">
import { vendorLocale } from '@/i18n/catalog'
import Icon from '@/components/icons/Icon.vue'
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'

// AliyunCaptchaWidget.vue coordinates the Aliyun popup challenge with
// authentication form submission. The SDK produces a verification parameter,
// which the component caches and forwards to the parent for server verification.
// A completed parameter remains available until the parent resets the widget
// after its authentication attempt.
//
// The SDK supplies no dismissal callback used here, so a polling loop observes
// popup visibility and resolves a pending submission as cancellation when an
// opened popup closes or never appears within the timeout. Before appearance,
// polling retries the trigger click because the SDK attaches its button listener
// asynchronously. Reset and unmount stop polling and cancel the registered
// waiter. SDK readiness continues independently: a verify call awaiting readiness
// can register a waiter after either cleanup path has run.

// AliyunCaptchaVerifyResult reports the local challenge callback result expected
// by the vendor SDK.
interface AliyunCaptchaVerifyResult {
  captchaResult: boolean // Local challenge callback success.
  bizResult?: boolean // Optional business validation result.
}

// AliyunCaptchaInitOptions describes SDK configuration, target selectors, and
// verification callbacks for one widget.
interface AliyunCaptchaInitOptions {
  SceneId: string // Configured vendor scene identifier.
  prefix: string // Vendor deployment prefix value.
  mode: 'popup' | 'embed' // Vendor challenge presentation mode.
  element: string // Challenge host CSS selector.
  button: string // Challenge trigger CSS selector.
  // captchaVerifyCallback forwards proof to the parent before reporting local
  // challenge success to the SDK.
  captchaVerifyCallback: (
    captchaVerifyParam: string,
  ) => AliyunCaptchaVerifyResult | Promise<AliyunCaptchaVerifyResult>
  onBizResultCallback: (bizResult: boolean) => void // Vendor business result notification.
  getInstance: (instance: unknown) => void // Vendor instance publication callback.
  slideStyle?: {
    width: number // Measured width in pixels.
    height: number // Measured height in pixels.
  } // Slider dimensions in pixels.
  language?: string // Vendor interface language code.
}

declare global {
  // Window exposes the loaded Aliyun initializer and configuration read by its
  // browser script.
  interface Window {
    initAliyunCaptcha?: (
      options: AliyunCaptchaInitOptions,
    ) => void /* Loaded vendor initialization callback. */
    AliyunCaptchaConfig?: {
      region: string // Vendor SDK regional setting.
      prefix: string // Vendor deployment prefix value.
    } // Configuration read before SDK initialization.
  }
}

// props configures the vendor scene, deployment prefix, and region used by SDK
// initialization for this widget.
const props = withDefaults(
  defineProps<{
    sceneId: string // Configured vendor scene identifier.
    prefix: string // Vendor deployment prefix value.
    region?: 'cn' | 'sgp' // Vendor regional endpoint selection.
  }>(),
  {
    region: 'cn',
  },
)

// emit forwards verification parameters and initialization errors to the
// authentication form that owns server-side proof validation.
const emit = defineEmits<{
  (e: 'verify', param: string): void /* Authentication proof verification callback. */
  (e: 'expire'): void /* Current proof expiration notification. */
  (e: 'error'): void /* Verification failure notification event. */
}>()

// Translation and vendor locale selection keep trigger labels and the SDK
// challenge language consistent with the authentication form.
const { t, locale } = useI18n()

// Widget identifiers isolate each mounted trigger and host so the SDK can bind
// their selectors independently.
const uid = Math.random().toString(36).slice(2, 10)
const buttonId = `aliyun-captcha-button-${uid}`
const elementId = `aliyun-captcha-element-${uid}`

// state selects the idle trigger, pending challenge, or completed proof
// presentation used by the authentication form.
const state = ref<'idle' | 'verifying' | 'verified'>('idle')

// buttonText chooses the translated trigger label from the current verification
// state for visible authentication feedback.
const buttonText = computed(() => {
  switch (state.value) {
    case 'verified':
      return t('auth.captchaVerified')
    case 'verifying':
      return t('auth.captchaVerifying')
    default:
      return t('auth.captchaClickToVerify')
  }
})

// Vendor script and popup identifiers support SDK loading and dismissal
// observation; timing limits bound opening retries and polling frequency.
const SCRIPT_SRC = 'https://o.alicdn.com/captcha-frontend/aliyunCaptcha/AliyunCaptcha.js'
const POPUP_ID = 'aliyunCaptcha-window-popup'
const MASK_ID = 'aliyunCaptcha-mask'
const POPUP_OPEN_TIMEOUT_MS = 8000
const POPUP_WATCH_INTERVAL_MS = 300

// cachedParam holds a completed verification parameter until reset; the parent
// resets after a rejected authentication submission.
let cachedParam: string | null = null
// pending owns the current form waiter; popupWatchTimer tracks dismissal
// observation, and readyPromise shares SDK readiness until initialization fails.
let pending: {
  resolve: (value: string | null) => void /* Pending authentication completion callback. */
} | null = null
let popupWatchTimer: number | null = null
let readyPromise: Promise<void> | null = null

// loadScript publishes vendor configuration and resolves when the Aliyun
// initializer is available. Existing scripts receive load listeners; a new
// script is inserted when absent, and network failures reject readiness.
const loadScript = (): Promise<void> => {
  return new Promise((resolve, reject) => {
    // Publish region and prefix before SDK loading so vendor initialization
    // reads the configuration supplied by the current authentication form.
    window.AliyunCaptchaConfig = { region: props.region, prefix: props.prefix }

    if (window.initAliyunCaptcha) {
      resolve()
      return
    }

    const existingScript = document.querySelector<HTMLScriptElement>(
      'script[src*="aliyunCaptcha/AliyunCaptcha"]',
    )
    if (existingScript) {
      existingScript.addEventListener('load', () => resolve())
      existingScript.addEventListener('error', () =>
        reject(new Error('Failed to load Aliyun captcha script')),
      )
      return
    }

    const script = document.createElement('script')
    script.src = SCRIPT_SRC
    script.async = true
    script.onload = () => resolve()
    script.onerror = () => reject(new Error('Failed to load Aliyun captcha script'))
    document.head.appendChild(script)
  })
}

// initCaptcha attaches the vendor popup to this widget's unique button and host
// selectors. It requires a loaded SDK initializer and forwards generated
// parameters for server-side authentication verification.
function initCaptcha(): void {
  if (!window.initAliyunCaptcha) {
    throw new Error('Aliyun captcha script not ready')
  }
  window.initAliyunCaptcha({
    SceneId: props.sceneId,
    prefix: props.prefix,
    mode: 'popup',
    element: `#${elementId}`,
    button: `#${buttonId}`,
    // Forward the verification parameter to the authentication form, whose
    // business request submits it for server verification through the token
    // field.
    // captchaVerifyCallback forwards proof to the parent before reporting local
    // challenge success to the SDK.
    captchaVerifyCallback: (captchaVerifyParam: string) => {
      onCaptchaParam(captchaVerifyParam)
      return { captchaResult: true }
    },
    onBizResultCallback: () => {},
    getInstance: () => {},
    slideStyle: { width: 360, height: 40 },
    language: vendorLocale('aliyun', locale.value),
  })
}

// ensureReady shares SDK loading and initialization across callers while
// readiness is pending or complete. A rejected readiness promise is cleared so
// later verification attempts can retry initialization.
function ensureReady(): Promise<void> {
  if (!readyPromise) {
    readyPromise = loadScript().then(() => initCaptcha())
    readyPromise.catch(() => {
      // Clear the rejected readiness promise so a later explicit verification
      // can retry loading after a temporary SDK failure.
      readyPromise = null
    })
  }
  return readyPromise
}

// onCaptchaParam caches a completed verification parameter, marks the widget
// verified, and resolves the waiting submission. Polling stops before proof
// publication so dismissal observation cannot cancel that completed result.
function onCaptchaParam(param: string): void {
  stopPopupWatch()
  cachedParam = param
  state.value = 'verified'
  emit('verify', param)
  const current = pending
  pending = null
  current?.resolve(param)
}

// settlePending clears the waiting submission slot before resolving its result.
// Passing null represents cancellation, and calling it with no waiting
// submission leaves challenge and cached proof state unchanged.
function settlePending(value: string | null): void {
  const current = pending
  pending = null
  current?.resolve(value)
}

// isPopupVisible reports whether the vendor popup exists with a visible display
// style.
function isPopupVisible(): boolean {
  const popup = document.getElementById(POPUP_ID)
  if (!popup) return false
  return window.getComputedStyle(popup).display !== 'none'
}

// stopPopupWatch clears the polling interval when one is active and releases its
// timer handle. Reset, proof completion, and unmount call it before settling
// waiting authentication submissions.
function stopPopupWatch(): void {
  if (popupWatchTimer !== null) {
    window.clearInterval(popupWatchTimer)
    popupWatchTimer = null
  }
}

// startPopupWatch observes challenge opening, dismissal, and successful proof state.
// An opened popup that disappears resolves cancellation; a popup that never
// appears also cancels after the opening timeout. Before appearance the loop
// retries the trigger click because SDK listener attachment can follow the
// first programmatic click. Each new watch cancels its predecessor, and proof
// completion stops polling before resolving the pending form submission.
function startPopupWatch(): void {
  stopPopupWatch()
  const startedAt = Date.now()
  let seen = false
  popupWatchTimer = window.setInterval(() => {
    if (state.value === 'verified') {
      stopPopupWatch()
      return
    }
    if (isPopupVisible()) {
      seen = true
      return
    }
    if (seen || Date.now() - startedAt > POPUP_OPEN_TIMEOUT_MS) {
      stopPopupWatch()
      state.value = 'idle'
      settlePending(null)
      return
    }
    document.getElementById(buttonId)?.click()
  }, POPUP_WATCH_INTERVAL_MS)
}

// handleTriggerClick starts the pending display and popup watch for an unverified widget.
// Repeated trigger clicks retain the existing watch deadline, while a verified
// widget keeps its cached proof until reset.
function handleTriggerClick(): void {
  if (state.value === 'verified') {
    return
  }
  state.value = 'verifying'
  if (popupWatchTimer === null) {
    startPopupWatch()
  }
}

// verify returns a cached completed parameter or opens the popup for the current submission.
// Starting a new call first settles an existing waiter as cancellation, then
// awaits SDK readiness. Popup dismissal or opening timeout returns null, while
// SDK loading failure rejects the call. Reset or unmount during readiness does
// not cancel this await, so its continuation can register a waiter afterward.
async function verify(): Promise<string | null> {
  if (state.value === 'verified' && cachedParam) {
    return cachedParam
  }
  settlePending(null)
  await ensureReady()
  return new Promise<string | null>((resolve) => {
    pending = { resolve }
    document.getElementById(buttonId)?.click()
  })
}

// reset clears completed proof and returns the widget to its idle presentation.
// The authentication form calls it after a rejected submission. Popup polling
// stops and the registered waiter receives null before cached proof is discarded;
// calls still awaiting SDK readiness continue and may register a later waiter.
function reset(): void {
  stopPopupWatch()
  settlePending(null)
  cachedParam = null
  state.value = 'idle'
}

defineExpose({ verify, reset })

onMounted(async () => {
  if (!props.sceneId || !props.prefix) {
    return
  }

  document.getElementById(buttonId)?.addEventListener('click', handleTriggerClick)
  try {
    await ensureReady()
  } catch (error) {
    console.error('Failed to initialize Aliyun captcha:', error)
    emit('error')
  }
})

onUnmounted(() => {
  document.getElementById(buttonId)?.removeEventListener('click', handleTriggerClick)
  stopPopupWatch()
  settlePending(null)
  // Remove vendor popup elements after releasing this component's listeners and
  // pending waiter, so a later mount does not reuse stale popup DOM.
  document.getElementById(MASK_ID)?.remove()
  document.getElementById(POPUP_ID)?.remove()
})
</script>

<style scoped>
.aliyun-captcha-wrapper {
  width: 100%;
}

.aliyun-captcha-button {
  display: flex;
  width: 100%;
  min-height: 44px;
  align-items: center;
  justify-content: center;
  gap: 0.5rem;
  border-radius: var(--radius-control);
  border: 1px solid rgb(209 213 219);
  background-color: rgb(249 250 251);
  padding: 0.5rem 0.75rem;
  font-size: 0.875rem;
  font-weight: 500;
  color: rgb(55 65 81);
  transition:
    border-color var(--motion-fast) var(--motion-ease),
    background-color var(--motion-fast) var(--motion-ease),
    color var(--motion-fast) var(--motion-ease);
}

.aliyun-captcha-button:hover:not(:disabled) {
  border-color: rgb(156 163 175);
  background-color: rgb(243 244 246);
}

.aliyun-captcha-button--verified {
  border-color: rgb(34 197 94);
  background-color: rgb(240 253 244);
  color: rgb(21 128 61);
}

:root.dark .aliyun-captcha-button,
.dark .aliyun-captcha-button {
  border-color: theme('borderColor.dark.600');
  background-color: rgb(20 20 22);
  color: rgb(212 212 216);
}

:root.dark .aliyun-captcha-button:hover:not(:disabled),
.dark .aliyun-captcha-button:hover:not(:disabled) {
  border-color: theme('borderColor.dark.500');
  background-color: rgb(39 39 42);
}

:root.dark .aliyun-captcha-button--verified,
.dark .aliyun-captcha-button--verified {
  border-color: rgb(34 197 94);
  background-color: rgb(20 83 45 / 0.3);
  color: rgb(134 239 172);
}

.aliyun-captcha-icon {
  height: 1rem;
  width: 1rem;
  flex-shrink: 0;
}
</style>
