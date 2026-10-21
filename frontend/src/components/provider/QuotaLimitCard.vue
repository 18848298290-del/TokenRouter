<script setup lang="ts">
import Collapse from '@/components/common/Collapse.vue'

import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import { ref, watch, computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import QuotaDimensionRow from './QuotaDimensionRow.vue'
import type { QuotaThresholdType, QuotaResetMode } from '@/constants/provider'

// QuotaLimitCard.vue combines total and periodic spending ceilings with per-dimension
// notification settings. A shared timezone controls fixed schedules, while each daily,
// weekly, or monthly limit retains its own reset mode and local hour. Positive amounts
// determine the externally restored enabled state.
//
// A local switch permits enabling the editor before any amount is entered. Disabling it
// emits null for all limit and schedule drafts in a single watcher callback; notification
// drafts remain owned by the parent. Fixed-mode hints display default day, hour, and
// timezone values when their corresponding drafts are absent.

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// uid supplies one stable instance ID so labels target their own input when several
// editors are mounted.
const uid = useId()

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = withDefaults(
  defineProps<{
    // The amount and reset drafts remain nullable until configured. Positive amounts
    // enable limits, while fixed reset modes use their local hour, weekday or date, and
    // the shared resetTimezone supplied by the parent.
    totalLimit: number | null // Total spending ceiling in USD.
    dailyLimit: number | null // Daily spending ceiling in USD.
    weeklyLimit: number | null // Weekly spending ceiling in USD.
    monthlyLimit: number | null // Monthly spending ceiling in USD.
    dailyResetMode: QuotaResetMode | null // Daily reset policy draft.
    dailyResetHour: number | null // Daily local reset clock hour.
    weeklyResetMode: QuotaResetMode | null // Weekly reset policy draft.
    monthlyResetMode: QuotaResetMode | null // Monthly reset policy draft.
    weeklyResetDay: number | null // Weekly reset weekday.
    monthlyResetDay: number | null // Monthly reset calendar date.
    weeklyResetHour: number | null // Weekly local reset clock hour.
    monthlyResetHour: number | null // Monthly local reset clock hour.
    // IANA timezone for fixed schedules; the row initializes UTC when entering fixed mode
    // with an empty draft.
    resetTimezone: string | null
    quotaNotifyGlobalEnabled?: boolean // Global notification feature switch.
    quotaNotifyDailyEnabled?: boolean | null // Daily nullable alert switch.
    quotaNotifyDailyThreshold?: number | null // Daily notification threshold amount.
    quotaNotifyDailyThresholdType?: QuotaThresholdType | null // Daily fixed amount or percentage.
    quotaNotifyWeeklyEnabled?: boolean | null // Weekly nullable alert switch.
    quotaNotifyMonthlyEnabled?: boolean | null // Monthly nullable alert switch.
    quotaNotifyWeeklyThreshold?: number | null // Weekly notification threshold amount.
    quotaNotifyMonthlyThreshold?: number | null // Monthly notification threshold amount.
    quotaNotifyWeeklyThresholdType?: QuotaThresholdType | null // Weekly fixed amount or percentage.
    quotaNotifyMonthlyThresholdType?: QuotaThresholdType | null // Monthly fixed amount or percentage.
    quotaNotifyTotalEnabled?: boolean | null // Total nullable alert switch.
    quotaNotifyTotalThreshold?: number | null // Total notification threshold amount.
    quotaNotifyTotalThresholdType?: QuotaThresholdType | null // Total fixed amount or percentage.
  }>(),
  {
    quotaNotifyGlobalEnabled: false,
    quotaNotifyDailyEnabled: null,
    quotaNotifyDailyThreshold: null,
    quotaNotifyDailyThresholdType: null,
    quotaNotifyWeeklyEnabled: null,
    quotaNotifyMonthlyEnabled: null,
    quotaNotifyWeeklyThreshold: null,
    quotaNotifyMonthlyThreshold: null,
    quotaNotifyWeeklyThresholdType: null,
    quotaNotifyMonthlyThresholdType: null,
    quotaNotifyTotalEnabled: null,
    quotaNotifyTotalThreshold: null,
    quotaNotifyTotalThresholdType: null,
  },
)

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:totalLimit': [value: number | null] // Replacement total limit draft value.
  'update:dailyLimit': [value: number | null] // Replacement daily limit draft value.
  'update:weeklyLimit': [value: number | null] // Replacement weekly limit draft value.
  'update:monthlyLimit': [value: number | null] // Replacement monthly limit draft value.
  'update:dailyResetMode': [value: QuotaResetMode | null] // Replacement daily reset mode draft value.
  'update:dailyResetHour': [value: number | null] // Replacement daily reset hour draft value.
  'update:weeklyResetMode': [value: QuotaResetMode | null] // Replacement weekly reset mode draft value.
  'update:monthlyResetMode': [value: QuotaResetMode | null] // Replacement monthly reset mode draft value.
  'update:weeklyResetDay': [value: number | null] // Replacement weekly reset day draft value.
  'update:monthlyResetDay': [value: number | null] // Replacement monthly reset day draft value.
  'update:weeklyResetHour': [value: number | null] // Replacement weekly reset hour draft value.
  'update:monthlyResetHour': [value: number | null] // Replacement monthly reset hour draft value.
  'update:resetTimezone': [value: string | null] // Replacement reset timezone draft value.
  'update:quotaNotifyDailyEnabled': [value: boolean | null] // Replacement quota notify daily enabled draft value.
  'update:quotaNotifyDailyThreshold': [value: number | null] // Replacement quota notify daily threshold draft value.
  'update:quotaNotifyDailyThresholdType': [value: QuotaThresholdType | null] // Replacement quota notify daily threshold type draft value.
  'update:quotaNotifyWeeklyEnabled': [value: boolean | null] // Replacement quota notify weekly enabled draft value.
  'update:quotaNotifyMonthlyEnabled': [value: boolean | null] // Replacement quota notify monthly enabled draft value.
  'update:quotaNotifyWeeklyThreshold': [value: number | null] // Replacement quota notify weekly threshold draft value.
  'update:quotaNotifyMonthlyThreshold': [value: number | null] // Replacement quota notify monthly threshold draft value.
  'update:quotaNotifyWeeklyThresholdType': [value: QuotaThresholdType | null] // Replacement quota notify weekly threshold type draft value.
  'update:quotaNotifyMonthlyThresholdType': [value: QuotaThresholdType | null] // Replacement quota notify monthly threshold type draft value.
  'update:quotaNotifyTotalEnabled': [value: boolean | null] // Replacement quota notify total enabled draft value.
  'update:quotaNotifyTotalThreshold': [value: number | null] // Replacement quota notify total threshold draft value.
  'update:quotaNotifyTotalThresholdType': [value: QuotaThresholdType | null] // Replacement quota notify total threshold type draft value.
}>()

