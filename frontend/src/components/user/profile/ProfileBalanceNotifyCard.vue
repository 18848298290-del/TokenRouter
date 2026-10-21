<template>
  <div class="card">
    <div class="border-b border-gray-100 px-6 py-4 dark:border-dark-700">
      <h2 class="text-lg font-medium text-gray-900 dark:text-white">
        {{ t('profile.balanceNotify.title') }}
      </h2>
      <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">
        {{ t('profile.balanceNotify.description') }}
      </p>
    </div>
    <div class="px-6 py-6 space-y-6">
      <!-- Enable toggle -->
      <div class="flex items-center justify-between">
        <label class="input-label mb-0">{{ t('profile.balanceNotify.enabled') }}</label>
        <label class="relative inline-flex items-center cursor-pointer">
          <Toggle
            v-model="notifyEnabled"
            variant="flush"
            off-tone="soft"
            @update:model-value="handleToggle"
          />
        </label>
      </div>

      <template v-if="notifyEnabled">
        <!-- Custom threshold with save button -->
        <div>
          <label class="input-label">
            {{ t('profile.balanceNotify.threshold') }}
            <span class="text-xs text-gray-400 ml-2">{{
              t('profile.balanceNotify.thresholdHint')
            }}</span>
          </label>
          <div class="flex items-center gap-2">
            <span class="text-gray-500">{{ balanceUnitSymbol }}</span>
            <input
              v-model.number="customThreshold"
              type="number"
              min="0"
              step="0.01"
              class="input flex-1"
              :placeholder="
                systemDefaultThreshold > 0
                  ? `${t('profile.balanceNotify.systemDefault')} ${formatBalanceAmount(systemDefaultThreshold, { fractionDigits: 2 })}`
                  : t('profile.balanceNotify.thresholdPlaceholder')
              "
            />
            <button
              @click="handleThresholdUpdate"
              :disabled="savingThreshold"
              class="btn btn-primary btn-sm whitespace-nowrap"
            >
              {{ savingThreshold ? t('common.saving') : t('common.save') }}
            </button>
          </div>
        </div>

        <!-- Email list with toggles -->
        <div>
          <label class="input-label">{{ t('profile.balanceNotify.extraEmails') }}</label>
          <p class="mb-2 text-xs text-yellow-600 dark:text-yellow-400">
            {{ t('profile.balanceNotify.extraEmailsHint') }}
          </p>

          <!-- Saved email entries -->
          <div v-if="emailEntries.length > 0" class="space-y-2 mb-3">
            <div
              v-for="(entry, idx) in emailEntries"
              :key="idx"
              class="flex items-center justify-between px-3 py-2 bg-gray-50 dark:bg-dark-700 rounded-control"
            >
              <div class="flex items-center gap-2 min-w-0 flex-1">
                <label class="relative inline-flex items-center cursor-pointer shrink-0">
                  <Toggle
                    :model-value="!entry.disabled"
                    size="sm"
                    off-tone="soft"
                    @update:model-value="handleEmailToggle(entry)"
                  />
                </label>
                <span class="text-sm text-gray-700 dark:text-gray-300 truncate">{{
                  entry.email
                }}</span>
              </div>
              <div class="flex items-center gap-2 shrink-0">
                <template v-if="!entry.verified">
                  <!-- Inline verify flow for saved unverified emails -->
                  <template v-if="verifyingEmail === entry.email">
                    <input
                      v-model="verifyCode"
                      type="text"
                      maxlength="6"
                      class="input min-h-0 w-20 rounded-compact px-2 py-1 text-xs"
                      :placeholder="t('profile.balanceNotify.codePlaceholder')"
                    />
                    <button
                      @click="verifySavedEmail(entry.email)"
                      :disabled="!verifyCode || verifyCode.length !== 6 || verifyingSaved"
                      class="text-xs text-primary-600 hover:text-primary-700"
                    >
                      {{ t('profile.balanceNotify.verify') }}
                    </button>
                    <span v-if="verifyCountdown > 0" class="text-xs text-gray-400"
                      >{{ verifyCountdown }}s</span
                    >
                    <button
                      v-else
                      @click="sendCodeForSaved(entry.email)"
                      :disabled="sendingSavedCode"
                      class="text-xs text-gray-500 hover:text-gray-700"
                    >
                      {{ t('profile.balanceNotify.resend') }}
                    </button>
                    <button
                      @click="verifyingEmail = ''"
                      class="text-xs text-gray-400 hover:text-gray-600"
                    >
                      {{ t('common.cancel') }}
                    </button>
                  </template>
                  <template v-else>
                    <button
                      @click="sendCodeForSaved(entry.email)"
                      :disabled="sendingSavedCode"
                      class="text-xs text-primary-600 hover:text-primary-700"
                    >
                      {{ t('profile.balanceNotify.verify') }}
                    </button>
                    <span class="text-xs text-yellow-500">{{
                      t('profile.balanceNotify.unverified')
                    }}</span>
                  </template>
                </template>
                <span v-else class="text-xs text-green-500">{{
                  t('profile.balanceNotify.verified')
                }}</span>
                <button
                  @click="handleRemoveEmail(entry.email)"
                  class="text-red-500 hover:text-red-700 text-xs"
                >
                  {{ t('profile.balanceNotify.removeEmail') }}
                </button>
              </div>
            </div>
          </div>

          <!-- Pending (unverified) emails in verification flow -->
          <div v-if="pendingEmails.length > 0" class="space-y-2 mb-3">
            <div
              v-for="(pe, idx) in pendingEmails"
              :key="pe.email"
              class="flex items-center gap-2 px-3 py-2 bg-yellow-50 dark:bg-yellow-900/10 rounded-control border border-yellow-200 dark:border-yellow-800"
            >
              <span class="flex-1 text-sm text-gray-700 dark:text-gray-300">{{ pe.email }}</span>
              <div v-if="!pe.codeSent" class="flex items-center gap-1">
                <button
                  @click="sendCodeFor(idx)"
                  :disabled="pe.sending"
                  class="text-xs text-primary-600 hover:text-primary-700"
                >
                  {{ t('profile.balanceNotify.sendCode') }}
                </button>
                <button
                  @click="pendingEmails.splice(idx, 1)"
                  class="text-xs text-red-500 hover:text-red-700 ml-1"
                >
                  {{ t('profile.balanceNotify.removeEmail') }}
                </button>
              </div>
              <div v-else class="flex items-center gap-1">
                <input
                  v-model="pe.code"
                  type="text"
                  maxlength="6"
                  class="input min-h-0 w-20 rounded-compact px-2 py-1 text-xs"
                  :placeholder="t('profile.balanceNotify.codePlaceholder')"
                />
                <button
                  @click="verifyPending(idx)"
                  :disabled="!pe.code || pe.code.length !== 6 || pe.verifying"
                  class="text-xs text-primary-600 hover:text-primary-700"
                >
                  {{ t('profile.balanceNotify.verify') }}
                </button>
                <span v-if="pe.countdown > 0" class="text-xs text-gray-400"
                  >{{ pe.countdown }}s</span
                >
                <button
                  v-else
                  @click="sendCodeFor(idx)"
                  :disabled="pe.sending"
                  class="text-xs text-gray-500 hover:text-gray-700"
                >
                  {{ t('profile.balanceNotify.resend') }}
                </button>
              </div>
            </div>
          </div>

          <!-- Add new email input (hidden when at limit) -->
          <div v-if="canAddMore" class="flex gap-2">
            <input
              v-model="newEmail"
              type="email"
              class="input flex-1"
              :placeholder="t('profile.balanceNotify.emailPlaceholder')"
              @keyup.enter="addPendingEmail"
            />
            <button
              @click="addPendingEmail"
              :disabled="!newEmail"
              class="btn btn-secondary whitespace-nowrap"
            >
              {{ t('common.add') }}
            </button>
          </div>
          <p v-else class="text-xs text-gray-400">
            {{ t('profile.balanceNotify.maxEmailsReached') }}
          </p>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { userAPI } from '@/api'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { extractApiErrorMessage } from '@/utils/apiError'
