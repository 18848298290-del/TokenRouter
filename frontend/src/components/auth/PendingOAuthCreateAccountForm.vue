<template>
  <form class="space-y-3" @submit.prevent="handleSubmit">
    <input
      v-model="email"
      :data-testid="`${testIdPrefix}-create-account-email`"
      type="email"
      class="input w-full"
      :placeholder="t('auth.emailPlaceholder')"
      :disabled="isSubmitting || isSendingCode"
    />
    <input
      v-model="password"
      :data-testid="`${testIdPrefix}-create-account-password`"
      type="password"
      class="input w-full"
      :placeholder="t('auth.passwordPlaceholder')"
      :disabled="isSubmitting"
    />
    <div v-if="captchaEnabled" class="space-y-2">
      <TurnstileWidget
        ref="turnstileRef"
        :site-key="turnstileSiteKey"
        :turnstile-enabled="turnstileEnabled"
        :turnstile-site-key="turnstileSiteKey"
        :tencent-enabled="tencentCaptchaEnabled"
        :tencent-app-id="tencentCaptchaAppId"
        :tencent-region="tencentCaptchaRegion"
        :aliyun-enabled="aliyunCaptchaEnabled"
        :aliyun-scene-id="aliyunCaptchaSceneId"
        :aliyun-prefix="aliyunCaptchaPrefix"
        :aliyun-region="aliyunCaptchaRegion"
        @verify="onTurnstileVerify"
        @expire="onTurnstileExpire"
        @error="onTurnstileError"
      />
    </div>
    <div v-if="emailVerifyEnabled" class="flex gap-3">
      <input
        v-model="verifyCode"
        :data-testid="`${testIdPrefix}-create-account-verify-code`"
        type="text"
        inputmode="numeric"
        maxlength="6"
        class="input min-w-0 flex-1"
        placeholder="123456"
        :disabled="isSubmitting"
      />
      <button
        :data-testid="`${testIdPrefix}-create-account-send-code`"
        type="button"
        class="btn btn-secondary shrink-0"
        :disabled="
          isSubmitting ||
          isSendingCode ||
          countdown > 0 ||
          !email.trim() ||
          (turnstileEnabled && !turnstileToken)
        "
        @click="handleSendCode"
      >
        {{
          isSendingCode
            ? t('auth.sendingCode')
            : countdown > 0
              ? t('auth.resendCountdown', { countdown })
              : t('auth.sendCode')
        }}
      </button>
    </div>
    <p
      v-if="emailVerifyEnabled && sendCodeSuccess"
      class="text-sm text-green-600 dark:text-green-400"
    >
      {{ t('auth.codeSentSuccess') }}
    </p>
    <p v-else-if="emailVerifyEnabled" class="text-xs text-gray-500 dark:text-dark-400">
      {{ t('auth.verificationCodeHint') }}
    </p>
    <input
      v-if="invitationCodeEnabled"
      v-model="invitationCode"
      :data-testid="`${testIdPrefix}-create-account-invitation-code`"
      type="text"
      class="input w-full"
      :placeholder="t('auth.invitationCodePlaceholder')"
      :disabled="isSubmitting"
    />
    <button
      :data-testid="`${testIdPrefix}-create-account-submit`"
      type="button"
      class="btn btn-primary w-full"
      :disabled="
        isSubmitting ||
        !email.trim() ||
        password.length < 6 ||
        (invitationCodeEnabled && !invitationCode.trim()) ||
        (turnstileEnabled && !turnstileToken)
      "
      @click="handleSubmit"
    >
      {{ isSubmitting ? t('common.processing') : t('auth.createAccount') }}
    </button>
    <button
      type="button"
      class="btn btn-secondary w-full"
      :disabled="isSubmitting"
      @click="emitSwitchToBind"
    >
      {{ t('auth.alreadyHaveAccount') }}
    </button>
  </form>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import TurnstileWidget from '@/components/auth/CaptchaChallenge.vue'
import { getPublicSettings, sendPendingOAuthVerifyCode } from '@/api/auth'
import { useAppStore } from '@/stores'

// PendingOAuthCreateAccountForm.vue collects credentials, invitation information,
// and verification proof for an unfinished OAuth account creation. The parent
// submits the final payload and supplies server errors, while this form owns
// sending email codes and the resend countdown.
//
// Turnstile proof is prepared by the widget, while Tencent and Aliyun proof is
// acquired for each action. Sending a code consumes the current proof even on
// failure; a later submission therefore needs a fresh result. The form keeps
// provider-specific proof fields paired when emitting the creation payload and
// uses public settings to decide which verification and invitation controls apply.