// enabled reports whether any positive total, daily, weekly, or monthly amount restores
// the quota controls as active.
const enabled = computed(
  () =>
    (props.totalLimit != null && props.totalLimit > 0) ||
    (props.dailyLimit != null && props.dailyLimit > 0) ||
    (props.weeklyLimit != null && props.weeklyLimit > 0) ||
    (props.monthlyLimit != null && props.monthlyLimit > 0),
)

// localEnabled lets an administrator open the controls before entering an amount and
// follows later positive-limit state changes.
const localEnabled = ref(enabled.value)

watch(enabled, (val) => {
  localEnabled.value = val
})

// Turning limits off clears all amount and reset drafts together; notification drafts
// remain separately owned so their policies can survive later quota re-enablement.
watch(localEnabled, (val) => {
  if (!val) {
    emit('update:totalLimit', null)
    emit('update:dailyLimit', null)
    emit('update:weeklyLimit', null)
    emit('update:monthlyLimit', null)
    emit('update:dailyResetMode', null)
    emit('update:dailyResetHour', null)
    emit('update:weeklyResetMode', null)
    emit('update:monthlyResetMode', null)
    emit('update:weeklyResetDay', null)
    emit('update:monthlyResetDay', null)
    emit('update:weeklyResetHour', null)
    emit('update:monthlyResetHour', null)
    emit('update:resetTimezone', null)
  }
})

