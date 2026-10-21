<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import QuotaNotifyToggle from './QuotaNotifyToggle.vue'
import type { QuotaThresholdType, QuotaResetMode } from '@/constants/provider'
import Select from '@/components/common/Select.vue'

// QuotaDimensionRow.vue edits one provider spending ceiling and its periodic reset
// schedule. Total quota has no reset selector, while daily, weekly, and monthly rows
// expose the corresponding clock fields. Entering fixed mode initializes missing schedule
// fields before the parent persists the draft.

// t resolves interface labels from the active component locale while reactive consumers
// update their displayed text.
const { t } = useI18n()
// usdUnitSymbol labels monetary quota inputs with the configured display symbol used by
// the balance presentation helper.
const { usdUnitSymbol } = useBalanceDisplay()

// props exposes caller-owned inputs used by the template and derived state in this
// component.
const props = defineProps<{
  // limit and resetMode describe this dimension, while resetHour, resetDay, and
  // resetTimezone supply its fixed schedule. Notification values are independent drafts,
  // and the total dimension has no periodic reset controls.
  dim: 'daily' | 'weekly' | 'monthly' | 'total' // Spending quota dimension identifier.
  label: string // Displayed input label.
  limit: number | null // Spending ceiling in USD.
  quotaNotifyGlobalEnabled: boolean // Global notification feature switch.
  notifyEnabled: boolean | null // Nullable notification enable draft.
  notifyThreshold: number | null // Notification threshold amount draft.
  notifyThresholdType: QuotaThresholdType | null // Fixed amount or percentage.

  // Periodic reset mode; total quota does not render reset controls regardless of this
  // value.
  resetMode: QuotaResetMode | null
  resetHour: number | null // Local reset clock hour.
  resetDay: number | null // Weekday or monthly date.
  // IANA timezone for fixed schedules; the row initializes UTC when entering fixed mode
  // with an empty draft.
  resetTimezone: string | null
  hintRolling: string // Elapsed-window reset explanation text.
  hintFixed: string // Calendar reset explanation text.

  hourOptions: number[] // Available local clock hours.
  dayOptions: {
    value: number // Selector option wire value.
    key: string // Weekday translation key suffix.
  }[] // Weekday values and translation keys.
  timezoneOptions?: string[] // Available IANA timezone identifiers.
}>()

// emit publishes typed draft changes and actions to the parent that owns this component.
const emit = defineEmits<{
  'update:limit': [value: number | null] // Replacement limit draft value.
  'update:notifyEnabled': [value: boolean | null] // Replacement notify enabled draft value.
  'update:notifyThreshold': [value: number | null] // Replacement notify threshold draft value.
  'update:notifyThresholdType': [value: QuotaThresholdType | null] // Replacement notify threshold type draft value.
  'update:resetMode': [value: QuotaResetMode | null] // Replacement reset mode draft value.
  'update:resetHour': [value: number | null] // Replacement reset hour draft value.
  'update:resetDay': [value: number | null] // Replacement reset day draft value.
  'update:resetTimezone': [value: string | null] // Replacement reset timezone draft value.
}>()

// hasResetMode is captured from the initial dimension; callers should keep a row assigned
// to the same dimension.
const hasResetMode = props.dim !== 'total'

// onLimitInput emits the numeric quota amount from the input element. Empty or invalid
// numeric input becomes null, allowing the parent draft to represent a cleared spending
// ceiling.
const onLimitInput = (e: Event) => {
  const raw = (e.target as HTMLInputElement).valueAsNumber
  emit('update:limit', Number.isNaN(raw) ? null : raw)
}

// onModeChange emits a reset mode and initializes absent fields when fixed mode is
// selected. Missing hours become midnight, weekly or monthly days become one, and a
// missing timezone becomes UTC.
const onModeChange = (value: string | number | boolean | null) => {
  const val = value as QuotaResetMode
  emit('update:resetMode', val)
  // Fixed schedules need a clock and timezone before persistence; initialize only missing
  // fields so switching modes retains administrator-supplied schedule values.
  if (val === 'fixed') {
    if (props.resetHour == null) emit('update:resetHour', 0)
    if ((props.dim === 'weekly' || props.dim === 'monthly') && props.resetDay == null)
      emit('update:resetDay', 1)
    if (!props.resetTimezone) emit('update:resetTimezone', 'UTC')
  }
}

// resetModeOptions supplies rolling and fixed reset policies with localized labels for
// the periodic quota row selector.
const resetModeOptions = computed(() => [
  { value: 'rolling', label: t('admin.providers.quotaResetModeRolling') },
  { value: 'fixed', label: t('admin.providers.quotaResetModeFixed') },
])

// weeklyResetDayOptions translates the parent weekday keys while retaining their numeric
// values for the fixed schedule draft.
const weeklyResetDayOptions = computed(() =>
  props.dayOptions.map((day) => ({
    value: day.value,
    label: t('admin.providers.dayOfWeek.' + day.key),
  })),
)

// monthlyResetDayOptions covers dates one through thirty-one; the backend clamps an
// oversized calendar date to the month end.
const monthlyResetDayOptions = Array.from({ length: 31 }, (_, index) => ({
  value: index + 1,
  label: String(index + 1),
}))

