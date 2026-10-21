<template>
  <div
    v-if="isInternational"
    ref="internationalContainerRef"
    data-testid="tencent-captcha-international-container"
    :class="
      internationalContainerVisible
        ? 'flex min-h-[60px] w-full justify-center'
        : 'pointer-events-none fixed left-1/2 top-0 flex h-[60px] w-[302px] -translate-x-1/2 scale-[0.01] opacity-0'
    "
  />
</template>

<script setup lang="ts">
import { vendorLocale } from '@/i18n/catalog'
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  loadTencentCaptcha,
  normalizeTencentCaptchaRegion,
  type TencentCaptchaProof,
  type TencentCaptchaResult,
} from '@/utils/tencentCaptcha'

// TencentCaptchaGate.vue coordinates Tencent challenge instances with
// authentication attempts and component lifetime. Domestic challenges open on
// demand. International challenges render a checkbox on mount and may finish
// before form submission, so the component caches a successful proof with its
// creation time. A later submission consumes that proof once if it remains
// within the configured age limit.
//
// Concurrent verification calls share the pending challenge. Reset destroys the
// current SDK instance, settles pending callers as cancellation, and clears
// cached proof state before optionally scheduling a replacement international
// checkbox. Asynchronous SDK loading checks the active cancellation callback
// before publishing an instance, preventing a reset challenge from taking
// ownership again.

// Locale, public application settings, and container state determine the SDK
// region and visible international checkbox host.
const { locale } = useI18n()
const props = withDefaults(
  defineProps<{
    appId: string // Tencent public application identifier.
    region?: string // Vendor SDK regional setting.
  }>(),
  { region: 'cn' },
)
const isInternational = computed(() => normalizeTencentCaptchaRegion(props.region) === 'intl')
const internationalContainerRef = ref<HTMLDivElement | null>(null)
const internationalContainerVisible = ref<boolean>(isInternational.value)

// instance holds the current vendor widget so completion and reset can release
// its DOM and event resources.
let instance: {
  // show displays the current vendor challenge in its configured container.
  show(): void
  // destroy releases the current vendor challenge and its browser resources.
  destroy(): void
} | null = null
// Pending challenge state shares one verification result across callers and lets
// reset settle waiting submissions as cancellation.
let pending: Promise<TencentCaptchaProof | null> | null = null
let cancelPending: (() => void) | null = null
// cachedProof and cachedProofCreatedAt hold one preloaded international result
// until verification consumes it or reset discards it. The timestamp supplies
// the age check before a submit can reuse that result.
let cachedProof: TencentCaptchaProof | null = null
let cachedProofCreatedAt = 0
// isMounted prevents the deferred reset callback from preloading a checkbox
// after component teardown has released this authentication widget.
let isMounted = false

// cachedProofMaxAgeMs bounds cached international proofs to four minutes before
// requiring another challenge for the current submission.
const cachedProofMaxAgeMs = 4 * 60 * 1000

// createVerificationPromise owns one Tencent instance and settles its
// verification result through a shared finish guard. User cancellation returns
// null, malformed proof data rejects, and a valid international result keeps the
// checked widget visible. The cancellation callback identifies the active load,
// so a delayed SDK response returns when reset has replaced that callback. Every
// completion clears the matching cancellation handle and destroys the instance
// unless the completed international widget is retained.
function createVerificationPromise(
  revealInternational: boolean = true,
): Promise<TencentCaptchaProof | null> {
  return new Promise((resolve, reject) => {
    let settled = false

    // finish accepts the first completion for this challenge and ignores later
    // SDK or cancellation callbacks through the settled guard. It clears
    // cancelPending only when that slot still contains this challenge's cancel
    // function, keeping a replacement challenge's cancellation handle intact.
    //
    // International success can retain the checked widget for the owning form.
    // Other results destroy the instance and hide its container before invoking
    // the notification callback, so callers observe resource cleanup when their
    // verification promise settles.
    const finish = (callback: () => void, keepInternationalContainer: boolean = false): void => {
      if (settled) return
      settled = true
      if (cancelPending === cancel) cancelPending = null
      if (!keepInternationalContainer) {
        instance?.destroy()
        instance = null
        internationalContainerVisible.value = false
      }
      callback()
    }
    // cancel finishes the active challenge with a null proof result.
    const cancel = (): void => finish(() => resolve(null))

    cancelPending = cancel
    const region = normalizeTencentCaptchaRegion(props.region)
    if (region === 'intl' && revealInternational) {
      internationalContainerVisible.value = true
    }

    void nextTick()
      .then(() => loadTencentCaptcha(region))
      .then((TencentCaptcha) => {
        if (cancelPending !== cancel) return

        const userLanguage = vendorLocale('tencent', locale.value)
        // handleResult translates Tencent completion into proof, cancellation, or
        // rejection. Missing proof fields, error tickets, and explicit SDK errors reject
        // the challenge; international success keeps the checked widget visible.
        const handleResult = (result: TencentCaptchaResult): void => {
          if (result.ret === 2) {
            finish(() => resolve(null))
            return
          }

          const ticket = result.ticket?.trim() || ''
          const randstr = result.randstr?.trim() || ''
          if (
            !ticket ||
            !randstr ||
            ticket.startsWith('trerror_') ||
            result.errorCode !== undefined
          ) {
            finish(() => reject(new Error('Tencent Captcha verification failed')))
            return
          }

          // Keep the completed international checkbox visible until the owning
          // authentication form resets it after the submission result.
          finish(() => resolve({ ticket, randstr }), region === 'intl')
        }

        if (region === 'intl') {
          // Mount the international checkbox inside this component so it stays
          // beside the authentication form rather than at the document end.
          const container = internationalContainerRef.value
          if (!container) {
            throw new Error('Tencent Captcha international container is unavailable')
          }
          instance = new TencentCaptcha(container, props.appId, handleResult, {
            // Require the user to confirm the visible checkbox before the SDK
            // starts its challenge interaction for this form.
            enableAutoCheck: false,
            userLanguage,
            type: 'popup',
          })
        } else {
          instance = new TencentCaptcha(props.appId, handleResult, { userLanguage })
        }
        instance.show()
      })
      .catch((error: unknown) => finish(() => reject(error)))
  })
}