// timezoneOptions provides commonly used IANA zones for the shared fixed-schedule
// timezone stored with the provider quota drafts.
const timezoneOptions = [
  'UTC',
  'Asia/Shanghai',
  'Asia/Tokyo',
  'Asia/Seoul',
  'Asia/Singapore',
  'Asia/Kolkata',
  'Asia/Dubai',
  'Europe/London',
  'Europe/Paris',
  'Europe/Berlin',
  'Europe/Moscow',
  'America/New_York',
  'America/Chicago',
  'America/Denver',
  'America/Los_Angeles',
  'America/Sao_Paulo',
  'Australia/Sydney',
  'Pacific/Auckland',
]

// hourOptions enumerates local clock hours zero through twenty-three for each periodic
// quota dimension using fixed resets.
const hourOptions = Array.from({ length: 24 }, (_, i) => i)

// dayOptions maps JavaScript-style weekday numbers, including Sunday zero, to localized
// names used by the weekly reset controls.
const dayOptions = [
  { value: 1, key: 'monday' },
  { value: 2, key: 'tuesday' },
  { value: 3, key: 'wednesday' },
  { value: 4, key: 'thursday' },
  { value: 5, key: 'friday' },
  { value: 6, key: 'saturday' },
  { value: 0, key: 'sunday' },
]

// weeklyFixedHint displays the selected weekday, hour, and timezone, using Monday
// midnight UTC when the relevant drafts are absent.
const weeklyFixedHint = computed(() => {
  const dayKey = dayOptions.find((d) => d.value === (props.weeklyResetDay ?? 1))?.key || 'monday'
  return t('admin.providers.quotaWeeklyLimitHintFixed', {
    day: t('admin.providers.dayOfWeek.' + dayKey),
    hour: String(props.weeklyResetHour ?? 0).padStart(2, '0'),
    timezone: props.resetTimezone || 'UTC',
  })
})

// monthlyFixedHint displays the monthly calendar date, hour, timezone, and translated
// short-month guidance for the selected reset schedule.
const monthlyFixedHint = computed(() =>
  t('admin.providers.quotaMonthlyLimitHintFixed', {
    day: props.monthlyResetDay ?? 1,
    hour: String(props.monthlyResetHour ?? 0).padStart(2, '0'),
    timezone: props.resetTimezone || 'UTC',
  }),
)

// dailyFixedHint displays the local reset hour and timezone, using midnight UTC when
// those draft fields are absent.
const dailyFixedHint = computed(() =>
  t('admin.providers.quotaDailyLimitHintFixed', {
    hour: String(props.dailyResetHour ?? 0).padStart(2, '0'),
    timezone: props.resetTimezone || 'UTC',
  }),
)
</script>

