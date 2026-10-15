<template>
  <SettingsSection data-testid="upstream-balance-alert">
    <SettingToggleRow
      :id="`${uid}-enabled`"
      :model-value="active"
      :disabled="!supported"
      :label="t('admin.providers.balanceAlert.title')"
      :hint="supported ? t('admin.providers.balanceAlert.hint') : t('admin.providers.balanceAlert.unsupported')"
      testid="balance-alert-enabled"
      @update:model-value="update({ enabled: $event })"
    />

    <Collapse :open="active" unmount-on-hide>
      <SettingsSubpanel>
        <SettingsNotice v-if="!queryEnabled" tone="warning">
          {{ t('admin.providers.balanceAlert.queryDisabled') }}
        </SettingsNotice>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div>
            <label :for="`${uid}-threshold`" class="input-label">{{ t('admin.providers.balanceAlert.threshold') }}</label>
            <input
              :id="`${uid}-threshold`"
              v-model.number="threshold"
              type="number"
              min="0"
              step="any"
              required
              class="input"
              data-testid="balance-alert-threshold"
            />
            <p class="input-hint">{{ t('admin.providers.balanceAlert.thresholdHint') }}</p>
          </div>
          <div>
            <label :for="`${uid}-interval`" class="input-label">{{ t('admin.providers.balanceAlert.interval') }}</label>
            <div class="input-icon-wrap">
              <input
                :id="`${uid}-interval`"
                v-model.number="interval"
                type="number"
                :min="BALANCE_ALERT_MIN_INTERVAL"
                :max="BALANCE_ALERT_MAX_INTERVAL"
                step="1"
                required
                class="input pr-16"
                data-testid="balance-alert-interval"
              />
              <span class="input-icon-right text-sm text-gray-500 dark:text-gray-400">{{ t('common.minutes') }}</span>
            </div>
            <p class="input-hint">{{ t('admin.providers.balanceAlert.intervalHint') }}</p>
          </div>
        </div>

        <div>
          <label :for="`${uid}-email`" class="input-label">{{ t('admin.providers.balanceAlert.email') }}</label>
          <input
            :id="`${uid}-email`"
            v-model="recipientEmail"
            type="email"
            required
            autocomplete="email"
            class="input"
            placeholder="ops@example.com"
            data-testid="balance-alert-email"
          />
        </div>

        <div
          v-if="providerId"
          class="border-t border-gray-200 pt-4 dark:border-dark-600"
          data-testid="balance-alert-status"
        >
          <div class="mb-2 flex items-center justify-between gap-2">
            <span class="text-xs font-medium text-gray-700 dark:text-dark-200">
              {{ t('admin.providers.balanceAlert.status') }}
            </span>
            <button
              type="button"
              class="btn-icon-sm text-gray-500 hover:bg-gray-100 hover:text-gray-700 disabled:cursor-not-allowed disabled:opacity-40 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-dark-100"
              :disabled="loading"
              :aria-label="t('admin.providers.balanceAlert.refresh')"
              :title="t('admin.providers.balanceAlert.refresh')"
              data-testid="balance-alert-refresh"
              @click="loadStatus"
            >
              <Icon name="refresh" size="sm" :class="{ 'animate-spin': loading }" />
            </button>
          </div>

          <ContentSkeleton v-if="loading && !status" variant="detail" :rows="2" />
          <SettingsNotice v-else-if="loadFailed" tone="error">
            {{ t('admin.providers.balanceAlert.loadFailed') }}
          </SettingsNotice>
          <p v-else-if="!status?.round" class="input-hint">
            {{ t('admin.providers.balanceAlert.notChecked') }}
          </p>
          <template v-else>
            <div class="grid grid-cols-[minmax(4rem,auto)_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-xs">
              <span class="text-gray-500 dark:text-gray-400">{{ t('admin.providers.balanceAlert.checkedAt') }}</span>
              <span class="break-words text-gray-900 dark:text-white">{{ formatTime(status.last_checked_at) }}</span>
              <span class="text-gray-500 dark:text-gray-400">{{ t('admin.providers.balanceAlert.balance') }}</span>
              <span class="break-words tabular-nums text-gray-900 dark:text-white">{{ balanceLabel }}</span>
              <span class="text-gray-500 dark:text-gray-400">{{ t('admin.providers.balanceAlert.sentAt') }}</span>
              <span class="break-words text-gray-900 dark:text-white">
                {{ status.sent_at ? formatTime(status.sent_at) : t('admin.providers.balanceAlert.notSent') }}
              </span>
              <span class="text-gray-500 dark:text-gray-400">{{ t('admin.providers.balanceAlert.nextCheckAt') }}</span>
              <span class="break-words text-gray-900 dark:text-white">{{ formatTime(status.next_check_at) }}</span>
            </div>
            <p
              v-if="status.error_code"
              class="mt-2 break-words border-t border-gray-100 pt-2 text-xs text-amber-700 dark:border-dark-700 dark:text-amber-300"
              data-testid="balance-alert-error"
            >
              {{ errorLabel(status.error_code) }}
            </p>
          </template>
        </div>
      </SettingsSubpanel>
    </Collapse>
  </SettingsSection>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUpstreamBalanceAlertStatus } from '@/api/admin/providers'
