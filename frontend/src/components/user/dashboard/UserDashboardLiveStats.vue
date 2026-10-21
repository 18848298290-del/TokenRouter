<template>
  <div
    class="flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-gray-500 dark:text-dark-400"
    data-testid="live-stats"
    :aria-busy="!loaded"
  >
    <span
      v-for="item in items"
      :key="item.key"
      class="inline-flex items-center gap-2"
      :title="item.hint"
      :data-testid="`live-stat-${item.key}`"
    >
      <Icon
        :name="item.icon"
        size="xs"
        :class="METRIC_TONES[item.tone].icon"
        :animate-on-hover="false"
      />
      <span>{{ item.label }}</span>
      <Skeleton v-if="!loaded" :width="40" :height="12" />
      <span v-else class="font-medium tabular-nums text-gray-900 dark:text-dark-100">
        <span aria-hidden="true">{{ item.animated }}</span>
        <span class="sr-only">{{ item.value }}</span>
      </span>
    </span>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Skeleton from '@/components/common/Skeleton.vue'
import Icon from '@/components/icons/Icon.vue'
import type { IconName } from '@/components/icons/registry'
import { usageAPI, type UserDashboardStats } from '@/api/usage'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { useCountUp } from '@/composables/useCountUp'
import { formatNumberLocaleString as formatNumber, formatTokensK } from '@/utils/format'
import { COUNT_UP_MS } from './dashboardMotion'
import { METRIC_TONES, type MetricTone } from './metricTones'

// UserDashboardLiveStats.vue displays periodically refreshed request rates, latency, and
// daily spending in the dashboard heading. It distinguishes initial loading from unavailable
// data and supplies separate animated visual text and final accessible text for each metric.
//
// Polling runs while the document is visible and manual refresh may start another request. A
// monotonic sequence lets only the latest invocation publish success, failure, and loading
// completion. Hiding the document stops its timer; returning triggers a fresh load and
// timer. Unmount removes the interval and visibility listener, while requests already in
// flight are not cancelled by that cleanup.

// POLL_INTERVAL_MS refreshes visible-page statistics each minute while the API reports its
// recent five-minute rate window.
const POLL_INTERVAL_MS = 60 * 1000

// Translation and balance formatting dependencies render each live statistic with the
// current account display settings.
const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()

// stats and loaded separate missing data from initial loading; timer and requestSeq track
// polling ownership and the latest overlapping request.
const stats = ref<UserDashboardStats | null>(null)
const loaded = ref(false)
let timer: ReturnType<typeof setInterval> | null = null
// requestSeq selects the most recently started request when polling and manual dashboard
// refresh overlap.
let requestSeq = 0

// Animated values follow the current statistics while accessible text uses the final values
// for each metric.
const animatedRpm = useCountUp(() => stats.value?.rpm ?? 0, COUNT_UP_MS)
const animatedTpm = useCountUp(() => stats.value?.tpm ?? 0, COUNT_UP_MS)
const animatedLatency = useCountUp(() => stats.value?.average_duration_ms ?? 0, COUNT_UP_MS)
const animatedTodayCost = useCountUp(() => stats.value?.today_actual_cost ?? 0, COUNT_UP_MS)

// formatLatency formats subsecond durations as rounded milliseconds and longer durations as
// seconds.
const formatLatency = (ms: number): string =>
  ms < 1000 ? `${Math.round(ms)}ms` : `${(ms / 1000).toFixed(2)}s`

// formatCost formats spending with four decimal places below one balance unit.
const formatCost = (value: number): string =>
  formatBalanceAmount(value, { fractionDigits: value >= 1 ? 2 : 4 })

// display formats a present value or returns a dash for missing data. A failed latest
// request clears stats, so every live item uses the same unavailable display.
const display = (format: (value: number) => string, value: number | undefined): string =>
  value === undefined ? '—' : format(value)

