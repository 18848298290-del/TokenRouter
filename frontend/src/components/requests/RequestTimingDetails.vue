<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UsageLogTiming } from '@/types'
import { formatTimingMs, timingBreakdown, type TimingRow } from '@/utils/requestTiming'

// RequestTimingDetails.vue renders the compact timing grid of the usage tooltip.
// Admission durations sit under a heading that shows their total, which is the
// same number as the slot row in the timeline below; offsets are never summed.
// One grid holds every pair, including the caller's rows and any retry wait, so
// labels and values line up across groups. Missing values show a dash, while a
// failed admission titles its durations as running until the request ended.

// The undefined default stops Vue from casting an omitted boolean prop to false,
// which would hide stream-only rows of records whose stream mode is unknown.
const props = withDefaults(defineProps<{
  timing: UsageLogTiming // Persisted request timing observations.
  // Legacy client stream mode, used only when the record lacks its own flag.
  stream?: boolean
}>(), { stream: undefined })
const { t } = useI18n()

const breakdown = computed(() => timingBreakdown(props.timing, props.stream))

// durationsTitle names the endpoint the four durations add up to: the slot
// acquisition when admission succeeded, otherwise the end of the request. The
// total beside it spells out the equality with the slot row once admission is known.
const durationsTitle = computed(() => t(
  breakdown.value.admitted === false ? 'requests.timing.untilEnd' : 'requests.timing.beforeSlot',
))

// display formats a row value; a request that never acquired a slot says so
// at the slot row, because a dash would read as a lost observation.
function display(row: TimingRow): string {
  if (row.value == null && row.key === 'provider_slot_acquired_ms' && breakdown.value.admitted === false) {
    return t('requests.timing.notAdmitted')
  }
  return formatTimingMs(row.value)
}
</script>

<template>
  <div
    data-testid="request-timing-details"
    class="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-1 text-xs leading-4 md:grid-cols-[max-content_minmax(0,1fr)_max-content_minmax(0,1fr)]"
  >
    <!-- Callers put request-level pairs here so they share this grid's columns. -->
    <slot />
    <template v-if="breakdown.retryWait">
      <span class="text-gray-400">{{ t('requests.timingNames.retry_provider_slot_wait_ms') }}</span>
      <span class="font-medium tabular-nums text-right">{{ display(breakdown.retryWait) }}</span>
    </template>
    <template v-if="breakdown.durations.length">
      <div class="col-span-full mt-1 flex justify-between gap-4 border-t border-gray-700 pt-1.5 text-gray-300 dark:border-dark-600">
        <span>{{ durationsTitle }}</span>
        <span v-if="breakdown.admissionTotal != null" class="font-medium tabular-nums">{{ t(breakdown.admitted ? 'requests.timing.totalIsSlot' : 'requests.timing.total', { value: formatTimingMs(breakdown.admissionTotal) }) }}</span>
      </div>
      <template v-for="row in breakdown.durations" :key="row.key">
        <span class="text-gray-400">{{ t(`requests.timingNames.${row.key}`) }}</span>
        <span class="font-medium tabular-nums text-right">{{ display(row) }}</span>
      </template>
    </template>
    <div class="col-span-full mt-1 border-t border-gray-700 pt-1.5 text-gray-300 dark:border-dark-600">{{ t('requests.timing.events') }}</div>
    <template v-for="row in breakdown.events" :key="row.key">
      <span class="text-gray-400">{{ t(`requests.timingNames.${row.key}`) }}</span>
      <span class="font-medium tabular-nums text-right">{{ display(row) }}</span>
    </template>
  </div>
</template>
