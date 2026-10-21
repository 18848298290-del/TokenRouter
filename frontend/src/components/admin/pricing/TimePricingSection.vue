<template>
  <RuleListEditor
    class="mt-3 border-t border-gray-200 pt-3 dark:border-dark-600"
    :items="modelValue.periods"
    :title="t('admin.pricing.form.timePricing')"
    :add-label="t('admin.pricing.form.addTimePeriod')"
    :remove-label="t('admin.pricing.form.removeTimePeriod')"
    variant="card"
    :item-label="(index) => t('common.ruleIndex', { index: index + 1 })"
    test-id="time-periods"
    @add="addPeriod"
    @remove="removePeriod"
  >
    <template #header-extra>
      <div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
        <div class="min-w-0">
          <label class="block text-xs text-gray-400">
            {{ t('admin.pricing.form.timezone') }}
          </label>
          <Select
            :model-value="modelValue.timezone"
            :options="timezoneOptions"
            :aria-label="t('admin.pricing.form.timezone')"
            data-testid="time-pricing-timezone"
            searchable
            creatable
            class="mt-1 w-full"
            @update:model-value="updateTimezone"
          />
        </div>
        <div class="min-w-0">
          <label class="block text-xs text-gray-400">
            {{ t('admin.pricing.form.timePricingDayScope') }}
          </label>
          <Select
            :model-value="modelValue.weekdays_only"
            :options="dayScopeOptions"
            :aria-label="t('admin.pricing.form.timePricingDayScope')"
            data-testid="time-pricing-day-scope"
            class="mt-1 w-full"
            @update:model-value="updateDayScope"
          />
        </div>
      </div>
    </template>
    <template #row="{ item: period, index }">
      <div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
        <div class="min-w-0">
          <label :for="`${inputIdPrefix}-start-${index}`" class="block text-xs text-gray-400">
            {{ t('admin.pricing.form.startTime') }}
          </label>
          <input
            :id="`${inputIdPrefix}-start-${index}`"
            :value="period.start_time"
            type="text"
            inputmode="numeric"
            maxlength="8"
            placeholder="HH:mm:ss"
            pattern="[0-9]{2}:[0-9]{2}:[0-9]{2}"
            autocomplete="off"
            class="input mt-1 w-full text-sm"
            @input="
              updatePeriod(
                index,
                'start_time',
                normalizeClockTime(($event.target as HTMLInputElement).value),
              )
            "
          />
        </div>
        <div class="min-w-0">
          <label :for="`${inputIdPrefix}-end-${index}`" class="block text-xs text-gray-400">
            {{ t('admin.pricing.form.endTime') }}
          </label>
          <input
            :id="`${inputIdPrefix}-end-${index}`"
            :value="period.end_time"
            type="text"
            inputmode="numeric"
            maxlength="8"
            placeholder="HH:mm:ss"
            pattern="[0-9]{2}:[0-9]{2}:[0-9]{2}"
            autocomplete="off"
            class="input mt-1 w-full text-sm"
            @input="
              updatePeriod(
                index,
                'end_time',
                normalizeClockTime(($event.target as HTMLInputElement).value),
              )
            "
          />
        </div>
        <div class="min-w-0">
          <label :for="`${inputIdPrefix}-multiplier-${index}`" class="block text-xs text-gray-400">
            {{ t('admin.pricing.form.multiplier') }}
          </label>
          <input
            :id="`${inputIdPrefix}-multiplier-${index}`"
            :value="period.multiplier"
            type="number"
            min="0.01"
            step="0.01"
            class="input mt-1 w-full text-sm"
            @input="updatePeriod(index, 'multiplier', ($event.target as HTMLInputElement).value)"
            @blur="formatMultiplier(index, ($event.target as HTMLInputElement).value)"
          />
        </div>
      </div>
    </template>
  </RuleListEditor>
</template>

<script setup lang="ts">
import { computed, getCurrentInstance } from 'vue'
import { useI18n } from 'vue-i18n'
import RuleListEditor from '@/components/common/RuleListEditor.vue'
import Select from '@/components/common/Select.vue'
import {
  COMMON_TIMEZONES,
  formatTimezoneOffset,
  isValidTimePricingMultiplier,
  type TimePricingFormEntry,
  type TimePricingPeriodFormEntry,
} from './types'

