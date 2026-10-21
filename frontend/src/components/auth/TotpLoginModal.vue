<template>
  <AuthCardDialog
    :show="show"
    @after-leave="$emit('after-leave')"
    :close-on-overlay="false"
    @close="$emit('cancel')"
  >
    <div class="mb-6 text-center">
      <div
        class="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary-100 dark:bg-primary-900/30"
      >
        <Icon name="shield" size="lg" class="h-6 w-6 text-primary-600 dark:text-primary-400" />
      </div>
      <h3 class="mt-4 text-xl font-semibold text-gray-900 dark:text-white">
        {{ t('profile.totp.loginTitle') }}
      </h3>
      <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.totp.loginHint') }}
      </p>
      <p v-if="userEmailMasked" class="mt-1 text-sm font-medium text-gray-700 dark:text-gray-300">
        {{ userEmailMasked }}
      </p>
    </div>

    <div class="mb-6">
      <!--
        Keep a hidden target for system OTP autofill while password managers use
        the first visible cell, which accepts a complete six-digit value.
      -->
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
          :name="index === 0 ? 'totp_login_code' : undefined"
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
      @click="$emit('cancel')"
    >
      {{ t('common.cancel') }}
    </button>
  </AuthCardDialog>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import AuthCardDialog from '@/components/common/AuthCardDialog.vue'
import { ref, watch, nextTick, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'

// TotpLoginModal.vue collects a six-digit one-time code through visible cells and
// a hidden browser-autofill input. Browsers and password managers can deliver the
// full code to one field, so the input handlers distribute filtered digits into
// both reactive state and the visible DOM.
//
// The parent owns login verification and uses exposed methods to mark requests
// busy or reset rejected input. A remembered submitted value suppresses duplicate
// input and change notifications for the same complete code until the draft
// changes or an error resets it. Paste uses the same distribution path and focuses
// the next available cell after Vue updates the view.

withDefaults(
  defineProps<{
    // show defaults to true for direct callers. The login page can set it false
    // while the leaving-presence owner retains this component until transition
    // completion.
    show?: boolean // dialog presentation flag

    // tempToken is supplied by the login parent with challenge context. This
    // component emits only the entered code, leaving token submission to the
    // parent.
    tempToken: string // temporary authentication challenge token

    userEmailMasked?: string // masked challenge account email
  }>(),
  { show: true },
)

// emit reports completed codes, cancellation, and the end of the dialog
// transition. The login parent owns challenge submission and keeps the dialog
// mounted through its leaving state.
const emit = defineEmits<{
  'after-leave': [] // completed dialog leave transition

  verify: [code: string] // complete numeric OTP value

  cancel: [] // user cancelled authentication challenge
}>() // parent component event dispatcher

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
// lastSubmittedCode suppresses duplicate completed OTP notifications. Partial
// input or an exposed error reset clears it so the same value can be submitted
// again.
const lastSubmittedCode = ref('') // last automatically submitted OTP

// When the joined draft reaches six digits, emit it once per completed value. The
// remembered value suppresses duplicate notifications from multiple autofill
// targets.
watch(
  () => code.value.join(''),
  (newCode) => {
    if (!/^[0-9]{6}$/.test(newCode)) {
      lastSubmittedCode.value = ''
      return
    }
    if (!verifying.value && newCode !== lastSubmittedCode.value) {
      lastSubmittedCode.value = newCode
      emit('verify', newCode)
    }
  },
)

defineExpose({
  // setVerifying lets the login parent control the OTP busy state.
  setVerifying: (value: boolean) => {
    verifying.value = value
  },
  // setError reports a nonempty verification error and clears all OTP
  // representations. It also clears duplicate-submission history and refocuses the
  // first field after rendering so the same code can be retried.
  setError: (message: string) => {
    if (message) {
      appStore.showError(message)
    }
    code.value = ['', '', '', '', '', '']
    lastSubmittedCode.value = ''
    // The visible cells also hold direct DOM values from autofill. Clear them with
    // the reactive draft so rejected digits are no longer displayed.
    inputRefs.value.forEach((input) => {
      if (input) input.value = ''
    })
    // Clear the hidden autofill target as well as the displayed cells so a
    // subsequent browser event cannot reintroduce the rejected code.
    if (hiddenOtpInputRef.value) {
      hiddenOtpInputRef.value.value = ''
    }
    nextTick(() => {
      inputRefs.value[0]?.focus()
    })
  },
})

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
    nextTick(() => {
      inputRefs.value[index + 1]?.focus()
    })
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
  nextTick(() => {
    inputRefs.value[focusIndex]?.focus()
  })
}

// Wait for the visible OTP cells to render before focusing the first one, so
// keyboard entry starts at an attached input element.
onMounted(() => {
  nextTick(() => {
    inputRefs.value[0]?.focus()
  })
})
</script>
