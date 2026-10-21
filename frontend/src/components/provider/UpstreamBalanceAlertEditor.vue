<template>
  <SettingsSection data-testid="upstream-balance-alert">
    <SettingToggleRow
      :id="`${uid}-enabled`"
      :model-value="active"
      :disabled="!supported"
      :label="t('admin.providers.balanceAlert.title')"
      :hint="
        supported
          ? t('admin.providers.balanceAlert.hint')
          : t('admin.providers.balanceAlert.unsupported')
      "
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
            <label :for="`${uid}-threshold`" class="input-label">{{
              t('admin.providers.balanceAlert.threshold')
            }}</label>
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
            <label :for="`${uid}-interval`" class="input-label">{{
              t('admin.providers.balanceAlert.interval')
            }}</label>
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
              <span class="input-icon-right text-sm text-gray-500 dark:text-gray-400">{{
                t('common.minutes')
              }}</span>
            </div>
            <p class="input-hint">{{ t('admin.providers.balanceAlert.intervalHint') }}</p>
          </div>
        </div>

        <div>
          <label :for="`${uid}-email`" class="input-label">{{
            t('admin.providers.balanceAlert.email')
          }}</label>
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
              <span class="text-gray-500 dark:text-gray-400">{{
                t('admin.providers.balanceAlert.checkedAt')
              }}</span>
              <span class="break-words text-gray-900 dark:text-white">{{
                formatTime(status.last_checked_at)
              }}</span>
              <span class="text-gray-500 dark:text-gray-400">{{
                t('admin.providers.balanceAlert.balance')
              }}</span>
              <span class="break-words tabular-nums text-gray-900 dark:text-white">{{
                balanceLabel
              }}</span>
              <span class="text-gray-500 dark:text-gray-400">{{
                t('admin.providers.balanceAlert.sentAt')
              }}</span>
              <span class="break-words text-gray-900 dark:text-white">
                {{
                  status.sent_at
                    ? formatTime(status.sent_at)
                    : t('admin.providers.balanceAlert.notSent')
                }}
              </span>
              <span class="text-gray-500 dark:text-gray-400">{{
                t('admin.providers.balanceAlert.nextCheckAt')
              }}</span>
              <span class="break-words text-gray-900 dark:text-white">{{
                formatTime(status.next_check_at)
              }}</span>
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
import type {
  UpstreamBalanceAlertConfig,
  UpstreamBalanceAlertStatus,
} from '@/api/admin/providers.types'
import { formatDateTime } from '@/utils/format'
import {
  BALANCE_ALERT_MAX_INTERVAL,
  BALANCE_ALERT_MIN_INTERVAL,
} from '@/utils/upstreamBalanceAlert'

// UpstreamBalanceAlertEditor.vue edits one API-key provider balance reminder and displays
// the latest saved check status. The parent owns the editable policy and persists it with
// Extra, while this component owns status reads and their loading or failure state.
// Unsupported providers display the policy as inactive.
//
// Each read captures a sequence number and can publish only while that number remains
// current. Provider changes, disabling the displayed reminder, and unmount all invalidate
// pending reads. The status endpoint returns existing observations; policy editing and
// status refresh remain separate from upstream querying and email delivery.

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  // Whether the query adapter reports a wallet balance; unsupported policies display as
  // inactive.
  supported: boolean
  queryEnabled: boolean // Upstream usage query switch.
  // providerId identifies the saved reminder status to read; changes invalidate
  // pending status reads, while omission during creation skips the status request.
  providerId?: number
}>()
// model holds the parent-owned editable value and sends replacement values through the
// component model event.
const model = defineModel<UpstreamBalanceAlertConfig>({ required: true })
// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()

// REMINDER_ERROR_CODES separates reminder-owned failures from usage-query failures when
// choosing the namespace for the status error label.
const REMINDER_ERROR_CODES = new Set([
  'UPSTREAM_BALANCE_ALERT_HOST_DENIED',
  'UPSTREAM_BALANCE_ALERT_EMAIL_FAILED',
])