// PendingOAuthCreateAccountPayload carries the account draft and optional proof to
// the callback parent. verifyCode is empty when email verification is disabled;
// otherwise it contains the trimmed code consumed by the creation request.
// turnstileToken is included only for enabled Turnstile or Aliyun with a nonempty
// token. Tencent enablement and a nonempty ticket include tencentCaptchaTicket
// and tencentCaptchaRandstr together; other configurations omit that pair.
// A blank trimmed invitation code becomes undefined for the callback request.
export type PendingOAuthCreateAccountPayload = {
  email: string // account email address

  password: string // entered account password

  verifyCode: string // entered email code

  turnstileToken?: string // Turnstile or Aliyun proof

  tencentCaptchaTicket?: string // Tencent challenge ticket

  tencentCaptchaRandstr?: string // Tencent challenge random string

  invitationCode?: string // entered invitation code
}

// props supplies the parent email suggestion, selector prefix, submission state,
// and server error. emit returns the creation payload or existing-account email
// while the parent owns final submission.
const props = defineProps<{
  // initialEmail initializes the editable email and overwrites it whenever the
  // parent value changes. A missing or empty suggestion resets the local draft to
  // empty.
  initialEmail: string // parent email suggestion

  testIdPrefix: string // consumer test selector prefix

  // isSubmitting comes from the parent account request. It disables the exposed
  // form controls while that parent owns the final creation or binding transition.
  isSubmitting: boolean // parent account submission state

  // errorMessage reports a parent submission failure. A nonempty changed value
  // triggers a toast and resets enabled captcha so a retry can acquire fresh
  // proof.
  errorMessage?: string // parent submission failure text
}>() // parent component inputs

const emit = defineEmits<{
  submit: [payload: PendingOAuthCreateAccountPayload] // validated account creation payload

  switchToBind: [email: string] // email for binding continuation
}>() // parent component event dispatcher

// t resolves the authentication messages displayed by this component. Nearby store
// and navigation services deliver those messages and handle the requested
// authentication transition.
const { t } = useI18n() // translated authentication message resolver
const appStore = useAppStore() // public settings and toast owner

// The credential draft keeps the entered email, password, and verification or
// invitation values until the corresponding form handler consumes them. Optional
// proofs remain in their separately owned challenge state.
const email = ref('') // editable account email draft
const password = ref('') // editable account password draft
const verifyCode = ref('') // entered email verification code
const invitationCode = ref('') // entered signup invitation code
// isSendingCode gates email delivery controls while the request is pending.
// Delivery feedback and countdown values let the user distinguish sending from the
// server-required resend wait.
const isSendingCode = ref(false) // email-code request busy flag
const sendCodeError = ref('') // email-code failure toast source
const sendCodeSuccess = ref(false) // email-code success hint flag
const countdown = ref(0) // remaining resend wait seconds
// Invitation and email-verification flags determine which additional creation
// fields appear. Their initial values remain available until the form settings
// request supplies its configuration.
const invitationCodeEnabled = ref(false) // public invitation requirement flag
const emailVerifyEnabled = ref(true) // public email verification requirement
// These values carry public challenge configuration and local verification state
// into the widget and request handlers. Provider-specific token and random-string
// fields travel together when Tencent is enabled.
const turnstileEnabled = ref(false) // public Turnstile enable flag
const turnstileSiteKey = ref('') // public Turnstile widget identifier
const tencentCaptchaEnabled = ref(false) // public Tencent captcha enable flag
const tencentCaptchaAppId = ref('') // public Tencent application identifier
const tencentCaptchaRegion = ref('cn') // Tencent SDK region selector
const aliyunCaptchaEnabled = ref(false) // public Aliyun captcha enable flag
const aliyunCaptchaSceneId = ref('') // public Aliyun scene identifier
const aliyunCaptchaPrefix = ref('') // public Aliyun SDK prefix
const aliyunCaptchaRegion = ref('cn') // Aliyun SDK region selector
const turnstileToken = ref('') // prepared challenge proof value
const tencentCaptchaRandstr = ref('') // paired Tencent proof random string
const turnstileRef = ref<InstanceType<typeof TurnstileWidget> | null>(null) // captcha widget action handle
const aliyunCaptchaReady = computed(
  () =>
    aliyunCaptchaEnabled.value &&
    Boolean(aliyunCaptchaSceneId.value) &&
    Boolean(aliyunCaptchaPrefix.value),
) // complete Aliyun configuration flag

const actionCaptchaEnabled = computed(
  () =>
    (tencentCaptchaEnabled.value && Boolean(tencentCaptchaAppId.value)) || aliyunCaptchaReady.value,
) // Tencent or Aliyun action gate
const captchaEnabled = computed(
  () => (turnstileEnabled.value && Boolean(turnstileSiteKey.value)) || actionCaptchaEnabled.value,
) // available challenge provider flag

