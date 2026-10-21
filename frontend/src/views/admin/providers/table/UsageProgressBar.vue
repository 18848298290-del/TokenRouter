<template>
  <div>
    <!-- Local window statistics appear above their progress bar. -->
    <ProviderUsageStatsChips
      v-if="windowStats && (windowStats.requests > 0 || windowStats.tokens > 0)"
      :stats="windowStats"
      scope="window"
    />

    <!-- Progress rows follow right-aligned mobile value columns and left-aligned desktop table cells. -->
    <div class="flex items-center justify-end gap-1 lg:justify-start">
      <!-- Fixed label width aligns multiple progress bars within one provider cell; automatic width follows the caller-selected label layout. -->
      <span
        :class="[
          props.labelWidth === 'auto'
            ? 'max-w-[72px] truncate text-left'
            : wideLabel
              ? 'w-[48px] whitespace-nowrap'
              : 'w-[32px] whitespace-nowrap',
          'shrink-0 rounded-compact px-1 text-xs font-medium',
          props.labelWidth === 'auto' ? '' : 'text-center',
          labelClass,
        ]"
      >
        {{ label }}
      </span>

      <!-- Progress bar container -->
      <div class="h-1.5 w-8 shrink-0 overflow-hidden rounded-full bg-gray-200 dark:bg-dark-700">
        <div
          :class="['h-full transition-[width,background-color] duration-layout', barClass]"
          :style="{ width: barWidth }"
        ></div>
      </div>

      <!-- Percentage -->
      <span :class="['w-[32px] shrink-0 text-right text-xs font-medium', textClass]">
        {{ displayPercent }}
      </span>

      <!-- Reset time -->
      <span v-if="shouldShowResetTime" class="shrink-0 text-xs text-gray-400">
        {{ formatResetTime }}
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { useI18n } from 'vue-i18n'
import type { WindowStats } from '@/api/admin/providers.types'
import ProviderUsageStatsChips from './ProviderUsageStatsChips.vue'

/**
 * UsageProgressBar.vue renders utilization or remaining capacity with a bounded bar, percentage
 * label, reset countdown, and optional local usage chips. Remaining-capacity mode reverses the
 * warning direction, while consumed-utilization mode permits text above one hundred percent and
 * clamps only the visual bar width.
 *
 * Reset text combines the clock with the latest utilization observation. An expired deadline
 * with positive usage shows pending refresh, because the component cannot infer a renewed
 * upstream allowance from elapsed time alone. The minute timer runs only while a reset value is
 * supplied and is owned by the VueUse effect scope. Missing reset data and explicitly idle
 * rolling windows have distinct labels.
 */

/**
 * props supplies utilization, reset metadata, and presentation choices while defaults keep
 * ordinary usage labels at a fixed width.
 */
const props = withDefaults(
  defineProps<{
    label: string // usage window display label
    utilization: number // percentage from zero upward
    // Percentage (0-100+)
    /**
     * Optional reset timestamp activates the minute clock; the latest utilization decides
     * whether an elapsed reset is pending refresh.
     */
    resetsAt?: string | null
    color: 'indigo' | 'emerald' | 'purple' | 'amber' // window label color family
    windowStats?: WindowStats | null // optional local window statistics
    /**
     * When enabled, nonpositive utilization displays immediate availability even if the
     * supplied reset timestamp lies later.
     */
    showNowWhenIdle?: boolean
    /**
     * When enabled, utilization represents remaining capacity and low percentages receive
     * warning colors while text is bounded to zero through one hundred.
     */
    remainingCapacity?: boolean
    wideLabel?: boolean // allow a wider window label
    /**
     * Fixed centers a fixed-width label; auto uses a bounded, truncated label aligned to the
     * left.
     */
    labelWidth?: 'fixed' | 'auto'
  }>(),
  { labelWidth: 'fixed' },
)

/**
 * Translation labels reset states, and the local minute clock updates countdown text only while
 * a reset timestamp is supplied.
 */
const { t } = useI18n()

/**
 * now supports minute-level reset display; the VueUse interval starts paused and follows the
 * presence of resetsAt in props.
 */
const now = ref(new Date())
const { pause: pauseClock, resume: resumeClock } = useIntervalFn(
  () => {
    now.value = new Date()
  },
  60_000,
  { immediate: false },
)
if (props.resetsAt) resumeClock()
watch(
  () => props.resetsAt,
  (val) => {
    if (val) {
      now.value = new Date()
      // Refresh the local clock before resuming so newly supplied reset metadata is evaluated
      // against the current wall time.
      resumeClock()
    } else {
      // Pause the component-owned interval when reset metadata disappears so rows without
      // countdowns stop scheduling clock updates.
      pauseClock()
    }
  },
)

