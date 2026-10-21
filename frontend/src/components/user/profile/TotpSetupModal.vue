<template>
  <AuthCardDialog :show="show" @after-leave="$emit('after-leave')" @close="$emit('close')">
    <!-- Header -->
    <div class="mb-6 text-center">
      <h3 class="text-xl font-semibold text-gray-900 dark:text-white">
        {{ t('profile.totp.setupTitle') }}
      </h3>
      <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">
        {{ stepDescription }}
      </p>
    </div>

    <!-- Step 0: Identity Verification -->
    <div v-if="step === 0" v-content-reveal class="space-y-6">
      <!-- Loading verification method -->
      <ContentSkeleton v-if="methodLoading" variant="form" :rows="2" class="py-4" />

      <template v-else>
        <!-- Email verification -->
        <div v-if="verificationMethod === 'email'" class="space-y-4">
          <div>
            <label class="input-label">{{ t('profile.totp.emailCode') }}</label>
            <div class="flex gap-2">
              <input
                v-model="verifyForm.emailCode"
                type="text"
                maxlength="6"
                inputmode="numeric"
                class="input flex-1"
                :placeholder="t('profile.totp.enterEmailCode')"
              />
              <button
                type="button"
                class="btn btn-secondary whitespace-nowrap"
                :disabled="sendingCode || codeCooldown > 0"
                @click="handleSendCode"
              >
                {{
                  codeCooldown > 0
                    ? `${codeCooldown}s`
                    : sendingCode
                      ? t('common.sending')
                      : t('profile.totp.sendCode')
                }}
              </button>
            </div>
          </div>
        </div>

        <!-- Password verification -->
        <div v-else class="space-y-4">
          <div>
            <label class="input-label">{{ t('profile.currentPassword') }}</label>
            <input
              v-model="verifyForm.password"
              type="password"
              autocomplete="current-password"
              class="input"
              :placeholder="t('profile.totp.enterPassword')"
            />
          </div>
        </div>

        <div class="flex justify-end gap-3 pt-4">
          <button type="button" class="btn btn-secondary" @click="$emit('close')">
            {{ t('common.cancel') }}
          </button>
          <button
            type="button"
            class="btn btn-primary"
            :disabled="!canProceedFromVerify || setupLoading"
            @click="handleVerifyAndSetup"
          >
            {{ setupLoading ? t('common.loading') : t('common.next') }}
          </button>
        </div>
      </template>
    </div>

    <!-- Step 1: Show QR Code -->
    <div v-if="step === 1" v-content-reveal class="space-y-6">
      <!-- QR Code and Secret -->
      <template v-if="setupData">
        <div class="flex justify-center">
          <div
            class="rounded-surface border border-gray-200 p-4 bg-white dark:border-dark-600 dark:bg-white"
          >
            <img :src="qrCodeDataUrl" alt="QR Code" class="h-48 w-48" />
          </div>
        </div>

        <div class="text-center">
          <p class="text-sm text-gray-500 dark:text-gray-400 mb-2">
            {{ t('profile.totp.manualEntry') }}
          </p>
          <div class="flex items-center justify-center gap-2">
            <code class="rounded-compact bg-gray-100 px-3 py-2 font-mono text-sm dark:bg-dark-700">
              {{ setupData.secret }}
            </code>
            <button
              type="button"
              class="rounded-compact p-1.5 text-gray-500 hover:bg-gray-100 dark:hover:bg-dark-700"
              @click="copySecret"
            >
              <Icon name="clipboard" size="sm" />
            </button>
          </div>
        </div>
      </template>

      <div class="flex justify-end gap-3 pt-4">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="!setupData" @click="step = 2">
          {{ t('common.next') }}
        </button>
      </div>
    </div>

    <!-- Step 2: Verify Code -->
    <div v-if="step === 2" v-content-reveal class="space-y-6">
      <form @submit.prevent="handleVerify">
        <div class="mb-6">
          <label class="input-label text-center block mb-3">
            {{ t('profile.totp.enterCode') }}
          </label>
          <div class="flex justify-center gap-2">
            <input
              v-for="(_, index) in 6"
              :key="index"
              :ref="(el) => setInputRef(el, index)"
              data-testid="totp-digit-input"
              type="text"
              :maxlength="index === 0 ? 6 : 1"
              inputmode="numeric"
              :pattern="index === 0 ? '[0-9]{1,6}' : '[0-9]'"
              :autocomplete="index === 0 ? 'one-time-code' : 'off'"
              :name="index === 0 ? 'totp_setup_code' : undefined"
              class="h-12 w-10 rounded-control border border-gray-300 text-center text-lg font-semibold focus:border-primary-500 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
              @input="handleCodeInput($event, index)"
              @change="handleCodeInput($event, index)"
              @keydown="handleKeydown($event, index)"
              @paste="handlePaste"
            />
          </div>
        </div>

        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="step = 1">
            {{ t('common.back') }}
          </button>
          <button
            type="submit"
            class="btn btn-primary"
            :disabled="verifying || code.join('').length !== 6"
          >
            {{ verifying ? t('common.verifying') : t('profile.totp.verify') }}
          </button>
        </div>
      </form>
    </div>
  </AuthCardDialog>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { vContentReveal } from '@/directives/contentReveal'