// verify returns a fresh or shared proof for the current authentication attempt.
// A cached international proof is consumed once within its age limit; an expired
// proof is cleared before a new challenge starts. When a challenge is already
// pending, callers share its result and the container is revealed before the
// instance is shown again. Completion clears pending state only while that
// promise still owns the slot, protecting any replacement verification.
function verify(): Promise<TencentCaptchaProof | null> {
  if (cachedProof) {
    if (Date.now() - cachedProofCreatedAt >= cachedProofMaxAgeMs) {
      // Discard the expired proof and its completed widget before creating a
      // fresh challenge for this same verification call.
      reset(false)
    } else {
      const proof = cachedProof
      cachedProof = null
      cachedProofCreatedAt = 0
      // Clear the completed promise alongside the cached proof so subsequent
      // submissions cannot consume the same preloaded verification result.
      pending = null
      return Promise.resolve(proof)
    }
  }

  if (pending) {
    const verification = pending
    internationalContainerVisible.value = true
    // Reveal the container before showing the existing instance again, and
    // confirm the pending challenge still owns that instance after the DOM
    // update.
    void nextTick().then(() => {
      if (pending === verification) instance?.show()
    })
    return verification.then((proof) => {
      if (cachedProof === proof) {
        cachedProof = null
        cachedProofCreatedAt = 0
      }
      return proof
    })
  }

  internationalContainerVisible.value = true
  const verification = createVerificationPromise(true)
  pending = verification
  void verification.then(
    () => {
      if (pending === verification) pending = null
    },
    () => {
      if (pending === verification) pending = null
    },
  )
  return verification
}

// preload starts an international checkbox when no challenge or cached proof
// exists. Successful verification records proof and time for a later submit;
// failure is ignored so an explicit verification can retry.
function preload(): void {
  if (!isInternational.value || pending || cachedProof) return

  // Start the international checkbox during mount so its proof can be available
  // by the time the authentication form submits.
  const verification = createVerificationPromise(true)
  pending = verification
  void verification
    .then((proof) => {
      if (proof) {
        cachedProof = proof
        cachedProofCreatedAt = Date.now()
      }
    })
    // Preload failure remains silent because an explicit submit can start
    // another challenge and surface its error.
    .catch(() => undefined)
    .finally(() => {
      if (pending === verification) pending = null
    })
}

// reset destroys the SDK instance, cancels waiting verification, and discards
// cached proof state. By default a mounted international widget schedules a new
// checkbox; callers pass false when replacing an expired proof within verify.
function reset(reinitialize: boolean = true): void {
  // Release the widget before resolving cancellation so waiting callers cannot
  // keep interacting with the discarded SDK instance.
  instance?.destroy()
  instance = null
  cancelPending?.()
  cancelPending = null
  pending = null
  cachedProof = null
  cachedProofCreatedAt = 0
  internationalContainerVisible.value = isInternational.value

  // Schedule a replacement international checkbox after clearing consumed proof
  // state, and check mount state again before the deferred preload runs.
  if (reinitialize && isMounted && isInternational.value) {
    void nextTick().then(() => {
      if (isMounted) preload()
    })
  }
}

onMounted(() => {
  isMounted = true
  preload()
})
onBeforeUnmount(() => {
  isMounted = false
  reset()
})
defineExpose({ verify, reset })
</script>