import type { NotifyEmailEntry } from '@/api/notifications.types'
import Toggle from '@/components/common/Toggle.vue'

// ProfileBalanceNotifyCard.vue edits balance-notification settings and verifies additional
// delivery addresses. Persisted profile fields are mirrored into local drafts, while pending
// addresses retain their own code, request flags, and countdown interval. Saved unverified
// addresses share a separate verification form and timer.
//
// Verification and removal mutate server state before requesting a fresh profile, so a
// failed refresh can follow a successful mutation. Pending verification also removes its
// draft before that refresh. Existing countdown handles are cleared on unmount, but in-
// flight send requests are not cancelled and can finish later; handlers and template
// controls rely on stable pending row indices while requests run.

// maxTotalEmails limits the combined saved and pending notification-address rows offered by
// the add-email control in this card.
const maxTotalEmails = 3

// PendingEmail retains an unsaved notification address and its verification request state.
interface PendingEmail {
  email: string // Trimmed notification destination.
  codeSent: boolean // Verification form availability flag.
  code: string // Entered verification code.
  sending: boolean // Code request in progress.
  verifying: boolean // Verification request in progress.
  countdown: number // Remaining resend cooldown seconds.
  // timer owns this row's countdown interval until completion, successful verification,
  // or component disposal clears the handle that is currently stored here.
  timer: ReturnType<typeof setInterval> | null
}

