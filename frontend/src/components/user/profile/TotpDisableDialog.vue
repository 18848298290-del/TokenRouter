<template>
  <AuthCardDialog :show="show" @after-leave="$emit('after-leave')" @close="$emit('close')">
    <!-- Header -->
    <div class="mb-6">
      <div
        class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-red-100 dark:bg-red-900/30"
      >
        <Icon name="exclamationTriangle" size="lg" class="h-6 w-6 text-red-600 dark:text-red-400" />
      </div>
      <h3 class="mt-4 text-center text-xl font-semibold text-gray-900 dark:text-white">
        {{ t('profile.totp.disableTitle') }}
      </h3>
      <p class="mt-2 text-center text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.totp.disableWarning') }}
      </p>
    </div>

    <!-- Loading verification method -->
    <ContentSkeleton v-if="methodLoading" variant="form" :rows="2" class="py-4" />

    <form v-else @submit.prevent="handleDisable" class="space-y-4">
      <!-- Email verification -->
      <div v-if="verificationMethod === 'email'">
        <label class="input-label">{{ t('profile.totp.emailCode') }}</label>
        <div class="flex gap-2">
          <input
            v-model="form.emailCode"
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

      <!-- Password verification -->
      <div v-else>
        <label for="password" class="input-label">
          {{ t('profile.currentPassword') }}
        </label>
        <input
          id="password"
          v-model="form.password"
          type="password"
          autocomplete="current-password"
          class="input"
          :placeholder="t('profile.totp.enterPassword')"
        />
      </div>

      <!-- Actions -->
      <div class="flex justify-end gap-3 pt-4">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">
          {{ t('common.cancel') }}
        </button>
        <button type="submit" class="btn btn-danger" :disabled="loading || !canSubmit">
          {{ loading ? t('common.processing') : t('profile.totp.confirmDisable') }}
        </button>
      </div>
    </form>
  </AuthCardDialog>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import Icon from '@/components/icons/Icon.vue'
import AuthCardDialog from '@/components/common/AuthCardDialog.vue'
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { totpAPI } from '@/api'

// TotpDisableDialog.vue disables account TOTP after the server-selected identity
// verification step. It submits either an email code or a password, reports completion to
// the profile card, and clears an existing resend interval when the retained dialog instance
// unmounts. A send request still pending at disposal can later create another interval.

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

// Verification-method state gates the initial form; loading and sendingCode track requests,
// while codeCooldown and cooldownTimer retain resend feedback and the currently registered
// interval for disposal to clear.
const methodLoading = ref(true)
const verificationMethod = ref<'email' | 'password'>('password')
const loading = ref(false)
const sendingCode = ref(false)
const codeCooldown = ref(0)
const cooldownTimer = ref<ReturnType<typeof setInterval> | null>(null)
// form retains the email verification code or current password; the selected server
// verification method determines which value is submitted.
const form = ref({
  emailCode: '', // Email verification digits.
  password: '', // Current account password.
})

// canSubmit requires six email-code characters or a nonempty password according to the
// server-selected verification method.
const canSubmit = computed(() => {
  if (verificationMethod.value === 'email') {
    return form.value.emailCode.length === 6
  }
  return form.value.password.length > 0
})

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

// handleDisable submits the identity proof selected by the server and disables TOTP. Invalid
// local input is ignored, success notifies the parent, and API failure leaves the dialog
// open for correction.
const handleDisable = async () => {
  if (!canSubmit.value) return

  loading.value = true

  try {
    const request =
      verificationMethod.value === 'email'
        ? { email_code: form.value.emailCode }
        : { password: form.value.password }

    await totpAPI.disable(request)
    appStore.showSuccess(t('profile.totp.disableSuccess'))
    emit('success')
  } catch (err: any) {
    appStore.showError(err.response?.data?.message || t('profile.totp.disableFailed'))
  } finally {
    loading.value = false
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
