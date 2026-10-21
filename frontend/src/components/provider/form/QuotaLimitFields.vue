<template>
  <SettingsSection :title="t('admin.providers.quotaControl.title')" :hint="hint">
    <QuotaLimitCard
      :total-limit="limits.totalLimit"
      :daily-limit="limits.dailyLimit"
      :weekly-limit="limits.weeklyLimit"
      :monthly-limit="limits.monthlyLimit"
      :daily-reset-mode="limits.dailyResetMode"
      :daily-reset-hour="limits.dailyResetHour"
      :weekly-reset-mode="limits.weeklyResetMode"
      :monthly-reset-mode="limits.monthlyResetMode"
      :weekly-reset-day="limits.weeklyResetDay"
      :monthly-reset-day="limits.monthlyResetDay"
      :weekly-reset-hour="limits.weeklyResetHour"
      :monthly-reset-hour="limits.monthlyResetHour"
      :reset-timezone="limits.resetTimezone"
      :quota-notify-global-enabled="notifyGlobalEnabled"
      :quota-notify-daily-enabled="notify.daily.enabled"
      :quota-notify-daily-threshold="notify.daily.threshold"
      :quota-notify-daily-threshold-type="notify.daily.thresholdType"
      :quota-notify-weekly-enabled="notify.weekly.enabled"
      :quota-notify-monthly-enabled="notify.monthly.enabled"
      :quota-notify-weekly-threshold="notify.weekly.threshold"
      :quota-notify-monthly-threshold="notify.monthly.threshold"
      :quota-notify-weekly-threshold-type="notify.weekly.thresholdType"
      :quota-notify-monthly-threshold-type="notify.monthly.thresholdType"
      :quota-notify-total-enabled="notify.total.enabled"
      :quota-notify-total-threshold="notify.total.threshold"
      :quota-notify-total-threshold-type="notify.total.thresholdType"
      @update:total-limit="emit('update:limit', 'totalLimit', $event)"
      @update:daily-limit="emit('update:limit', 'dailyLimit', $event)"
      @update:weekly-limit="emit('update:limit', 'weeklyLimit', $event)"
      @update:monthly-limit="emit('update:limit', 'monthlyLimit', $event)"
      @update:daily-reset-mode="emit('update:limit', 'dailyResetMode', $event)"
      @update:daily-reset-hour="emit('update:limit', 'dailyResetHour', $event)"
      @update:weekly-reset-mode="emit('update:limit', 'weeklyResetMode', $event)"
      @update:monthly-reset-mode="emit('update:limit', 'monthlyResetMode', $event)"
      @update:weekly-reset-day="emit('update:limit', 'weeklyResetDay', $event)"
      @update:monthly-reset-day="emit('update:limit', 'monthlyResetDay', $event)"
      @update:weekly-reset-hour="emit('update:limit', 'weeklyResetHour', $event)"
      @update:monthly-reset-hour="emit('update:limit', 'monthlyResetHour', $event)"
      @update:reset-timezone="emit('update:limit', 'resetTimezone', $event)"
      @update:quota-notify-daily-enabled="emit('update:notify', 'daily', 'enabled', $event)"
      @update:quota-notify-daily-threshold="emit('update:notify', 'daily', 'threshold', $event)"
      @update:quota-notify-daily-threshold-type="
        emit('update:notify', 'daily', 'thresholdType', $event)
      "
      @update:quota-notify-weekly-enabled="emit('update:notify', 'weekly', 'enabled', $event)"
      @update:quota-notify-monthly-enabled="emit('update:notify', 'monthly', 'enabled', $event)"
      @update:quota-notify-weekly-threshold="emit('update:notify', 'weekly', 'threshold', $event)"
      @update:quota-notify-monthly-threshold="emit('update:notify', 'monthly', 'threshold', $event)"
      @update:quota-notify-weekly-threshold-type="
        emit('update:notify', 'weekly', 'thresholdType', $event)
      "
      @update:quota-notify-monthly-threshold-type="
        emit('update:notify', 'monthly', 'thresholdType', $event)
      "
      @update:quota-notify-total-enabled="emit('update:notify', 'total', 'enabled', $event)"
      @update:quota-notify-total-threshold="emit('update:notify', 'total', 'threshold', $event)"
      @update:quota-notify-total-threshold-type="
        emit('update:notify', 'total', 'thresholdType', $event)
      "
    />
  </SettingsSection>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import SettingsSection from '@/components/common/settings/SettingsSection.vue'
import type { QuotaNotifyDim, QuotaNotifyDimState } from '@/composables/useQuotaNotifyState'
import QuotaLimitCard from '../QuotaLimitCard.vue'
import type { QuotaLimitValues } from './quotaLimit'

// QuotaLimitFields.vue connects shared quota controls to provider form drafts and
// notification settings. Create and edit dialogs supply the same value structures, and
// keyed update events return individual changes to their owners before the dialog
// constructs its API payload.
defineProps<{
  limits: QuotaLimitValues // Parent-owned quota limit drafts.
  notify: Record<QuotaNotifyDim, QuotaNotifyDimState> // Parent-owned dimension notification drafts.
  notifyGlobalEnabled: boolean // Global notification feature switch.
  hint?: string // Supplemental field help text.
}>()
// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:limit': [key: keyof QuotaLimitValues, value: QuotaLimitValues[keyof QuotaLimitValues]] // Replacement limit draft value.
  'update:notify': [
    dim: QuotaNotifyDim,
    field: keyof QuotaNotifyDimState,
    value: QuotaNotifyDimState[keyof QuotaNotifyDimState],
  ] // Replacement notify draft value.
}>()

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
</script>