// countdownTimer owns the resend interval handle. Starting another countdown
// replaces its scheduled work, and component unmount releases the remaining
// interval.
let countdownTimer: ReturnType<typeof setInterval> | null = null // owned resend interval handle

// Apply a changed parent email suggestion to the local draft immediately,
// including initial setup, so the form opens with the current pending-account
// address.
watch(
  () => props.initialEmail,
  (value) => {
    email.value = value || ''
  },
  { immediate: true },
)

// Show a nonempty authentication error through the shared app toast when its
// watched value changes, keeping feedback outside the form content.
watch(sendCodeError, (value) => {
  if (value) {
    appStore.showError(value)
  }
})

// A parent submission error is shown through the app toast and invalidates enabled
// proof, because retry may need a fresh challenge after that attempted request.
watch(
  () => props.errorMessage,
  (value) => {
    if (value) {
      appStore.showError(value)
      // A submitted request or reported server error can consume this proof. Reset
      // the widget and local values before the user retries the action.
      if (captchaEnabled.value) {
        resetTurnstile()
      }
    }
  },
)

// clearCountdown releases the current resend interval when one is active. It
// clears the stored handle after cancellation, allowing subsequent reset and
// unmount paths to call it repeatedly.
function clearCountdown() {
  // Release the existing resend interval before replacement or cleanup, preventing
  // an old timer from continuing to decrement this component countdown.
  if (countdownTimer) {
    clearInterval(countdownTimer)
    countdownTimer = null
  }
}

// startCountdown replaces the resend timer with a countdown in seconds. The
// interval updates the displayed remaining time once per second and clears its
// handle at expiry; component cleanup also releases it.
function startCountdown(seconds: number) {
  clearCountdown()
  countdown.value = Math.max(0, seconds)

  if (countdown.value <= 0) {
    return
  }

  // Tick once per second to match the countdown units returned by the verification
  // endpoint, and retain the handle for replacement or component cleanup.
  countdownTimer = setInterval(() => {
    if (countdown.value <= 1) {
      countdown.value = 0
      clearCountdown()
      return
    }

    countdown.value -= 1
  }, 1000)
}

// getRequestErrorMessage selects server detail, server message, or the error
// message in that order. It returns the caller fallback when those fields are
// empty, keeping endpoint-specific text available to toast callers.
function getRequestErrorMessage(error: unknown, fallback: string): string {
  const err = error as {
    message?: string // reported failure message

    response?: {
      data?: {
        detail?: string // server detailed failure message
        message?: string // reported failure message
      } // HTTP response payload
    } // optional HTTP error response
  }
  return err.response?.data?.detail || err.response?.data?.message || err.message || fallback
}

// resetTurnstile clears both local proof fields and resets the challenge widget.
// Call it after a consumed request or a server submission error so later actions
// cannot reuse that proof.
function resetTurnstile() {
  turnstileToken.value = ''
  tencentCaptchaRandstr.value = ''
  turnstileRef.value?.reset()
}

// onTurnstileVerify stores the challenge token and optional Tencent random string.
// It clears the captcha error so the current proof can enable the next guarded
// form action.
function onTurnstileVerify(token: string, randstr = '') {
  turnstileToken.value = token
  tencentCaptchaRandstr.value = randstr
  sendCodeError.value = ''
}

// onTurnstileExpire clears both challenge proof fields after widget expiration. It
// publishes the translated expiry error, requiring a later verification callback
// or action challenge before guarded submission can proceed.
function onTurnstileExpire() {
  turnstileToken.value = ''
  tencentCaptchaRandstr.value = ''
  sendCodeError.value = t('auth.turnstileExpired')
}

// onTurnstileError clears both challenge proof fields after widget failure. It
// publishes the translated verification error so a failed proof cannot remain
// available for a later form submission.
function onTurnstileError() {
  turnstileToken.value = ''
  tencentCaptchaRandstr.value = ''
  sendCodeError.value = t('auth.turnstileFailed')
}

// acquireActionProof obtains a fresh proof only for enabled action-based captcha
// providers. It returns true immediately otherwise; cancellation or an unavailable
// widget returns false, and successful proof fields replace the local values.
async function acquireActionProof(): Promise<boolean> {
  if (!actionCaptchaEnabled.value) return true

  const proof = await turnstileRef.value?.verifyAction()
  if (!proof) return false

  turnstileToken.value = proof.token
  tencentCaptchaRandstr.value = proof.randstr
  return true
}

