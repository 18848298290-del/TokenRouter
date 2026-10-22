<template>
  <BaseDialog
    :show="show"
    :title="t('admin.providers.inviteResetTitle')"
    width="wide"
    @close="handleClose"
  >
    <div v-if="provider" class="space-y-5">
      <div
        class="flex flex-col gap-3 rounded-surface border border-gray-200 bg-gray-50 p-4 dark:border-dark-600 dark:bg-dark-800 sm:flex-row sm:items-center sm:justify-between"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-11 w-11 shrink-0 items-center justify-center rounded-control bg-emerald-500 text-white"
          >
            <Icon name="gift" size="md" :stroke-width="2" />
          </div>
          <div class="min-w-0">
            <div class="truncate font-semibold text-gray-900 dark:text-white">
              {{ provider.name }}
            </div>
            <div class="mt-0.5 text-sm text-gray-500 dark:text-gray-400">
              {{ t('admin.providers.inviteResetSubtitle') }}
            </div>
          </div>
        </div>
        <button
          type="button"
          class="btn btn-secondary shrink-0"
          :disabled="loading"
          @click="loadStatus()"
        >
          <Icon name="refresh" size="sm" :class="loading && 'animate-spin'" />
          {{ t('admin.providers.inviteResetRefresh') }}
        </button>
      </div>

      <div
        v-if="loading"
        class="grid gap-4 md:grid-cols-2"
        role="status"
        :aria-label="t('common.loading')"
        aria-busy="true"
        data-loading-skeleton
      >
        <ContentSkeleton
          variant="detail"
          :rows="4"
          class="rounded-control border border-gray-200 p-4 dark:border-dark-600"
        />
        <ContentSkeleton
          variant="list"
          :rows="3"
          class="rounded-control border border-gray-200 p-4 dark:border-dark-600"
        />
      </div>

      <template v-else>
        <div class="grid grid-cols-1 gap-4 md:grid-cols-[minmax(0,1fr)_minmax(0,1.15fr)]">
          <section
            class="space-y-4 rounded-control border border-gray-200 p-4 dark:border-dark-600"
          >
            <div class="flex items-center justify-between gap-3">
              <div>
                <div class="text-sm font-medium text-gray-500 dark:text-gray-400">
                  {{ t('admin.providers.inviteResetAvailable') }}
                </div>
                <div class="mt-2 flex items-end gap-2">
                  <span class="text-4xl font-bold text-gray-900 dark:text-white">{{
                    availableCount
                  }}</span>
                  <span class="pb-1 text-sm text-gray-500 dark:text-gray-400">{{
                    t('admin.providers.inviteResetAvailableUnit')
                  }}</span>
                </div>
              </div>
              <div
                class="flex h-12 w-12 items-center justify-center rounded-control bg-cyan-100 text-cyan-700 dark:bg-cyan-500/15 dark:text-cyan-300"
              >
                <Icon name="refresh" size="md" :stroke-width="2" />
              </div>
            </div>

            <div
              class="grid grid-cols-1 gap-2 rounded-control bg-gray-50 p-3 text-sm dark:bg-dark-800"
            >
              <div class="flex items-center justify-between gap-3">
                <span class="text-gray-500 dark:text-gray-400">{{
                  t('admin.providers.inviteResetRewardType')
                }}</span>
                <span class="text-right font-medium text-gray-900 dark:text-white">{{
                  rewardTypeLabel
                }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span class="text-gray-500 dark:text-gray-400">{{
                  t('admin.providers.inviteResetRewardAmount')
                }}</span>
                <span class="text-right font-medium text-gray-900 dark:text-white">{{
                  rewardAmountLabel
                }}</span>
              </div>
              <div class="flex items-center justify-between gap-3">
                <span class="text-gray-500 dark:text-gray-400">{{
                  t('admin.providers.inviteResetShouldShow')
                }}</span>
                <span :class="['rounded-full px-2 py-0.5 text-xs font-medium', shouldShowClass]">{{
                  shouldShowLabel
                }}</span>
              </div>
              <div v-if="showGrantAction" class="flex items-center justify-between gap-3">
                <span class="text-gray-500 dark:text-gray-400">{{
                  t('admin.providers.inviteResetGrantAction')
                }}</span>
                <span
                  class="max-w-[12rem] truncate text-right font-mono text-xs text-gray-700 dark:text-gray-200"
                  >{{ status?.grant_action }}</span
                >
              </div>
            </div>

            <div v-if="availableCredits.length > 0" class="space-y-3">
              <label class="input-label">{{
                t('admin.providers.inviteResetSelectedCredit')
              }}</label>
              <Select
                v-model="selectedCreditId"
                :options="creditOptions"
                :placeholder="t('admin.providers.inviteResetSelectCredit')"
                :searchable="false"
              />
              <div
                v-if="selectedCredit"
                class="rounded-control bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300"
              >
                <div class="font-medium text-gray-900 dark:text-white">
                  {{ creditTitle(selectedCredit) }}
                </div>
                <div class="mt-1">{{ creditDescription(selectedCredit) }}</div>
                <div
                  v-if="selectedCredit.expires_at"
                  class="mt-2 text-xs text-gray-500 dark:text-gray-400"
                >
                  {{
                    t('admin.providers.inviteResetCreditExpiresAtFull', {
                      time: formatCreditExpiry(selectedCredit.expires_at, 'full'),
                    })
                  }}
                </div>
              </div>
              <div
                v-if="creditExpirations.length > 0"
                class="space-y-2 text-xs text-gray-600 dark:text-gray-300"
              >
                <div class="font-medium text-gray-700 dark:text-gray-200">
                  {{ t('admin.providers.inviteResetCreditExpirations') }}
                </div>
                <div class="flex flex-wrap gap-2">
                  <span
                    v-for="(expiresAt, index) in creditExpirations"
                    :key="`${expiresAt}-${index}`"
                    class="inline-flex max-w-full items-center rounded-compact bg-gray-100 px-2 py-1 tabular-nums dark:bg-dark-700"
                    :title="
                      t('admin.providers.inviteResetCreditExpiresAtFull', {
                        time: formatCreditExpiry(expiresAt, 'full'),
                      })
                    "
                  >
                    {{ formatCreditExpiry(expiresAt, 'short') }}
                  </span>
                </div>
              </div>
            </div>

            <div
              v-else
              class="rounded-control border border-dashed border-gray-300 p-4 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400"
            >
              {{
                t(
                  availableCount > 0
                    ? 'admin.providers.inviteResetCreditDetailsUnavailable'
                    : 'admin.providers.inviteResetNoCredits',
                )
              }}
            </div>

            <button
              type="button"
              class="btn btn-primary w-full"
              :disabled="!canConsume"
              @click="handleConsume"
            >
              <Icon name="refresh" size="sm" :class="consuming && 'animate-spin'" />
              {{
                consuming
                  ? t('admin.providers.inviteResetUsing')
                  : t('admin.providers.inviteResetUseReset')
              }}
            </button>

            <button
              type="button"
              class="flex w-full items-center justify-between rounded-control border border-gray-200 px-3 py-2 text-left text-sm font-medium text-gray-700 hover:bg-gray-50 dark:border-dark-600 dark:text-gray-200 dark:hover:bg-dark-700"
              @click="showRules = !showRules"
            >
              <span>{{ t('admin.providers.inviteResetRules') }}</span>
              <Icon
                name="chevronDown"
                size="sm"
                :class="['transition-transform', showRules && 'rotate-180']"
                :animate-on-hover="false"
              />
            </button>
            <div
              v-if="showRules"
              class="rounded-control bg-gray-50 p-3 text-sm text-gray-600 dark:bg-dark-800 dark:text-gray-300"
            >
              <ul v-if="rules.length > 0" class="list-disc space-y-1 pl-5">
                <li v-for="rule in rules" :key="rule">{{ rule }}</li>
              </ul>
              <p v-else>{{ t('admin.providers.inviteResetRulesEmpty') }}</p>
            </div>
          </section>

          <section
            class="space-y-4 rounded-control border border-gray-200 p-4 dark:border-dark-600"
          >
            <div>
              <label class="input-label" for="codex-invite-reset-emails">
                {{ t('admin.providers.inviteResetInviteEmails') }}
              </label>
              <textarea
                id="codex-invite-reset-emails"
                v-model="emailInput"
                rows="8"
                class="input mt-2 min-h-[180px] resize-y font-mono text-sm leading-6"
                :placeholder="t('admin.providers.inviteResetPlaceholder')"
              ></textarea>
              <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">
                {{ t('admin.providers.inviteResetEmailHint', { max: maxEmails }) }}
              </p>
            </div>

            <label
              class="flex items-start gap-2 rounded-control border border-gray-200 p-3 text-sm text-gray-700 dark:border-dark-600 dark:text-gray-300"
            >
              <input
                v-model="consentConfirmed"
                type="checkbox"
                class="mt-0.5 h-4 w-4 rounded-compact border-gray-300 text-primary-600 focus:ring-primary-500"
              />
              <span>{{ t('admin.providers.inviteResetConsent') }}</span>
            </label>

            <div
              v-if="inviteUnavailableMessage"
              class="rounded-control bg-amber-50 p-3 text-sm text-amber-700 dark:bg-amber-500/10 dark:text-amber-300"
            >
              {{ inviteUnavailableMessage }}
            </div>

            <div v-if="message" :class="['rounded-control p-3 text-sm', messageClass]">
              {{ message }}
            </div>

            <button
              type="button"
              class="btn btn-secondary w-full justify-center"
              :disabled="!canSendInvite"
              @click="handleSendInvite"
            >
              <Icon name="mail" size="sm" :class="sendingInvite && 'animate-pulse'" />
              {{
                sendingInvite
                  ? t('admin.providers.inviteResetSending')
                  : t('admin.providers.inviteResetSendInvite')
              }}
            </button>
          </section>
        </div>
      </template>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { Provider } from '@/api/admin/providers.types'
import type { CodexInviteResetCredit, CodexInviteResetStatus } from '@/api/admin/providers'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

/**
 * CodexInviteResetModal.vue displays OpenAI invitation eligibility and lets administrators send
 * invitations or consume available reset credits. Credit choices prioritize expiry, and a
 * positive available count can still permit server-side automatic selection when individual
 * credit records are absent. Eligibility metadata remains visible regardless of the upstream
 * desktop-display suggestion.
 *
 * Invitation input is deduplicated case-insensitively and validated before sending, with
 * consent required when the server omits that setting. Partial invitation failures retain input
 * for inspection. Credit consumption distinguishes a completed reset from other
 * success-response codes, emits updated only for a reset, and reloads status afterward while
 * retaining the action message. Known invitation failures use interface translations, while
 * unknown failures retain server explanations. Hidden-dialog cleanup clears local input and selection.
 */

/**
 * Invitation validation limits each request to five case-insensitively unique addresses and
 * checks their basic email shape before contacting the endpoint.
 */
const maxEmails = 5
const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/

/**
 * props identifies the provider used for invitation and reset operations, while visibility
 * changes load status or clear local dialog input.
 */
const props = defineProps<{
  show: boolean // parent-controlled invitation dialog visibility
  provider: Provider | null // selected provider or no selection
}>()

/**
 * emit delegates dialog closure and reports completed resets to the parent; invitation sending
 * retains its own inline operation feedback.
 */
const emit = defineEmits<{
  close: [] // request parent dialog closure
  /**
   * Notify the parent after a successful reset so its provider row and usage can be refreshed.
   */
  updated: []
}>()

/**
 * Translation and application toasts describe eligibility, invitation validation, reset
 * outcomes, and request failures within the dialog.
 */
const { t } = useI18n()
const appStore = useAppStore()

/**
 * Request flags, credit selection, consent, invitation text, and action messages belong to this
 * dialog and remain separate from server eligibility observations.
 */
const loading = ref(false)
const sendingInvite = ref(false)
const consuming = ref(false)
const status = ref<CodexInviteResetStatus | null>(null)
const selectedCreditId = ref<string | null>(null)
const emailInput = ref('')
const consentConfirmed = ref(false)
const message = ref('')
const messageType = ref<'success' | 'error' | ''>('')
const showRules = ref(false)

/**
 * availableCredits retains records marked available or with absent status, sorts their expiry,
 * and lets the returned available_count override the derived list count.
 */
const availableCredits = computed(() => {
  return (status.value?.credits ?? [])
    .filter((credit) => {
      const state = credit.status?.toLowerCase()
      return !state || state === 'available'
    })
    .sort((a, b) => {
      // Sort known expiry times first so default selection uses the earliest expiring available
      // credit before less urgent or unknown deadlines.
      const expiryOrder = compareCreditExpiry(a.expires_at ?? '', b.expires_at ?? '')
      return expiryOrder !== 0 ? expiryOrder : a.id.localeCompare(b.id)
    })
})

const availableCount = computed(
  () => status.value?.available_count ?? availableCredits.value.length,
)

/**
 * Eligibility labels preserve unknown grant amounts and desktop display recommendations
 * separately from the ability to inspect upstream qualification information.
 */
const rules = computed(() => status.value?.eligibility_rules ?? [])

const rewardTypeLabel = computed(() => {
  if (status.value?.grant_type === 'none') {
    return t('admin.providers.inviteResetGrantTypeNone')
  }
  if (status.value?.grant_type === 'rate_limit_reset') {
    return t('admin.providers.inviteResetGrantTypeRateLimitReset')
  }
  if (status.value?.grant_type === 'workspace_credits') {
    return t('admin.providers.inviteResetGrantTypeWorkspaceCredits')
  }
  return t('admin.providers.inviteResetGrantTypeUnknown')
})

const rewardAmountLabel = computed(() => {
  const amount = status.value?.grant_amount
  return typeof amount === 'number'
    ? String(amount)
    : t('admin.providers.inviteResetRewardAmountUnknown')
})

const shouldShowLabel = computed(() => {
  if (status.value?.should_show === true) return t('admin.providers.inviteResetShouldShowTrue')
  if (status.value?.should_show === false) return t('admin.providers.inviteResetShouldShowFalse')
  return t('admin.providers.inviteResetShouldShowUnknown')
})

const shouldShowClass = computed(() => {
  if (status.value?.should_show === true) {
    return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300'
  }
  if (status.value?.should_show === false) {
    return 'bg-amber-100 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300'
  }
  return 'bg-gray-200 text-gray-600 dark:bg-dark-600 dark:text-gray-300'
})

const showGrantAction = computed(
  () => Boolean(status.value?.grant_action) && status.value?.grant_type === 'unknown',
)

/**
 * Credit choices include sorted expiry labels; selection and expiration detail derive from the
 * same available-record list to keep their identifiers consistent.
 */
const creditOptions = computed<SelectOption[]>(() => {
  return availableCredits.value.map((credit, index) => ({
    value: credit.id,
    label: credit.expires_at
      ? `${creditTitle(credit)} #${index + 1} · ${formatCreditExpiry(credit.expires_at, 'short')}`
      : `${creditTitle(credit)} #${index + 1}`,
  }))
})

const selectedCredit = computed(() => {
  return availableCredits.value.find((credit) => credit.id === selectedCreditId.value) ?? null
})

const creditExpirations = computed(() => {
  return availableCredits.value
    .map((credit) => credit.expires_at?.trim() ?? '')
    .filter((expiresAt) => expiresAt.length > 0)
    .sort(compareCreditExpiry)
})

/**
 * canConsume requires idle status loading and consumption plus a positive available count,
 * allowing server selection when detailed credit records are absent.
 */
const canConsume = computed(() => {
  if (loading.value || consuming.value || availableCount.value <= 0) return false
  // A positive count can outlive detailed credit availability; omitting credit_id lets the
  // consume endpoint select an available credit automatically.
  return availableCredits.value.length === 0 || Boolean(selectedCreditId.value)
})

/**
 * localizedInviteUnavailableMessage resolves the known invitation failure in the active
 * interface language. Unknown reasons return undefined so status and action callers can
 * retain server details or choose their own translated default.
 */
const localizedInviteUnavailableMessage = (reason: unknown): string | undefined => {
  if (reason === 'CODEX_INVITE_RESET_REFERRAL_UNAVAILABLE') {
    return t('admin.providers.inviteResetInviteUnavailable')
  }
  return undefined
}

/**
 * inviteUnavailableMessage translates the known referral-unavailable reason in the active
 * interface language. Unknown reasons retain the server explanation, with a translated
 * default when the server supplies no message.
 */
const inviteUnavailableMessage = computed(() => {
  if (!status.value || status.value.invite_available !== false) return ''
  return (
    localizedInviteUnavailableMessage(status.value.invite_unavailable_reason) ||
    status.value.invite_unavailable_message ||
    t('admin.providers.inviteResetInviteUnavailable')
  )
})

// canSendInvite requires loaded status, an available invitation service, and no
// active status load or invitation request before enabling the send action.
const canSendInvite = computed(() => {
  return (
    Boolean(status.value) &&
    !loading.value &&
    !sendingInvite.value &&
    status.value?.invite_available !== false
  )
})

/**
 * messageClass maps the current action result to success, error, or neutral presentation while
 * keeping its text independent from request flags.
 */
const messageClass = computed(() => {
  if (messageType.value === 'success') {
    return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
  }
  if (messageType.value === 'error') {
    return 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300'
  }
  return 'bg-gray-50 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
})

/**
 * creditTitle returns the upstream credit title or a localized default. Empty titles use the
 * default so every credit option and detail section has a visible heading.
 */
const creditTitle = (credit: CodexInviteResetCredit) => {
  return credit.title || t('admin.providers.inviteResetCreditFallbackTitle')
}

/**
 * creditDescription returns the upstream credit description or a localized default. Missing
 * explanations use the default so the selected reset opportunity still has descriptive text in
 * its detail view.
 */
const creditDescription = (credit: CodexInviteResetCredit) => {
  return credit.description || t('admin.providers.inviteResetCreditFallbackDescription')
}

/**
 * getCreditExpiryTime converts valid expiration text to epoch milliseconds. Invalid text sorts
 * as positive infinity so known deadlines precede unknown expiration representations in the
 * credit selection list.
 */
const getCreditExpiryTime = (value: string): number => {
  const time = new Date(value).getTime()
  return Number.isNaN(time) ? Number.POSITIVE_INFINITY : time
}

/**
 * compareCreditExpiry orders valid deadlines chronologically and uses text when their numeric
 * values match. Invalid values share positive infinity, so their lexical order remains
 * deterministic among unknown deadlines.
 */
const compareCreditExpiry = (a: string, b: string): number => {
  const left = getCreditExpiryTime(a)
  const right = getCreditExpiryTime(b)
  if (left !== right) return left - right
  return a.localeCompare(b)
}

/**
 * formatCreditExpiry formats valid deadlines in the browser locale and includes the year in
 * full mode. Invalid strings pass through unchanged for inspection in option labels and
 * expiration details.
 */
const formatCreditExpiry = (value: string, style: 'short' | 'full'): string => {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value

  const options: Intl.DateTimeFormatOptions = {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }
  if (style === 'full') {
    options.year = 'numeric'
  }
  return new Intl.DateTimeFormat(undefined, options).format(date)
}

/**
 * parseEmails splits invitation input on commas, semicolons, or whitespace and deduplicates
 * case-insensitively. Empty input, more than five unique addresses, or a malformed address
 * throws a localized validation error.
 */
const parseEmails = () => {
  const emails = emailInput.value
    .split(/[,\s;]+/)
    .map((item) => item.trim())
    .filter(Boolean)
  const unique = [...new Map(emails.map((email) => [email.toLowerCase(), email])).values()]
  if (unique.length === 0) {
    throw new Error(t('admin.providers.inviteResetEmailsRequired'))
  }
  if (unique.length > maxEmails) {
    throw new Error(t('admin.providers.inviteResetEmailLimit', { max: maxEmails }))
  }
  const invalid = unique.find((email) => !emailPattern.test(email))
  if (invalid) {
    throw new Error(t('admin.providers.inviteResetInvalidEmail', { email: invalid }))
  }
  return unique
}

/**
 * setMessage updates the visible action message and its success or error tone.
 */
const setMessage = (type: 'success' | 'error', text: string) => {
  messageType.value = type
  message.value = text
}

/**
 * selectDefaultCredit retains a still-available selected credit or selects the first sorted
 * option. If no credit details remain, selection becomes null so the consume endpoint can use
 * automatic selection.
 */
const selectDefaultCredit = () => {
  if (!availableCredits.value.length) {
    selectedCreditId.value = null
    return
  }
  if (!availableCredits.value.some((credit) => credit.id === selectedCreditId.value)) {
    selectedCreditId.value = availableCredits.value[0].id
  }
}

/**
 * loadStatus refreshes invitation and credit observations, optionally clearing the prior action
 * message. Success reconciles credit selection; failure clears status and reports both inline
 * and toast feedback for the request, translating known invitation failures by reason.
 */
const loadStatus = async (clearMessage = true) => {
  if (!props.provider) return
  loading.value = true
  if (clearMessage) {
    message.value = ''
    messageType.value = ''
  }
  try {
    status.value = await adminAPI.providers.getCodexInviteResetStatus(props.provider.id)
    selectDefaultCredit()
  } catch (error: any) {
    status.value = null
    const text =
      localizedInviteUnavailableMessage(error?.reason) ||
      error?.message ||
      t('admin.providers.inviteResetLoadFailed')
    setMessage('error', text)
    appStore.showError(text)
  } finally {
    loading.value = false
  }
}

/**
 * handleSendInvite validates availability, email input, and required consent before sending
 * invitations. Partial failures retain the input and show failed addresses; full success clears
 * email text and reports completion. Known invitation errors use the current interface language
 * when availability changes between the status read and invitation submission.
 */
const handleSendInvite = async () => {
  if (!props.provider || sendingInvite.value) return
  try {
    if (!status.value) {
      throw new Error(t('admin.providers.inviteResetLoadFailed'))
    }
    if (inviteUnavailableMessage.value) {
      setMessage('error', inviteUnavailableMessage.value)
      return
    }
    const emails = parseEmails()
    if ((status.value?.requires_consent ?? true) && !consentConfirmed.value) {
      throw new Error(t('admin.providers.inviteResetConsentRequired'))
    }
    sendingInvite.value = true
    const result = await adminAPI.providers.sendCodexInviteResetInvite(props.provider.id, emails)
    const failed = result.failed_emails?.filter(Boolean) ?? []
    if (failed.length > 0) {
      setMessage(
        'error',
        t('admin.providers.inviteResetInvitePartialFailed', { emails: failed.join(', ') }),
      )
      return
    }
    emailInput.value = ''
    setMessage('success', result.message || t('admin.providers.inviteResetInviteSuccess'))
    appStore.showSuccess(t('admin.providers.inviteResetInviteSuccess'))
  } catch (error: any) {
    setMessage(
      'error',
      localizedInviteUnavailableMessage(error?.reason) ||
        error?.message ||
        t('admin.providers.inviteResetInviteFailed'),
    )
  } finally {
    sendingInvite.value = false
  }
}

/**
 * consumeSuccessMessage translates known reset outcome codes and otherwise returns the generic
 * consume-success text. The caller separately chooses message tone and whether a reset warrants
 * notifying the parent.
 */
const consumeSuccessMessage = (code?: string) => {
  if (code === 'nothing_to_reset') return t('admin.providers.inviteResetNothingToReset')
  if (code === 'already_redeemed') return t('admin.providers.inviteResetAlreadyRedeemed')
  if (code === 'no_credit') return t('admin.providers.inviteResetNoCredit')
  return t('admin.providers.inviteResetConsumeSuccess')
}

/**
 * handleConsume submits the selected credit or lets the server choose when no identifier is
 * available. A reset emits updated, while other outcome codes remain inline feedback; status
 * reload preserves that message. Known invitation failures are translated by reason, and other
 * failures retain the server explanation or use the translated consume default.
 */
const handleConsume = async () => {
  if (!props.provider || !canConsume.value) return
  consuming.value = true
  try {
    const result = await adminAPI.providers.consumeCodexInviteReset(
      props.provider.id,
      selectedCreditId.value ?? undefined,
    )
    const text = consumeSuccessMessage(result.code)
    setMessage(result.code === 'reset' || !result.code ? 'success' : 'error', text)
    if (result.code === 'reset' || !result.code) {
      appStore.showSuccess(text)
      emit('updated')
    }
    // Reload credit availability after consumption while keeping the outcome message, including
    // server responses that report nothing was reset.
    await loadStatus(false)
  } catch (error: any) {
    setMessage(
      'error',
      localizedInviteUnavailableMessage(error?.reason) ||
        error?.message ||
        t('admin.providers.inviteResetConsumeFailed'),
    )
  } finally {
    consuming.value = false
  }
}

/**
 * resetLocalState clears status, selected credit, invitation input, consent, and visible
 * messages.
 */
const resetLocalState = () => {
  status.value = null
  selectedCreditId.value = null
  emailInput.value = ''
  consentConfirmed.value = false
  message.value = ''
  messageType.value = ''
  showRules.value = false
}

/**
 * handleClose requests parent-controlled dismissal of the invitation and reset dialog.
 */
const handleClose = () => {
  emit('close')
}

watch(
  () => [props.show, props.provider?.id],
  ([visible]) => {
    if (visible && props.provider) {
      loadStatus()
      return
    }
    resetLocalState()
  },
)

watch(availableCredits, selectDefaultCredit)
</script>