import Icon from '@/components/icons/Icon.vue'
import AuthCardDialog from '@/components/common/AuthCardDialog.vue'
import { ref, onMounted, onUnmounted, nextTick, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { totpAPI } from '@/api'
import type { TotpSetupResponse } from '@/api/totp.types'
import QRCode from 'qrcode'

// TotpSetupModal.vue enrolls account TOTP through identity verification, QR display, and
// code confirmation. The server chooses the identity proof and returns a setup token tied to
// the issued secret; confirmation submits that token with the six-digit code before
// reporting success to the profile card.
//
// Confirmation input supports individual typing, clipboard paste, and password-manager
// autofill into one cell. Whole-code replacements synchronize reactive values and visible
// inputs while clearing unused trailing digits, preventing mixed attempts from being
// submitted. Disposal clears the email resend interval already registered when the parent
// releases the retained instance after its exit transition. A pending send can still
// complete afterward and register a new countdown.

// show controls the credential dialog shell; the parent waits for after-leave before
// unmounting the retained dialog instance.
withDefaults(defineProps<{ show?: boolean /* Dialog shell visibility. */ }>(), { show: true })

// close requests dismissal, success lets the profile card refresh status, and after-leave
// allows the parent to release the retained dialog instance.
const emit = defineEmits<{
  'after-leave': [] // Shell exit has completed.
  close: [] // Parent dismissal request.
  success: [] // TOTP operation completed.
}>()

// Localized messages and app notifications report identity-verification and TOTP operation
// results for this dialog instance.
const { t } = useI18n()
const appStore = useAppStore()

// step selects identity verification, QR display, or six-digit TOTP confirmation;
// verification request flags and cooldown state belong to this dialog instance.
const step = ref(0)
const methodLoading = ref(true)
const verificationMethod = ref<'email' | 'password'>('password')
const verifyForm = ref({
  emailCode: '', // Email verification digits.
  password: '', // Current account password.
})
const sendingCode = ref(false)
const codeCooldown = ref(0)
const cooldownTimer = ref<ReturnType<typeof setInterval> | null>(null)

// Setup request state retains the issued secret and token; code and inputRefs keep the six
// visible confirmation cells synchronized with the submitted digits.
const setupLoading = ref(false)
const setupData = ref<TotpSetupResponse | null>(null)
const verifying = ref(false)
const code = ref<string[]>(['', '', '', '', '', ''])
const inputRefs = ref<(HTMLInputElement | null)[]>([])
const qrCodeDataUrl = ref('')

// stepDescription chooses the localized instruction for the current setup stage and server-
// selected identity verification method before enrollment.
const stepDescription = computed(() => {
  switch (step.value) {
    case 0:
      return verificationMethod.value === 'email'
        ? t('profile.totp.verifyEmailFirst')
        : t('profile.totp.verifyPasswordFirst')
    case 1:
      return t('profile.totp.setupStep1')
    case 2:
      return t('profile.totp.setupStep2')
    default:
      return ''
  }
})

// canProceedFromVerify requires six email-code characters or a nonempty password before the
// setup action becomes available to the user.
const canProceedFromVerify = computed(() => {
  if (verificationMethod.value === 'email') {
    return verifyForm.value.emailCode.length === 6
  }
  return verifyForm.value.password.length > 0
})

// Encode the issued enrollment URI after setup data changes. QR generation errors
// retain the prior image and log the cause while the textual secret stays available.
watch(
  () => setupData.value?.qr_code_url,
  async (url) => {
    if (url) {
      try {
        qrCodeDataUrl.value = await QRCode.toDataURL(url, {
          width: 200,
          margin: 2,
          color: {
            dark: '#000000',
            light: '#ffffff',
          },
        })
      } catch (err) {
        console.error('Failed to generate QR code:', err)
      }
    }
  },
  { immediate: true },
)

// setInputRef records a confirmation cell element for focus and visible-value updates.
const setInputRef = (el: any, index: number) => {
  inputRefs.value[index] = el as HTMLInputElement | null
}

// handleCodeInput accepts digit input from one confirmation cell and synchronizes the six-
// cell code. Password managers can place a complete code into a single cell, so multi-digit
// input replaces the whole code through fillCodeDigits. Single digits update their own index
// and advance focus after the DOM update. Filtering and truncation restrict either path to
// six numeric characters, with final focus bounded to the last available cell.
const handleCodeInput = (event: Event, index: number) => {
  const input = event.target as HTMLInputElement
  const digits = input.value.replace(/[^0-9]/g, '').slice(0, 6)

  // Password-manager autofill can replace one DOM cell with all six digits, so
  // distribute that value across the complete code before moving the next focus.
  if (digits.length > 1) {
    fillCodeDigits(digits.split(''))
    const focusIndex = Math.min(digits.length, 5)
    nextTick(() => inputRefs.value[focusIndex]?.focus())
    return
  }

  const value = digits[0] || ''
  code.value[index] = value
  input.value = value

  if (value && index < 5) {
    nextTick(() => {
      inputRefs.value[index + 1]?.focus()
    })
  }
}

// fillCodeDigits replaces the entire six-cell confirmation code using at most six supplied
// entries. The caller supplies digits already filtered from user input. Empty slots are
// cleared so a shorter replacement cannot retain trailing digits from a previous code. Both
// the reactive array and mounted input values update together because password-manager
// writes can change a DOM input before Vue state receives the corresponding full-code
// replacement.
const fillCodeDigits = (digits: string[]) => {
  const nextCode = ['', '', '', '', '', '']
  const availableDigits = digits.slice(0, 6)

  availableDigits.forEach((digit, offset) => {
    nextCode[offset] = digit
  })
  for (let index = availableDigits.length; index < 6; index++) {
    nextCode[index] = ''
  }

  code.value = nextCode
  inputRefs.value.forEach((visibleInput, index) => {
    if (visibleInput) visibleInput.value = nextCode[index]
  })
}

// handleKeydown moves focus to the prior cell when Backspace begins in an empty nonfirst
// input. Other deletions use browser behavior and the input handler to update the reactive
// code.
const handleKeydown = (event: KeyboardEvent, index: number) => {
  if (event.key === 'Backspace') {
    const input = event.target as HTMLInputElement
    // An empty cell has nothing to delete, so Backspace moves to the prior cell
    // and prevents the browser from applying an unrelated deletion to this input.
    if (!input.value && index > 0) {
      event.preventDefault()
      inputRefs.value[index - 1]?.focus()
    }
    // Native deletion reaches handleCodeInput through the following input event,
    // which keeps the reactive code consistent with the remaining visible character.
  }
}

// handlePaste replaces confirmation cells with up to six numeric clipboard characters. It
// prevents native paste, synchronizes the visible inputs, and focuses the next available
// cell after Vue updates.
const handlePaste = (event: ClipboardEvent) => {
  event.preventDefault()
  const pastedData = event.clipboardData?.getData('text') || ''
  const digits = pastedData
    .replace(/[^0-9]/g, '')
    .slice(0, 6)
    .split('')

  // A paste replaces the complete code, including clearing unused trailing cells,
  // so digits from a previous attempt cannot remain in the submitted value.
  fillCodeDigits(digits)

  const focusIndex = Math.min(digits.length, 5)
  nextTick(() => {
    inputRefs.value[focusIndex]?.focus()
  })
}

// copySecret copies the issued setup secret when enrollment data exists. Clipboard rejection
// reports a localized copy error, while successful copying leaves the enrollment state and
// confirmation step unchanged.
const copySecret = async () => {
  if (setupData.value) {
    try {
      await navigator.clipboard.writeText(setupData.value.secret)
      appStore.showSuccess(t('common.copied'))
    } catch {
      appStore.showError(t('common.copyFailed'))
    }
  }
}

// loadVerificationMethod retrieves the identity check required before changing TOTP
// settings. Failure reports the API message and requests dialog closure, preventing
// submission with an assumed verification method.
const loadVerificationMethod = async () => {
  methodLoading.value = true
  try {
    const method = await totpAPI.getVerificationMethod()
    verificationMethod.value = method.method
  } catch (err: any) {
    appStore.showError(err.response?.data?.message || t('common.error'))
    emit('close')
  } finally {
    methodLoading.value = false
  }
}

// handleSendCode requests an email verification code and starts a sixty-second resend
// cooldown after success. It replaces any previous interval, reports request failures, and
// resets its sending flag when the request settles. Disposal clears the currently registered
// interval, but a pending send can succeed afterward and start another countdown.
const handleSendCode = async () => {
  sendingCode.value = true
  try {
    await totpAPI.sendVerifyCode()
    appStore.showSuccess(t('profile.totp.codeSent'))
    // Replace the previous countdown after a successful send so repeated requests
    // cannot leave multiple intervals decrementing the same resend cooldown.
    codeCooldown.value = 60
    if (cooldownTimer.value) {
      clearInterval(cooldownTimer.value)
      cooldownTimer.value = null
    }
    cooldownTimer.value = setInterval(() => {
      codeCooldown.value--
      if (codeCooldown.value <= 0) {
        if (cooldownTimer.value) {
          clearInterval(cooldownTimer.value)
          cooldownTimer.value = null
        }
      }
    }, 1000)
  } catch (err: any) {
    appStore.showError(err.response?.data?.message || t('profile.totp.sendCodeFailed'))
  } finally {
    sendingCode.value = false
  }
}

// handleVerifyAndSetup submits the server-selected identity proof and stores the resulting
// enrollment secret and setup token. A successful response advances to QR display; failures
// retain the verification step for correction.
const handleVerifyAndSetup = async () => {
  setupLoading.value = true

  try {
    const request =
      verificationMethod.value === 'email'
        ? { email_code: verifyForm.value.emailCode }
        : { password: verifyForm.value.password }

    setupData.value = await totpAPI.initiateSetup(request)
    step.value = 1
  } catch (err: any) {
    appStore.showError(err.response?.data?.message || t('profile.totp.setupFailed'))
  } finally {
    setupLoading.value = false
  }
}

// handleVerify submits six confirmation digits together with the enrollment token. Missing
// digits or setup data prevent submission; a rejected confirmation clears both reactive and
// visible inputs and restores focus to the first cell.
const handleVerify = async () => {
  const totpCode = code.value.join('')
  if (totpCode.length !== 6 || !setupData.value) return

  verifying.value = true

  try {
    await totpAPI.enable({
      totp_code: totpCode,
      setup_token: setupData.value.setup_token,
    })
    appStore.showSuccess(t('profile.totp.enableSuccess'))
    emit('success')
  } catch (err: any) {
    appStore.showError(err.response?.data?.message || t('profile.totp.verifyFailed'))
    // A rejected confirmation should not retain the attempted code. Clear both reactive and
    // browser-managed values before returning focus for the next attempt.
    code.value = ['', '', '', '', '', '']
    inputRefs.value.forEach((input) => {
      if (input) input.value = ''
    })
    nextTick(() => {
      inputRefs.value[0]?.focus()
    })
  } finally {
    verifying.value = false
  }
}

onMounted(() => {
  loadVerificationMethod()
})

// Clear the resend interval currently registered when the retained dialog unmounts. A send
// request that succeeds after disposal can still create another interval in its continuation.
onUnmounted(() => {
  if (cooldownTimer.value) {
    clearInterval(cooldownTimer.value)
    cooldownTimer.value = null
  }
})
</script>