<template>
  <div class="space-y-4">
    <SettingToggleRow
      :id="`${uid}-enabled`"
      v-model="localEnabled"
      :label="t('admin.providers.quotaLimitToggle')"
      :hint="t('admin.providers.quotaLimitToggleHint')"
      testid="quota-limit-toggle"
    />

    <!-- Collapsible content -->
    <Collapse :open="localEnabled" unmount-on-hide>
      <SettingsSubpanel>
        <!-- Daily quota -->
        <QuotaDimensionRow
          dim="daily"
          :label="t('admin.providers.quotaDailyLimit')"
          :limit="dailyLimit"
          :quota-notify-global-enabled="quotaNotifyGlobalEnabled"
          :notify-enabled="props.quotaNotifyDailyEnabled"
          :notify-threshold="props.quotaNotifyDailyThreshold"
          :notify-threshold-type="props.quotaNotifyDailyThresholdType"
          :reset-mode="dailyResetMode"
          :reset-hour="dailyResetHour"
          :reset-day="null"
          :reset-timezone="resetTimezone"
          :hint-rolling="t('admin.providers.quotaDailyLimitHint')"
          :hint-fixed="dailyFixedHint"
          :hour-options="hourOptions"
          :day-options="dayOptions"
          :timezone-options="timezoneOptions"
          @update:limit="emit('update:dailyLimit', $event)"
          @update:notify-enabled="emit('update:quotaNotifyDailyEnabled', $event)"
          @update:notify-threshold="emit('update:quotaNotifyDailyThreshold', $event)"
          @update:notify-threshold-type="emit('update:quotaNotifyDailyThresholdType', $event)"
          @update:reset-mode="emit('update:dailyResetMode', $event)"
          @update:reset-hour="emit('update:dailyResetHour', $event)"
          @update:reset-timezone="emit('update:resetTimezone', $event)"
        />

        <!-- Weekly quota -->
        <QuotaDimensionRow
          dim="weekly"
          :label="t('admin.providers.quotaWeeklyLimit')"
          :limit="weeklyLimit"
          :quota-notify-global-enabled="quotaNotifyGlobalEnabled"
          :notify-enabled="props.quotaNotifyWeeklyEnabled"
          :notify-threshold="props.quotaNotifyWeeklyThreshold"
          :notify-threshold-type="props.quotaNotifyWeeklyThresholdType"
          :reset-mode="weeklyResetMode"
          :reset-hour="weeklyResetHour"
          :reset-day="weeklyResetDay"
          :reset-timezone="resetTimezone"
          :hint-rolling="t('admin.providers.quotaWeeklyLimitHint')"
          :hint-fixed="weeklyFixedHint"
          :hour-options="hourOptions"
          :day-options="dayOptions"
          :timezone-options="timezoneOptions"
          @update:limit="emit('update:weeklyLimit', $event)"
          @update:notify-enabled="emit('update:quotaNotifyWeeklyEnabled', $event)"
          @update:notify-threshold="emit('update:quotaNotifyWeeklyThreshold', $event)"
          @update:notify-threshold-type="emit('update:quotaNotifyWeeklyThresholdType', $event)"
          @update:reset-mode="emit('update:weeklyResetMode', $event)"
          @update:reset-hour="emit('update:weeklyResetHour', $event)"
          @update:reset-day="emit('update:weeklyResetDay', $event)"
          @update:reset-timezone="emit('update:resetTimezone', $event)"
        />

        <!-- Monthly quota -->
        <QuotaDimensionRow
          dim="monthly"
          :label="t('admin.providers.quotaMonthlyLimit')"
          :limit="monthlyLimit"
          :quota-notify-global-enabled="quotaNotifyGlobalEnabled"
          :notify-enabled="props.quotaNotifyMonthlyEnabled"
          :notify-threshold="props.quotaNotifyMonthlyThreshold"
          :notify-threshold-type="props.quotaNotifyMonthlyThresholdType"
          :reset-mode="monthlyResetMode"
          :reset-hour="monthlyResetHour"
          :reset-day="monthlyResetDay"
          :reset-timezone="resetTimezone"
          :hint-rolling="t('admin.providers.quotaMonthlyLimitHint')"
          :hint-fixed="monthlyFixedHint"
          :hour-options="hourOptions"
          :day-options="dayOptions"
          :timezone-options="timezoneOptions"
          @update:limit="emit('update:monthlyLimit', $event)"
          @update:notify-enabled="emit('update:quotaNotifyMonthlyEnabled', $event)"
          @update:notify-threshold="emit('update:quotaNotifyMonthlyThreshold', $event)"
          @update:notify-threshold-type="emit('update:quotaNotifyMonthlyThresholdType', $event)"
          @update:reset-mode="emit('update:monthlyResetMode', $event)"
          @update:reset-hour="emit('update:monthlyResetHour', $event)"
          @update:reset-day="emit('update:monthlyResetDay', $event)"
          @update:reset-timezone="emit('update:resetTimezone', $event)"
        />

        <!-- Total quota -->
        <QuotaDimensionRow
          dim="total"
          :label="t('admin.providers.quotaTotalLimit')"
          :limit="totalLimit"
          :quota-notify-global-enabled="quotaNotifyGlobalEnabled"
          :notify-enabled="props.quotaNotifyTotalEnabled"
          :notify-threshold="props.quotaNotifyTotalThreshold"
          :notify-threshold-type="props.quotaNotifyTotalThresholdType"
          :reset-mode="null"
          :reset-hour="null"
          :reset-day="null"
          :reset-timezone="null"
          :hint-rolling="t('admin.providers.quotaTotalLimitHint')"
          hint-fixed=""
          :hour-options="hourOptions"
          :day-options="dayOptions"
          @update:limit="emit('update:totalLimit', $event)"
          @update:notify-enabled="emit('update:quotaNotifyTotalEnabled', $event)"
          @update:notify-threshold="emit('update:quotaNotifyTotalThreshold', $event)"
          @update:notify-threshold-type="emit('update:quotaNotifyTotalThresholdType', $event)"
        />
      </SettingsSubpanel>
    </Collapse>
  </div>
</template>
