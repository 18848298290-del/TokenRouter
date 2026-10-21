<template>
  <AuthCardDialog :show="controller.visible.value" :z-index="60" @close="handleCancel">
    <div class="mb-6 text-center">
      <div
        class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary-100 dark:bg-primary-900/30"
      >
        <Icon name="lock" size="lg" class="h-6 w-6 text-primary-600 dark:text-primary-400" />
      </div>
      <h3 class="mt-4 text-xl font-semibold text-gray-900 dark:text-white">
        {{ t('stepUp.title') }}
      </h3>
      <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">
        {{ t('stepUp.hint') }}
      </p>
    </div>

    <div class="mb-6">
      <input
        ref="hiddenOtpInputRef"
        type="text"
        inputmode="numeric"
        autocomplete="one-time-code"
        maxlength="6"
        class="pointer-events-none absolute left-0 top-0 h-px w-px opacity-0"
        aria-hidden="true"
        tabindex="-1"
        data-1p-ignore
        @input="handleHiddenOtpInput"
        @change="handleHiddenOtpInput"
      />
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
          :name="index === 0 ? 'totp_step_up_code' : undefined"
          class="h-12 w-10 rounded-control border border-gray-300 text-center text-lg font-semibold focus:border-primary-500 focus:ring-primary-500 dark:border-dark-600 dark:bg-dark-700"
          :disabled="verifying"
          @input="handleCodeInput($event, index)"
          @change="handleCodeInput($event, index)"
          @keydown="handleKeydown($event, index)"
          @paste="handlePaste"
        />
      </div>
      <div
        v-if="verifying"
        class="mt-3 flex items-center justify-center gap-2 text-sm text-gray-500"
      >
        <div class="spinner h-4 w-4 text-primary-500"></div>
        {{ t('common.verifying') }}
      </div>
    </div>

    <button
      type="button"
      class="btn btn-secondary w-full"
      :disabled="verifying"
      @click="handleCancel"
    >
      {{ t('common.cancel') }}
    </button>
  </AuthCardDialog>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import AuthCardDialog from '@/components/common/AuthCardDialog.vue'
import { ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import { totpAPI } from '@/api'
import type { StepUpController } from '@/composables/useStepUp'

// TotpStepUpDialog.vue collects a six-digit one-time code through visible cells
// and a hidden browser-autofill input. Browsers and password managers can deliver
// the full code to one field, so the input handlers distribute filtered digits
// into both reactive state and the visible DOM.
//
// The component calls the step-up endpoint itself, then notifies the supplied
// controller after clearing input. Opening the dialog resets its draft, and
// cancellation is ignored while verification is pending. Failed verification keeps
// the dialog open and returns focus to the first cell. Paste uses the same
// distribution path and focuses the next available cell after Vue updates the
// view.

// props supplies the step-up controller that owns visibility and completion
// callbacks. Verification stays inside this dialog and reports its settled outcome
// through that controller.
const props = defineProps<{
  // controller owns dialog visibility and completion callbacks. Opening resets
  // inputs, successful verification notifies onVerified, and permitted
  // cancellation notifies onCancel.
  controller: StepUpController // owning step-up dialog controller
}>() // parent component inputs

// t resolves the authentication messages displayed by this component. Nearby store
// and navigation services deliver those messages and handle the requested
// authentication transition.
const { t } = useI18n() // translated authentication message resolver
const appStore = useAppStore() // public settings and toast owner

// verifying gates automatic OTP submission while code holds the segmented draft.
// inputRefs and hiddenOtpInputRef synchronize visible typing with full-code
// browser autofill and focus changes.
const verifying = ref(false) // OTP verification busy flag
const code = ref<string[]>(['', '', '', '', '', '']) // current callback code or OTP draft
const inputRefs = ref<(HTMLInputElement | null)[]>([]) // visible OTP input elements
const hiddenOtpInputRef = ref<HTMLInputElement | null>(null) // system autofill input element

// Each controller opening resets previous code values before scheduling focus, so
// reopening begins from an empty draft after the input elements render.
watch(
  () => props.controller.visible.value,
  (open) => {
    if (open) {
      resetInputs()
      nextTick(() => inputRefs.value[0]?.focus())
    }
  },
)

// A complete numeric draft starts step-up verification only while no request is
// busy, leaving partial input editable without contacting the verification
// endpoint.
watch(
  () => code.value.join(''),
  (newCode) => {
    if (/^[0-9]{6}$/.test(newCode) && !verifying.value) {
      submit(newCode)
    }
  },
)

// submit sends the completed OTP to the step-up endpoint and marks verification
// busy. Success clears inputs before notifying the controller; failure shows an
// error, clears inputs, and refocuses the first cell.
async function submit(otp: string) {
  verifying.value = true
  try {
    await totpAPI.stepUp(otp)
    verifying.value = false
    resetInputs()
    props.controller.onVerified()
  } catch (err: any) {
    verifying.value = false
    appStore.showError(err?.message || t('stepUp.verifyFailed'))
    resetInputs()
    nextTick(() => inputRefs.value[0]?.focus())
  }
}

// resetInputs clears the reactive OTP draft and both visible and hidden DOM
// inputs. Keeping all representations empty prevents a later autofill or
// verification attempt from retaining digits from the previous challenge.
function resetInputs() {
  code.value = ['', '', '', '', '', '']
  inputRefs.value.forEach((input) => {
    if (input) input.value = ''
  })
  if (hiddenOtpInputRef.value) hiddenOtpInputRef.value.value = ''
}

// handleCancel tells the step-up controller that the user cancelled. It ignores
// cancellation during verification so an outstanding verification request retains
// control of the dialog outcome until it settles.
function handleCancel() {
  if (verifying.value) return
  props.controller.onCancel()
}

// setInputRef stores a visible OTP element at its digit index. Vue can pass null
// during removal, so focus and synchronization callers check the saved element
// before accessing it.
const setInputRef = (el: any, index: number) => {
  inputRefs.value[index] = el as HTMLInputElement | null
}

// handleCodeInput accepts single-digit typing or a whole code delivered to one
// visible field. It filters to six numeric characters and delegates
// multi-character input to the shared distributor before moving focus.
// Single-character edits update the corresponding draft slot and DOM value
// together, then advance to the next available slot. This keeps browser autofill
// compatible with the segmented editor while leaving submission to its reactive
// watcher.
const handleCodeInput = (event: Event, index: number) => {
  const input = event.target as HTMLInputElement
  // Accept only the six numeric OTP positions, even when a browser autofill event
  // writes formatting characters or a complete code into one cell.
  const digits = input.value.replace(/[^0-9]/g, '').slice(0, 6)

  // Password managers can write an entire OTP into one cell. Distribute that value
  // across all cells before the reactive watcher evaluates the completed code.
  if (digits.length > 1) {
    fillCodeDigits(digits.split(''))
    // Keep focus within the six available OTP cells.
    const focusIndex = Math.min(digits.length, 5)
    nextTick(() => inputRefs.value[focusIndex]?.focus())
    return
  }

  const value = digits[0] || ''
  code.value[index] = value
  input.value = value
  if (value && index < 5) {
    nextTick(() => inputRefs.value[index + 1]?.focus())
  }
}

// fillCodeDigits distributes at most six supplied characters across the complete
// OTP draft. It replaces the array and updates every visible input because
// autofill and paste can supply an entire code through one DOM field. Unused cells
// are cleared, preventing trailing digits from an earlier code from joining a
// shorter replacement. The caller owns character filtering, and the reactive
// watcher observes the resulting joined value for submission.
const fillCodeDigits = (digits: string[]) => {
  const nextCode = ['', '', '', '', '', '']
  // Limit distribution to the six displayed OTP cells.
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

// handleHiddenOtpInput filters the hidden autofill value and distributes up to six
// digits. It shares the visible-input synchronization path so system autofill can
// trigger the same code watcher as typing.
const handleHiddenOtpInput = (event: Event) => {
  const input = event.target as HTMLInputElement
  const digits = input.value
    .replace(/[^0-9]/g, '')
    .slice(0, 6)
    .split('')
  fillCodeDigits(digits)
}

// handleKeydown moves focus backward only for Backspace in an empty nonfirst cell.
// Other deletion remains with the browser, whose subsequent input event updates
// the reactive digit value.
const handleKeydown = (event: KeyboardEvent, index: number) => {
  if (event.key === 'Backspace') {
    const input = event.target as HTMLInputElement
    if (!input.value && index > 0) {
      event.preventDefault()
      inputRefs.value[index - 1]?.focus()
    }
  }
}

// handlePaste replaces the segmented code with filtered clipboard digits and
// prevents native insertion. It distributes at most six digits, then focuses the
// next cell or the last cell after rendering.
const handlePaste = (event: ClipboardEvent) => {
  event.preventDefault()
  const pastedData = event.clipboardData?.getData('text') || ''
  const digits = pastedData
    .replace(/[^0-9]/g, '')
    .slice(0, 6)
    .split('')
  fillCodeDigits(digits)
  // Keep focus within the six available OTP cells.
  const focusIndex = Math.min(digits.length, 5)
  nextTick(() => inputRefs.value[focusIndex]?.focus())
}
</script>