// handleSendCode requests verification for the trimmed pending-account email after
// captcha gating. It starts the returned resend countdown on success, reports
// endpoint errors, and resets any enabled challenge after the request settles.
async function handleSendCode() {
  const trimmedEmail = email.value.trim()
  if (!trimmedEmail) {
    return
  }

  if (turnstileEnabled.value && !turnstileToken.value) {
    sendCodeError.value = t('auth.completeVerification')
    return
  }

  if (!(await acquireActionProof())) {
    return
  }

  isSendingCode.value = true
  sendCodeError.value = ''
  sendCodeSuccess.value = false

  try {
    const response = await sendPendingOAuthVerifyCode({
      email: trimmedEmail,
      turnstile_token:
        turnstileEnabled.value || aliyunCaptchaEnabled.value ? turnstileToken.value : undefined,
      tencent_captcha_ticket: tencentCaptchaEnabled.value ? turnstileToken.value : undefined,
      tencent_captcha_randstr: tencentCaptchaEnabled.value
        ? tencentCaptchaRandstr.value
        : undefined,
    })
    sendCodeSuccess.value = true
    startCountdown(response.countdown)
  } catch (error: unknown) {
    sendCodeError.value = getRequestErrorMessage(error, t('auth.sendCodeFailed'))
  } finally {
    // A submitted request or reported server error can consume this proof. Reset
    // the widget and local values before the user retries the action.
    if (captchaEnabled.value) {
      resetTurnstile()
    }
    isSendingCode.value = false
  }
}

// handleSubmit emits a trimmed account draft after validating credentials and
// obtaining any required action proof. It rejects implicit submission without
// prepared Turnstile proof, includes only configured provider proof fields, and
// resets action proof after emission.
async function handleSubmit() {
  const trimmedEmail = email.value.trim()
  if (!trimmedEmail || password.value.length < 6) {
    return
  }

  // Sending an email code consumes the prepared Turnstile proof. Implicit form
  // submission also reaches this guard, so it cannot bypass the disabled button
  // with an empty token.

  if (turnstileEnabled.value && !turnstileToken.value) {
    sendCodeError.value = t('auth.completeVerification')
    return
  }

  if (!(await acquireActionProof())) {
    return
  }

  emit('submit', {
    email: trimmedEmail,
    password: password.value,
    verifyCode: emailVerifyEnabled.value ? verifyCode.value.trim() : '',
    ...((turnstileEnabled.value || aliyunCaptchaEnabled.value) && turnstileToken.value
      ? { turnstileToken: turnstileToken.value }
      : {}),
    ...(tencentCaptchaEnabled.value && turnstileToken.value
      ? {
          tencentCaptchaTicket: turnstileToken.value,
          tencentCaptchaRandstr: tencentCaptchaRandstr.value,
        }
      : {}),
    invitationCode: invitationCode.value.trim() || undefined,
  })

  // The parent has received the action proof in its payload. Reset the local
  // challenge now so another creation action obtains a fresh result.
  if (actionCaptchaEnabled.value) {
    resetTurnstile()
  }
}

// emitSwitchToBind emits the trimmed email for existing-account authentication.
function emitSwitchToBind() {
  emit('switchToBind', email.value.trim())
}

// Initialize this page from public settings before user actions depend on its
// configured authentication requirements, retaining the documented defaults when
// that request fails.
onMounted(async () => {
  try {
    const settings = await getPublicSettings()
    invitationCodeEnabled.value = settings.invitation_code_enabled === true
    emailVerifyEnabled.value = settings.email_verify_enabled !== false
    turnstileEnabled.value = settings.turnstile_enabled === true
    turnstileSiteKey.value = settings.turnstile_site_key || ''
    tencentCaptchaEnabled.value = settings.tencent_captcha_enabled === true
    tencentCaptchaAppId.value = settings.tencent_captcha_app_id || ''
    tencentCaptchaRegion.value = settings.tencent_captcha_region || 'cn'
    aliyunCaptchaEnabled.value = settings.aliyun_captcha_enabled === true
    aliyunCaptchaSceneId.value = settings.aliyun_captcha_scene_id || ''
    aliyunCaptchaPrefix.value = settings.aliyun_captcha_prefix || ''
    aliyunCaptchaRegion.value = settings.aliyun_captcha_region || 'cn'
  } catch {
    // On settings failure, the pending-account form keeps email verification
    // enabled while disabling invitations and external captcha requirements for
    // its fallback display.
    invitationCodeEnabled.value = false
    emailVerifyEnabled.value = true
    turnstileEnabled.value = false
    turnstileSiteKey.value = ''
    tencentCaptchaEnabled.value = false
    tencentCaptchaAppId.value = ''
    tencentCaptchaRegion.value = 'cn'
    aliyunCaptchaEnabled.value = false
    aliyunCaptchaSceneId.value = ''
    aliyunCaptchaPrefix.value = ''
    aliyunCaptchaRegion.value = 'cn'
  }
})

// Release the component-owned timer or rendered resource when this view leaves, so
// detached UI does not retain its scheduled work.
onUnmounted(() => {
  clearCountdown()
})
</script>

<style scoped></style>