import Collapse from '@/components/common/Collapse.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import SettingsNotice from '@/components/common/settings/SettingsNotice.vue'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import Icon from '@/components/icons/Icon.vue'
import type { UpstreamBalanceAlertConfig, UpstreamBalanceAlertStatus } from '@/types'
import { formatDateTime } from '@/utils/format'
import {
  BALANCE_ALERT_MAX_INTERVAL,
  BALANCE_ALERT_MIN_INTERVAL
} from '@/utils/upstreamBalanceAlert'

// UpstreamBalanceAlertEditor.vue edits one API-key provider's balance reminder
// and shows the latest scheduled check for a saved provider. The parent form owns
// the policy object and sends it with Extra on save; this component replaces that
// object field by field and keeps the read-only check status to itself.
//
// Status reads go through an endpoint that neither queries the upstream nor sends
// mail. The edit dialog can switch to another provider while a read is pending,
// so each read takes a sequence number and only the latest one may update the
// display. Unmounting advances the sequence as well, which discards a response
// that arrives after the dialog closes.

const props = defineProps<{
  supported: boolean // query adapter reports a wallet balance
  queryEnabled: boolean // upstream usage query switch
  providerId?: number // saved provider; omitted while creating
}>()
const model = defineModel<UpstreamBalanceAlertConfig>({ required: true })
const { t } = useI18n()
const uid = useId()

// Backend reasons owned by the reminder; other codes come from the shared usage query.
const REMINDER_ERROR_CODES = new Set(['UPSTREAM_BALANCE_ALERT_HOST_DENIED', 'UPSTREAM_BALANCE_ALERT_EMAIL_FAILED'])

// status, loading, and loadFailed describe the latest status read. Only the read
// whose number still equals loadSequence may change them.
const status = ref<UpstreamBalanceAlertStatus | null>(null)
const loading = ref(false)
const loadFailed = ref(false)
let loadSequence = 0

// An enabled policy stays saved for unsupported providers, but the switch shows off.
const active = computed(() => model.value.enabled && props.supported)

// Field models for the inputs; each write replaces the whole policy through update.
const threshold = computed({
  get: () => model.value.threshold,
  set: (value: number) => update({ threshold: value })
})

const interval = computed({
  get: () => model.value.interval_minutes,
  set: (value: number) => update({ interval_minutes: value })
})

const recipientEmail = computed({
  get: () => model.value.recipient_email,
  set: (value: string) => update({ recipient_email: value })
})

// balanceLabel shows the observed balance with up to four decimals and its upstream unit.
const balanceLabel = computed(() => {
  const balance = status.value?.balance
  if (balance == null) return '—'
  const amount = new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(balance)
  return status.value?.unit ? `${amount} ${status.value.unit}` : amount
})

/** update emits a new policy object with the given fields replaced. */
function update(patch: Partial<UpstreamBalanceAlertConfig>) {
  model.value = { ...model.value, ...patch }
}

/** formatTime formats a status timestamp, or returns a dash when it is absent. */
function formatTime(value?: string): string {
  return formatDateTime(value) || '—'
}

/** errorLabel returns the localized text for a backend error code, or the code itself. */
function errorLabel(code: string): string {
  const group = REMINDER_ERROR_CODES.has(code) ? 'balanceAlert' : 'upstreamUsage'
  return t(`admin.providers.${group}.errors.${code}`, code)
}

/** loadStatus reads the saved provider's latest check into the status area.
 * Calls without a saved, supported provider clear the display and send nothing.
 * A failed read shows an error in the status area and leaves the editable fields
 * untouched; the refresh button retries it.
 */
async function loadStatus() {
  const sequence = ++loadSequence
  loadFailed.value = false
  if (!props.providerId || !props.supported) {
    status.value = null
    loading.value = false
    return
  }
  loading.value = true
  try {
    const result = await getUpstreamBalanceAlertStatus(props.providerId)
    // A newer read started after the provider changed or the panel closed;
    // its result replaces this one, so this response is dropped.
    if (sequence !== loadSequence) return
    status.value = result
  } catch {
    if (sequence !== loadSequence) return
    // The status area shows the error in place of the earlier result; the
    // reminder settings being edited stay as they are.
    loadFailed.value = true
  } finally {
    if (sequence === loadSequence) loading.value = false
  }
}

// Read when the panel opens and whenever the dialog switches provider; status
// for the previous provider is cleared first so it never shows under the new one.
watch(
  () => [props.providerId, active.value] as const,
  ([providerId, open], previous) => {
    if (previous && previous[0] !== providerId) status.value = null
    if (open) void loadStatus()
  },
  { immediate: true }
)

onBeforeUnmount(() => {
  // Invalidate pending reads so their responses do not update an unmounted component.
  loadSequence++
})
</script>