// items pairs final and animated text with labels, hints, and metric colors for the heading
// statistics.
const items = computed(() => {
  const current = stats.value
  return [
    {
      key: 'rpm',
      icon: 'swap' as IconName,
      tone: 'requests' as MetricTone,
      label: t('dashboard.live.rpm'),
      hint: t('dashboard.live.rpmHint'),
      value: display((value) => formatNumber(Math.round(value * 10) / 10), current?.rpm),
      animated: display(
        (value) => formatNumber(Math.round(value * 10) / 10),
        current ? animatedRpm.value : undefined,
      ),
    },
    {
      key: 'tpm',
      icon: 'layers' as IconName,
      tone: 'tokens' as MetricTone,
      label: t('dashboard.live.tpm'),
      hint: t('dashboard.live.tpmHint'),
      value: display(formatTokensK, current?.tpm),
      animated: display(formatTokensK, current ? animatedTpm.value : undefined),
    },
    {
      key: 'latency',
      icon: 'clock' as IconName,
      tone: 'latency' as MetricTone,
      label: t('dashboard.live.latency'),
      hint: undefined,
      value: display(formatLatency, current?.average_duration_ms),
      animated: display(formatLatency, current ? animatedLatency.value : undefined),
    },
    {
      key: 'todayCost',
      icon: 'dollar' as IconName,
      tone: 'cost' as MetricTone,
      label: t('dashboard.live.todayCost'),
      hint: undefined,
      value: display(formatCost, current?.today_actual_cost),
      animated: display(formatCost, current ? animatedTodayCost.value : undefined),
    },
  ]
})

// load requests fresh dashboard statistics and publishes them if its sequence is still
// current. Manual refresh and polling can overlap, so both success and failure check the
// generation before changing data. A latest-request failure clears the values while leaving
// the status strip visible. Completion sets loaded only for the winning request, preventing
// an earlier request from ending the initial skeleton for a newer request.
const load = async () => {
  const seq = ++requestSeq
  try {
    const result = await usageAPI.getDashboardStats()
    // A refresh or poll may have started while awaiting this response, so publish success
    // only while this invocation still owns the shared statistics shown in the heading.
    if (seq !== requestSeq) return
    stats.value = result
  } catch (error) {
    // An obsolete failure must leave newer statistics intact; a current failure clears the
    // record so every live item displays its unavailable placeholder together.
    if (seq !== requestSeq) return
    console.error('Failed to load live usage stats:', error)
    stats.value = null
  } finally {
    // Only the latest invocation may end initial loading, because an earlier completion can
    // occur while a newer request still owns the heading's pending statistics.
    if (seq === requestSeq) loaded.value = true
  }
}

// stopPolling clears the component polling interval and resets its handle. It is safe to
// call before starting a replacement timer or while disposing a component without an active
// timer.
const stopPolling = () => {
  if (timer) clearInterval(timer)
  timer = null
}

// startPolling replaces any existing timer with the component polling interval. Visibility
// and mount handlers decide when it may run; disposal calls stopPolling to release that
// interval.
const startPolling = () => {
  stopPolling()
  timer = setInterval(() => void load(), POLL_INTERVAL_MS)
}

// onVisibilityChange pauses polling while the document is hidden and refreshes immediately
// when it becomes visible. Visibility changes manage the timer while existing requests
// finish through load generation checks.
const onVisibilityChange = () => {
  if (document.visibilityState === 'hidden') {
    stopPolling()
    return
  }
  void load()
  startPolling()
}

onMounted(() => {
  void load()
  if (document.visibilityState !== 'hidden') startPolling()
  document.addEventListener('visibilitychange', onVisibilityChange)
})

// Release the interval and document listener owned by this instance so navigation cannot
// continue scheduling new dashboard statistics requests.
onBeforeUnmount(() => {
  stopPolling()
  document.removeEventListener('visibilitychange', onVisibilityChange)
})

// The dashboard refresh action calls reload through the component ref and awaits the latest
// statistics request.
defineExpose({ reload: load })
</script>
