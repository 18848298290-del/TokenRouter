<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { UsageLogTiming } from '@/types'
import { formatTimingMs, timingBreakdown } from '@/utils/requestTiming'

// RequestTimeline.vue draws one timing record on the request lookup card as an
// admission equation above a waterfall of ingress-relative offsets. The four
// admission durations may be added, so they are written as a sum whose total
// is the slot offset. Offsets all start at gateway ingress and must never be
// added, so every bar starts at the left edge and ends at its own offset.
//
// The card shows request failure timings, each usage row's performance and
// each failed attempt's transport stages with this component, so all three
// share labels, scale rules and colors in both themes. Only recorded offsets
// get a bar, and the shared breakdown hides stream-only stages of synchronous
// requests.

const props = defineProps<{
  timing: UsageLogTiming // Persisted request timing observations.
  // Duration is the whole request in milliseconds. When present it adds a
  // closing total row and becomes the minimum bar scale, so timelines of
  // one request line up; without it bars scale to the latest offset.
  duration?: number | null
}>()
const { t } = useI18n()

// view splits the record into the admission equation and waterfall rows. Bars
// keep a sliver for offsets near zero so an early stage still marks its place.
const view = computed(() => {
  const breakdown = timingBreakdown(props.timing)
  const events = breakdown.events
    .filter((row) => row.value != null)
    .map((row) => ({ key: row.key, value: row.value ?? 0 }))
    .sort((a, b) => a.value - b.value)
  const scale = Math.max(props.duration ?? 0, ...events.map((row) => row.value), 1)
  return {
    breakdown,
    scale,
    rows: events.map((row) => ({ ...row, percent: Math.max((row.value / scale) * 100, 0.5) })),
  }
})
</script>

<template>
  <div class="space-y-3 text-xs">
    <!-- The four durations add up to the slot row of the waterfall below. -->
    <p v-if="view.breakdown.durations.length" class="flex flex-wrap items-baseline gap-x-1.5 gap-y-1">
      <span class="font-medium text-gray-900 dark:text-dark-50">{{ t(view.breakdown.admitted === false ? 'requests.timing.untilEnd' : 'requests.timingNames.provider_slot_acquired_ms') }}</span>
      <span v-if="view.breakdown.admissionTotal != null" class="font-mono font-medium tabular-nums text-gray-900 dark:text-dark-50">{{ formatTimingMs(view.breakdown.admissionTotal) }}</span>
      <span class="text-gray-400 dark:text-dark-500">=</span>
      <template v-for="(row, index) in view.breakdown.durations" :key="row.key">
        <span v-if="index" class="text-gray-400 dark:text-dark-500">+</span>
        <span class="text-gray-500 dark:text-dark-400">{{ t(`requests.timingNames.${row.key}`) }}</span>
        <span class="font-mono tabular-nums text-gray-900 dark:text-dark-100">{{ formatTimingMs(row.value) }}</span>
      </template>
      <template v-if="view.breakdown.retryWait">
        <span class="ml-3 text-gray-500 dark:text-dark-400">{{ t('requests.timingNames.retry_provider_slot_wait_ms') }}</span>
        <span class="font-mono tabular-nums text-gray-900 dark:text-dark-100">{{ formatTimingMs(view.breakdown.retryWait.value) }}</span>
      </template>
    </p>
    <ol v-if="view.rows.length" class="space-y-2">
      <li
        v-for="row in view.rows"
        :key="row.key"
        class="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)_4rem] items-center gap-3 sm:grid-cols-[9rem_minmax(0,1fr)_5rem]"
      >
        <span class="truncate text-gray-600 dark:text-dark-300" :title="t(`requests.timingNames.${row.key}`)">{{ t(`requests.timingNames.${row.key}`) }}</span>
        <span class="relative h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800">
          <span class="absolute inset-y-0 left-0 rounded-full bg-primary-500/70" :style="{ width: `${row.percent}%` }" />
        </span>
        <span class="text-right font-mono tabular-nums text-gray-900 dark:text-dark-100">{{ formatTimingMs(row.value) }}</span>
      </li>
      <li
        v-if="duration != null"
        class="grid grid-cols-[minmax(0,7rem)_minmax(0,1fr)_4rem] items-center gap-3 border-t border-dashed border-gray-200 pt-2 dark:border-dark-600 sm:grid-cols-[9rem_minmax(0,1fr)_5rem]"
      >
        <span class="font-medium text-gray-900 dark:text-dark-50">{{ t('requests.totalDuration') }}</span>
        <span class="relative h-1.5 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-800">
          <span class="absolute inset-y-0 left-0 rounded-full bg-primary-600 dark:bg-primary-500" :style="{ width: `${(duration / view.scale) * 100}%` }" />
        </span>
        <span class="text-right font-mono font-medium tabular-nums text-gray-900 dark:text-dark-50">{{ formatTimingMs(duration) }}</span>
      </li>
    </ol>
  </div>
</template>