// The parent supplies persisted notification settings and the account email; local drafts
// follow later profile updates through the watchers below.
const props = defineProps<{
  enabled: boolean // Persisted notification enablement flag.
  threshold: number | null // Optional custom balance threshold.
  extraEmails: NotifyEmailEntry[] // Persisted notification email records.
  systemDefaultThreshold: number // System balance notification threshold.
  userEmail: string // Account email for prefilling.
}>()

// Auth state receives refreshed profiles while app notifications and configured balance
// formatting describe notification settings and request outcomes.
const { t } = useI18n()
const authStore = useAuthStore()
const appStore = useAppStore()
const { balanceUnitSymbol, formatBalanceAmount } = useBalanceDisplay()

// notifyEnabled, customThreshold, and emailEntries mirror persisted settings; pendingEmails
// and newEmail hold unsaved address verification drafts within this card.
const notifyEnabled = ref(props.enabled)
const customThreshold = ref<number | null>(props.threshold)
const emailEntries = ref<NotifyEmailEntry[]>([...props.extraEmails])
const pendingEmails = ref<PendingEmail[]>([])
const newEmail = ref('')
const savingThreshold = ref(false)

// verifyingEmail and verifyCode identify the saved address being verified; request flags and
// a single countdown timer govern that shared verification form.
const verifyingEmail = ref('')
const verifyCode = ref('')
const verifyingSaved = ref(false)
const sendingSavedCode = ref(false)
const verifyCountdown = ref(0)
let verifyTimer: ReturnType<typeof setInterval> | null = null

// canAddMore reports whether saved and pending addresses leave room below the three-row
// limit, controlling whether the template displays the add-email input.
const canAddMore = computed(() => {
  return emailEntries.value.length + pendingEmails.value.length < maxTotalEmails
})

watch(
  () => props.enabled,
  (val) => {
    notifyEnabled.value = val
  },
)
watch(
  () => props.threshold,
  (val) => {
    customThreshold.value = val
  },
)
watch(
  () => props.extraEmails,
  (val) => {
    emailEntries.value = [...val]
  },
)

// When list is empty on mount, pre-fill the add input with user's email
onMounted(() => {
  if (emailEntries.value.length === 0 && props.userEmail) {
    newEmail.value = props.userEmail
  }
})

// Clear the currently registered pending-row and saved-address countdowns when the card
// unmounts, releasing intervals that would otherwise keep changing detached form state.
onUnmounted(() => {
  for (const pe of pendingEmails.value) {
    if (pe.timer) clearInterval(pe.timer)
  }
  if (verifyTimer) clearInterval(verifyTimer)
})

// handleToggle saves the current notification-enabled value and publishes the returned
// profile. Failure reports the API error and reverses the local switch value to recover from
// the attempted toggle.
const handleToggle = async () => {
  try {
    const updated = await userAPI.updateProfile({ balance_notify_enabled: notifyEnabled.value })
    authStore.user = updated
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
    // The input already changed the local switch before saving. On persistence failure, flip
    // the current value as recovery for the optimistic update shown by this control.
    notifyEnabled.value = !notifyEnabled.value
  }
}

// handleThresholdUpdate saves a positive custom threshold or zero for the default setting.
// It publishes the returned profile after success and resets its save indicator after either
// success or failure.
const handleThresholdUpdate = async () => {
  savingThreshold.value = true
  try {
    const threshold = customThreshold.value && customThreshold.value > 0 ? customThreshold.value : 0
    const updated = await userAPI.updateProfile({ balance_notify_threshold: threshold })
    authStore.user = updated
    appStore.showSuccess(t('common.saved'))
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    savingThreshold.value = false
  }
}

// handleEmailToggle persists the opposite disabled flag for one saved address. The returned
// profile replaces both auth state and the displayed address list after the API accepts the
// change.
async function handleEmailToggle(entry: NotifyEmailEntry) {
  const newDisabled = !entry.disabled
  try {
    const updated = await userAPI.toggleNotifyEmail(entry.email, newDisabled)
    authStore.user = updated
    emailEntries.value = [...updated.balance_notify_extra_emails]
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  }
}