// resetHourOptions formats each parent-provided local clock hour as a padded hour label
// for the fixed reset selector.
const resetHourOptions = computed(() =>
  props.hourOptions.map((hour) => ({
    value: hour,
    label: `${String(hour).padStart(2, '0')}:00`,
  })),
)

// resetTimezoneOptions combines each supplied IANA zone with its current offset, allowing
// invalid-zone formatting to return an empty suffix.
const resetTimezoneOptions = computed(() =>
  (props.timezoneOptions ?? []).map((tz) => ({
    value: tz,
    label: `${tz} (${getTimezoneOffsetLabel(tz)})`,
  })),
)

// getTimezoneOffsetLabel formats the current offset for an IANA timezone in the selector.
// Unsupported timezone values return an empty label, and GMT is rendered as GMT+0 for
// consistency.
function getTimezoneOffsetLabel(tz: string): string {
  try {
    const dtf = new Intl.DateTimeFormat('en-US', { timeZone: tz, timeZoneName: 'shortOffset' })
    const parts = dtf.formatToParts(new Date())
    const tzPart = parts.find((p) => p.type === 'timeZoneName')
    return tzPart ? (tzPart.value === 'GMT' ? 'GMT+0' : tzPart.value) : ''
  } catch {
    // Unknown or unsupported timezone identifiers cannot produce an offset, so the selector
    // keeps the zone text and omits only its offset label.
    return ''
  }
}
</script>

<template>
  <div>
    <!-- Title row (only when global notify is enabled) -->
    <div v-if="quotaNotifyGlobalEnabled" class="flex items-center gap-2 mb-1">
      <span class="input-label mb-0 min-w-0 flex-1">{{ label }}</span>
      <span v-if="limit && limit > 0" class="input-label mb-0 min-w-0 flex-1">{{
        t('admin.providers.quotaNotify.alert')
      }}</span>
    </div>
    <label v-else class="input-label">{{ label }}</label>

    <!-- Input row -->
    <div class="flex items-center gap-2">
      <div class="input-icon-wrap min-w-0 flex-1">
        <span class="input-icon text-sm text-gray-500 dark:text-gray-400">{{ usdUnitSymbol }}</span>
        <input
          :value="limit"
          @input="onLimitInput"
          type="number"
          min="0"
          step="0.01"
          class="input input-has-icon input-icon-text"
          :placeholder="t('admin.providers.quotaLimitPlaceholder')"
        />
      </div>
      <QuotaNotifyToggle
        v-if="quotaNotifyGlobalEnabled && limit && limit > 0"
        class="flex-1 min-w-0"
        :enabled="notifyEnabled"
        :threshold="notifyThreshold"
        :threshold-type="notifyThresholdType"
        @update:enabled="emit('update:notifyEnabled', $event)"
        @update:threshold="emit('update:notifyThreshold', $event)"
        @update:threshold-type="emit('update:notifyThresholdType', $event)"
      />
    </div>

    <!-- Reset settings for periodic quotas -->
    <div v-if="hasResetMode" class="mt-2 flex flex-wrap items-center gap-2">
      <label class="input-hint mt-0 whitespace-nowrap">{{
        t('admin.providers.quotaResetMode')
      }}</label>
      <Select
        :model-value="resetMode || 'rolling'"
        :options="resetModeOptions"
        class="w-28 text-xs"
        @change="onModeChange"
      />
      <template v-if="resetMode === 'fixed'">
        <!-- Weekly: day of week selector -->
        <template v-if="dim === 'weekly'">
          <label class="input-hint mt-0 whitespace-nowrap">{{
            t('admin.providers.quotaWeeklyResetDay')
          }}</label>
          <Select
            :model-value="resetDay ?? 1"
            :options="weeklyResetDayOptions"
            class="w-28 text-xs"
            @change="emit('update:resetDay', Number($event))"
          />
        </template>
        <template v-if="dim === 'monthly'">
          <label class="input-hint mt-0 whitespace-nowrap">{{
            t('admin.providers.quotaMonthlyResetDay')
          }}</label>
          <Select
            :model-value="resetDay ?? 1"
            :options="monthlyResetDayOptions"
            class="w-28 text-xs"
            @change="emit('update:resetDay', Number($event))"
          />
        </template>
        <label class="input-hint mt-0 whitespace-nowrap">{{
          t('admin.providers.quotaResetHour')
        }}</label>
        <Select
          :model-value="resetHour ?? 0"
          :options="resetHourOptions"
          class="w-24 text-xs"
          @change="emit('update:resetHour', Number($event))"
        />
        <template v-if="timezoneOptions && timezoneOptions.length > 0">
          <Select
            :model-value="resetTimezone || 'UTC'"
            :options="resetTimezoneOptions"
            class="min-w-44 text-xs"
            searchable
            @change="emit('update:resetTimezone', String($event))"
          />
        </template>
      </template>
      <span class="input-hint mt-0">
        <template v-if="resetMode === 'fixed'">{{ hintFixed }}</template>
        <template v-else>{{ hintRolling }}</template>
      </span>
    </div>

    <!-- Total dimension hint (no reset mode) -->
    <p v-if="!hasResetMode" class="input-hint">{{ hintRolling }}</p>
  </div>
</template>
