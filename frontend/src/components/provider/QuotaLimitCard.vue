<script setup lang="ts">
import Collapse from '@/components/common/Collapse.vue'

import SettingsSubpanel from '@/components/common/settings/SettingsSubpanel.vue'
import SettingToggleRow from '@/components/common/settings/SettingToggleRow.vue'
import { ref, watch, computed, useId } from 'vue'
import { useI18n } from 'vue-i18n'
import QuotaDimensionRow from './QuotaDimensionRow.vue'
import type { QuotaThresholdType, QuotaResetMode } from '@/constants/provider'

// QuotaLimitCard.vue edits provider spending limits, reset schedules, and alerts.
// Daily, weekly, monthly, and total limits share an enable switch while each
// dimension keeps its own amount and notification threshold. Periodic limits
// also retain an independent reset policy and clock configuration. A single
// timezone applies to every fixed schedule and is passed through row events.
//
// The enable switch reflects any positive limit restored by the caller. Turning
// it off clears every limit and reset draft together so a monthly-only provider
// can be enabled, edited, and disabled through the same controls as other quotas.

const { t } = useI18n()
const uid = useId()

const props = withDefaults(defineProps<{
  totalLimit: number | null
  dailyLimit: number | null
  weeklyLimit: number | null
  monthlyLimit: number | null // monthly spending ceiling in USD
  dailyResetMode: QuotaResetMode | null
  dailyResetHour: number | null
  weeklyResetMode: QuotaResetMode | null
  monthlyResetMode: QuotaResetMode | null // monthly reset policy selector
  weeklyResetDay: number | null
  monthlyResetDay: number | null // monthly reset calendar date
  weeklyResetHour: number | null
  monthlyResetHour: number | null // monthly local reset hour
  resetTimezone: string | null
  quotaNotifyGlobalEnabled?: boolean
  quotaNotifyDailyEnabled?: boolean | null
  quotaNotifyDailyThreshold?: number | null
  quotaNotifyDailyThresholdType?: QuotaThresholdType | null
  quotaNotifyWeeklyEnabled?: boolean | null
  quotaNotifyMonthlyEnabled?: boolean | null // monthly quota alert switch
  quotaNotifyWeeklyThreshold?: number | null
  quotaNotifyMonthlyThreshold?: number | null // monthly quota alert threshold
  quotaNotifyWeeklyThresholdType?: QuotaThresholdType | null
  quotaNotifyMonthlyThresholdType?: QuotaThresholdType | null // monthly alert threshold interpretation
  quotaNotifyTotalEnabled?: boolean | null
  quotaNotifyTotalThreshold?: number | null
  quotaNotifyTotalThresholdType?: QuotaThresholdType | null
}>(), {
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
})

const emit = defineEmits<{
  'update:totalLimit': [value: number | null]
  'update:dailyLimit': [value: number | null]
  'update:weeklyLimit': [value: number | null]
  'update:monthlyLimit': [value: number | null]
  'update:dailyResetMode': [value: QuotaResetMode | null]
  'update:dailyResetHour': [value: number | null]
  'update:weeklyResetMode': [value: QuotaResetMode | null]
  'update:monthlyResetMode': [value: QuotaResetMode | null]
  'update:weeklyResetDay': [value: number | null]
  'update:monthlyResetDay': [value: number | null]
  'update:weeklyResetHour': [value: number | null]
  'update:monthlyResetHour': [value: number | null]
  'update:resetTimezone': [value: string | null]
  'update:quotaNotifyDailyEnabled': [value: boolean | null]
  'update:quotaNotifyDailyThreshold': [value: number | null]
  'update:quotaNotifyDailyThresholdType': [value: QuotaThresholdType | null]
  'update:quotaNotifyWeeklyEnabled': [value: boolean | null]
  'update:quotaNotifyMonthlyEnabled': [value: boolean | null]
  'update:quotaNotifyWeeklyThreshold': [value: number | null]
  'update:quotaNotifyMonthlyThreshold': [value: number | null]
  'update:quotaNotifyWeeklyThresholdType': [value: QuotaThresholdType | null]
  'update:quotaNotifyMonthlyThresholdType': [value: QuotaThresholdType | null]
  'update:quotaNotifyTotalEnabled': [value: boolean | null]
  'update:quotaNotifyTotalThreshold': [value: number | null]
  'update:quotaNotifyTotalThresholdType': [value: QuotaThresholdType | null]
}>()

// enabled reports whether any positive spending limit activates the quota controls.
const enabled = computed(() =>
  (props.totalLimit != null && props.totalLimit > 0) ||
  (props.dailyLimit != null && props.dailyLimit > 0) ||
  (props.weeklyLimit != null && props.weeklyLimit > 0) ||
  (props.monthlyLimit != null && props.monthlyLimit > 0)
)

const localEnabled = ref(enabled.value)

// Sync when props change externally
watch(enabled, (val) => {
  localEnabled.value = val
})

// 关闭额度限制时清空全部取值。
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

// Common timezone options
const timezoneOptions = [
  'UTC', 'Asia/Shanghai', 'Asia/Tokyo', 'Asia/Seoul', 'Asia/Singapore', 'Asia/Kolkata',
  'Asia/Dubai', 'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
  'America/New_York', 'America/Chicago', 'America/Denver', 'America/Los_Angeles',
  'America/Sao_Paulo', 'Australia/Sydney', 'Pacific/Auckland',
]

// Hours for dropdown (0-23)
const hourOptions = Array.from({ length: 24 }, (_, i) => i)

// Day of week options
const dayOptions = [
  { value: 1, key: 'monday' },
  { value: 2, key: 'tuesday' },
  { value: 3, key: 'wednesday' },
  { value: 4, key: 'thursday' },
  { value: 5, key: 'friday' },
  { value: 6, key: 'saturday' },
  { value: 0, key: 'sunday' },
]

// Precomputed hint strings for the weekly fixed mode
const weeklyFixedHint = computed(() => {
  const dayKey = dayOptions.find(d => d.value === (props.weeklyResetDay ?? 1))?.key || 'monday'
  return t('admin.providers.quotaWeeklyLimitHintFixed', {
    day: t('admin.providers.dayOfWeek.' + dayKey),
    hour: String(props.weeklyResetHour ?? 0).padStart(2, '0'),
    timezone: props.resetTimezone || 'UTC',
  })
})

// monthlyFixedHint formats the monthly reset date, local clock, timezone, and short-month rule for the quota row.
const monthlyFixedHint = computed(() =>
  t('admin.providers.quotaMonthlyLimitHintFixed', {
    day: props.monthlyResetDay ?? 1,
    hour: String(props.monthlyResetHour ?? 0).padStart(2, '0'),
    timezone: props.resetTimezone || 'UTC',
  })
)

const dailyFixedHint = computed(() =>
  t('admin.providers.quotaDailyLimitHintFixed', {
    hour: String(props.dailyResetHour ?? 0).padStart(2, '0'),
    timezone: props.resetTimezone || 'UTC',
  })
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