// status holds the latest accepted saved check observation and is cleared whenever
// provider or active-policy identity changes.
const status = ref<UpstreamBalanceAlertStatus | null>(null)
// loading belongs to the current status-read sequence so an older response cannot clear
// the indicator for a newer read.
const loading = ref(false)
// loadFailed marks the current status read as failed while keeping the editable policy
// available for further changes.
const loadFailed = ref(false)
// loadSequence advances before reads and lifecycle invalidation so responses publish only
// into the provider display they still belong to.
let loadSequence = 0

// active displays the policy as enabled only when its model flag and the provider balance
// capability both permit it.
const active = computed(() => model.value.enabled && props.supported)

// threshold proxies the balance threshold through update so edits replace the whole
// parent-owned policy object with the changed field.
const threshold = computed({
  get: () => model.value.threshold,
  set: (value: number) => update({ threshold: value }),
})

// interval proxies the check interval in minutes through update so the parent receives a
// replacement policy object.
const interval = computed({
  get: () => model.value.interval_minutes,
  set: (value: number) => update({ interval_minutes: value }),
})

// recipientEmail proxies the destination address through update while retaining the other
// fields of the parent-owned reminder policy.
const recipientEmail = computed({
  get: () => model.value.recipient_email,
  set: (value: string) => update({ recipient_email: value }),
})

// balanceLabel formats a known balance with up to four decimals and its observed unit;
// missing balances show a dash.
const balanceLabel = computed(() => {
  const balance = status.value?.balance
  if (balance == null) return '—'
  const amount = new Intl.NumberFormat(undefined, { maximumFractionDigits: 4 }).format(balance)
  return status.value?.unit ? `${amount} ${status.value.unit}` : amount
})

// update replaces the policy model with the current fields and supplied patch.
// Unmentioned fields remain present, while the parent receives the replacement object
// through the model update event.
function update(patch: Partial<UpstreamBalanceAlertConfig>) {
  model.value = { ...model.value, ...patch }
}

// formatTime formats a status timestamp or returns a dash when formatting yields no text.
// Timestamp parsing and invalid-value behavior are delegated to the shared formatDateTime
// helper used elsewhere.
function formatTime(value?: string): string {
  return formatDateTime(value) || '—'
}

// errorLabel returns localized reminder or usage-query text for a backend error code.
// Reminder-owned codes choose their namespace, and unknown translations fall back to the
// exact code for diagnosis.
function errorLabel(code: string): string {
  const group = REMINDER_ERROR_CODES.has(code) ? 'balanceAlert' : 'upstreamUsage'
  return t(`admin.providers.${group}.errors.${code}`, code)
}

// loadStatus loads the saved provider reminder status into this component. Missing
// provider IDs or unsupported providers clear the display and send no request. Each
// invocation advances a sequence so later reads and lifecycle invalidation decide which
// result may publish.
//
// Failures mark the current status area as failed while leaving the editable policy
// untouched. The finally branch clears loading only for the current sequence, preventing
// an older request from hiding a newer request indicator.
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

    // A later read or lifecycle invalidation owns the current display, so this completion
    // must leave status and failure state untouched.
    if (sequence !== loadSequence) return
    status.value = result
  } catch {
    // A later read or lifecycle invalidation owns the current display, so this completion
    // must leave status and failure state untouched.
    if (sequence !== loadSequence) return

    loadFailed.value = true
  } finally {
    // Only the current read releases the loading indicator, preventing an older completion
    // from making a newer in-flight request look finished.
    if (sequence === loadSequence) loading.value = false
  }
}

// Provider and active-policy changes invalidate pending requests even when the next state
// is disabled and starts no replacement read.
watch(
  () => [props.providerId, active.value] as const,
  () => {
    loadSequence++
    status.value = null
    loading.value = false
    loadFailed.value = false
    if (active.value) void loadStatus()
  },
  { immediate: true },
)

// Advance the sequence when the component unmounts so pending success or failure responses
// cannot update its discarded status display.
onBeforeUnmount(() => {
  loadSequence++
})
</script>