/**
 * labelClass chooses the window-label palette, while barClass and textClass apply utilization
 * or remaining-capacity warning thresholds to the measured percentage.
 */
const labelClass = computed(() => {
  const colors = {
    indigo: 'bg-indigo-100 text-indigo-700 dark:bg-indigo-900/40 dark:text-indigo-300',
    emerald: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/40 dark:text-emerald-300',
    purple: 'bg-purple-100 text-purple-700 dark:bg-purple-900/40 dark:text-purple-300',
    amber: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300',
  }
  return colors[props.color]
})

const barClass = computed(() => {
  // Remaining capacity warns near depletion, reversing the consumed-utilization warning direction.
  if (props.remainingCapacity) {
    if (props.utilization <= 20) {
      return 'bg-red-500'
    } else if (props.utilization <= 50) {
      return 'bg-amber-500'
    }
    return 'bg-green-500'
  }
  // Consumed utilization warns before the full allowance is exhausted.
  if (props.utilization >= 90) {
    return 'bg-red-500'
  } else if (props.utilization >= 75) {
    return 'bg-amber-500'
  } else {
    return 'bg-green-500'
  }
})

const textClass = computed(() => {
  // Remaining capacity warns near depletion, reversing the consumed-utilization warning direction.
  if (props.remainingCapacity) {
    if (props.utilization <= 20) {
      return 'text-red-600 dark:text-red-400'
    } else if (props.utilization <= 50) {
      return 'text-amber-600 dark:text-amber-400'
    }
    return 'text-gray-600 dark:text-gray-400'
  }
  // Consumed utilization warns before the full allowance is exhausted.
  if (props.utilization >= 90) {
    return 'text-red-600 dark:text-red-400'
  } else if (props.utilization >= 75) {
    return 'text-amber-600 dark:text-amber-400'
  } else {
    return 'text-gray-600 dark:text-gray-400'
  }
})

/**
 * barWidth bounds visual geometry to zero through one hundred percent; displayPercent
 * separately limits consumed percentage text to a readable maximum.
 */
const barWidth = computed(() => {
  // Bound geometry while the separate text can still report overuse.
  return `${Math.min(Math.max(props.utilization, 0), 100)}%`
})

const displayPercent = computed(() => {
  // Whole percentages keep table labels compact; consumed overuse retains its separate
  // high-value cap.
  const percent = Math.round(
    props.remainingCapacity ? Math.min(Math.max(props.utilization, 0), 100) : props.utilization,
  )
  return percent > 999 ? '>999%' : `${percent}%`
})

/**
 * Reset visibility and text distinguish an explicit deadline from an idle rolling window whose
 * caller requests immediate-availability wording.
 */
const shouldShowResetTime = computed(() => {
  if (props.resetsAt) return true
  return Boolean(props.showNowWhenIdle && props.utilization <= 0)
})

/**
 * formatResetTime uses the observed utilization to distinguish elapsed but unrefreshed quota
 * from immediate availability, then formats future deadlines in whole time units.
 */
const formatResetTime = computed(() => {
  // A caller-marked idle rolling window reports immediate availability from the zero
  // observation even when an older reset timestamp remains.
  if (props.showNowWhenIdle && props.utilization <= 0) {
    return t('usage.resetNow')
  }

  if (!props.resetsAt) return '-'

  const date = new Date(props.resetsAt)
  const diffMs = date.getTime() - now.value.getTime()

  // A past deadline with positive observed usage needs a refreshed observation; elapsed wall
  // time alone cannot establish renewed upstream capacity.
  if (diffMs <= 0) {
    return props.utilization > 0 ? t('usage.resetPending') : t('usage.resetNow')
  }

  // Countdown precision stops at whole minutes for future resets.
  const diffHours = Math.floor(diffMs / (1000 * 60 * 60))
  const diffMins = Math.floor((diffMs % (1000 * 60 * 60)) / (1000 * 60))

  if (diffHours >= 24) {
    const days = Math.floor(diffHours / 24)
    return `${days}d ${diffHours % 24}h`
  } else if (diffHours > 0) {
    return `${diffHours}h ${diffMins}m`
  } else {
    return `${diffMins}m`
  }
})
</script>