// addPendingEmail trims the new address and opens a verification draft when it is nonempty
// and unique. Duplicate matching is case-insensitive across saved and pending addresses, and
// the template owns the row-count limit.
function addPendingEmail() {
  const email = newEmail.value.trim()
  if (!email) return
  // Saved and pending destinations share a case-insensitive uniqueness check so
  // separate verification drafts cannot introduce another row for the same address.
  const isDuplicate =
    emailEntries.value.some((e) => e.email.toLowerCase() === email.toLowerCase()) ||
    pendingEmails.value.some((p) => p.email.toLowerCase() === email.toLowerCase())
  if (isDuplicate) {
    appStore.showError(t('profile.balanceNotify.emailDuplicate'))
    return
  }
  pendingEmails.value.push({
    email,
    codeSent: false,
    code: '',
    sending: false,
    verifying: false,
    countdown: 0,
    timer: null,
  })
  newEmail.value = ''
}

// sendCodeFor requests verification for a pending row identified by its current index.
// Successful sending starts that row's sixty-second countdown; the template disables
// resending during cooldown and component disposal clears existing row timers.
async function sendCodeFor(idx: number) {
  const pe = pendingEmails.value[idx]
  if (!pe) return
  pe.sending = true
  try {
    await userAPI.sendNotifyEmailCode(pe.email)
    pe.codeSent = true
    pe.countdown = 60
    pe.timer = setInterval(() => {
      pe.countdown--
      if (pe.countdown <= 0 && pe.timer) {
        clearInterval(pe.timer)
        pe.timer = null
      }
    }, 1000)
    appStore.showSuccess(t('profile.balanceNotify.codeSent'))
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    pe.sending = false
  }
}

// verifyPending submits a six-character code for the pending row at the supplied index.
// Successful verification clears its timer and removes the draft before fetching the
// authoritative profile. The server mutation has already completed when that refresh begins,
// so a refresh failure reports an error after the local draft was removed. Callers keep row
// identity stable while the request is pending because removal uses its captured index.
async function verifyPending(idx: number) {
  const pe = pendingEmails.value[idx]
  if (!pe || !pe.code || pe.code.length !== 6) return
  pe.verifying = true
  try {
    await userAPI.verifyNotifyEmail(pe.email, pe.code)
    // Stop the verified row's countdown before removing its draft so the retained row object
    // is no longer updated by its interval.
    if (pe.timer) clearInterval(pe.timer)
    pendingEmails.value.splice(idx, 1)
    appStore.showSuccess(t('profile.balanceNotify.verifySuccess'))
    const updated = await userAPI.getProfile()
    authStore.user = updated
    emailEntries.value = [...updated.balance_notify_extra_emails]
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    pe.verifying = false
  }
}

// handleRemoveEmail removes a persisted address and then reloads the profile. The server
// removal completes before the refresh, so refresh failure may leave the old list visible
// until another profile update arrives.
const handleRemoveEmail = async (email: string) => {
  try {
    await userAPI.removeNotifyEmail(email)
    appStore.showSuccess(t('profile.balanceNotify.removeSuccess'))
    const updated = await userAPI.getProfile()
    authStore.user = updated
    emailEntries.value = [...updated.balance_notify_extra_emails]
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  }
}

// sendCodeForSaved sends a verification code for an existing unverified address and opens
// its shared verification form. It clears prior entered digits and replaces the shared
// countdown interval after a successful send.
async function sendCodeForSaved(email: string) {
  sendingSavedCode.value = true
  try {
    await userAPI.sendNotifyEmailCode(email)
    verifyingEmail.value = email
    verifyCode.value = ''
    verifyCountdown.value = 60
    if (verifyTimer) clearInterval(verifyTimer)
    verifyTimer = setInterval(() => {
      verifyCountdown.value--
      if (verifyCountdown.value <= 0 && verifyTimer) {
        clearInterval(verifyTimer)
        verifyTimer = null
      }
    }, 1000)
    appStore.showSuccess(t('profile.balanceNotify.codeSent'))
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    sendingSavedCode.value = false
  }
}

// verifySavedEmail submits the shared six-character code for the supplied saved address.
// Success clears the form and timer before reloading the profile; a later refresh failure
// reports an error after verification has completed.
async function verifySavedEmail(email: string) {
  if (!verifyCode.value || verifyCode.value.length !== 6) return
  verifyingSaved.value = true
  try {
    await userAPI.verifyNotifyEmail(email, verifyCode.value)
    verifyingEmail.value = ''
    verifyCode.value = ''
    if (verifyTimer) {
      clearInterval(verifyTimer)
      verifyTimer = null
    }
    appStore.showSuccess(t('profile.balanceNotify.verifySuccess'))
    const updated = await userAPI.getProfile()
    authStore.user = updated
    emailEntries.value = [...updated.balance_notify_extra_emails]
  } catch (err: unknown) {
    appStore.showError(extractApiErrorMessage(err, t('common.error')))
  } finally {
    verifyingSaved.value = false
  }
}
</script>