// TimePricingSection.vue edits recurring price periods, their timezone, and weekday scope. The
// parent owns the schedule draft and receives replacement schedule objects through update
// events. Period edits copy the array but mutate its existing row object because RuleListEditor
// derives display keys from object identity. Keeping that identity across a parent update keeps
// continuous typing attached to the same mounted input.
//
// Clock inputs normalize full-width separators, minute precision, and the 24-hour endpoint
// before publication. Multiplier inputs retain incomplete text until the accepted format can
// be displayed with two decimal places. The parent remains responsible for schedule validation,
// including overlap, timezone, and invalid multiplier errors.

// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  // modelValue belongs to the parent price card. Period edits publish a new array while retaining
  // and mutating the selected period object.
  modelValue: TimePricingFormEntry
}>()
// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  'update:modelValue': [
    value: TimePricingFormEntry, // replacement field value
  ] // replacement parent-owned field value
}>()
// inputIdPrefix namespaces schedule inputs by component instance so multiple price cards can
// render distinct accessible field identifiers.
const inputIdPrefix = `time-pricing-${getCurrentInstance()?.uid}`

// timezoneOptions combines common timezone identifiers with current UTC offsets, omitting the
// offset label when formatting cannot resolve it.
const timezoneOptions = COMMON_TIMEZONES.map((value) => {
  const offset = formatTimezoneOffset(value)
  return { value, label: offset ? `${value} (${offset})` : value }
})

// dayScopeOptions translates the boolean schedule scope into every-day and weekdays-only
// choices consumed by the parent draft.
const dayScopeOptions = computed(() => [
  { value: false, label: t('admin.pricing.form.timePricingEveryDay') },
  { value: true, label: t('admin.pricing.form.timePricingWeekdaysOnly') },
])

// updateTimezone emits the selected timezone string in a copied schedule object.
function updateTimezone(value: string | number | boolean | null) {
  emit('update:modelValue', { ...props.modelValue, timezone: String(value ?? '') })
}

// updateDayScope emits whether the selected schedule applies only on weekdays.
function updateDayScope(value: string | number | boolean | null) {
  emit('update:modelValue', { ...props.modelValue, weekdays_only: value === true })
}

// normalizeClockTime converts supported clock input into the schedule's seconds format.
// Full-width colons become ASCII, hour-minute values gain seconds, and 24:00:00 becomes the
// midnight end marker 00:00:00.
function normalizeClockTime(value: string): string {
  // Normalize alternate separators and minute precision together with the 24-hour endpoint
  // so emitted clock text uses the representation accepted by shared schedule validation.
  const normalized = value.replace(/：/g, ':')
  if (normalized === '24:00:00') return '00:00:00'
  return /^\d{2}:\d{2}$/.test(normalized) ? `${normalized}:00` : normalized
}

// addPeriod appends an empty daily period with the initial multiplier text.
function addPeriod() {
  emit('update:modelValue', {
    ...props.modelValue,
    periods: [...props.modelValue.periods, { start_time: '', end_time: '', multiplier: '1.00' }],
  })
}

// updatePeriod writes one field into the parent-owned period row and emits a replacement
// schedule with a copied period array. Callers supply an existing row index and publish the
// emitted schedule back through modelValue. The copy retains every child object, including the
// row being mutated, because RuleListEditor resolves display keys from those identities. This
// keeps the same input mounted across the parent round trip, allowing repeated keystrokes to
// edit the current row while other periods retain their values.
function updatePeriod(index: number, field: keyof TimePricingPeriodFormEntry, value: string) {
  // Keep the existing period object inside the copied list so continuous input updates stay
  // attached to the same mounted row.
  const periods = [...props.modelValue.periods]
  periods[index][field] = value
  emit('update:modelValue', { ...props.modelValue, periods })
}

// formatMultiplier formats an accepted multiplier to two decimal places on its row. Invalid or
// incomplete input is left intact so the parent validator can report it without discarding the
// draft.
function formatMultiplier(index: number, value: string) {
  if (!isValidTimePricingMultiplier(value)) return
  // Two decimal places match the schedule editor's accepted precision.
  updatePeriod(index, 'multiplier', Number(value).toFixed(2))
}

// removePeriod emits the schedule with the indexed daily period excluded.
function removePeriod(index: number) {
  emit('update:modelValue', {
    ...props.modelValue,
    periods: props.modelValue.periods.filter((_period, current) => current !== index),
  })
}
</script>
