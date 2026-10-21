<template>
  <div class="flex w-full min-w-0 items-center gap-2" :title="tooltip">
    <div
      class="grid h-8 min-w-0 flex-1 items-center overflow-hidden"
      :style="barGridStyle"
      role="img"
      :aria-label="ariaLabel"
    >
      <span
        v-for="(bucket, index) in normalizedBuckets"
        :key="`${bucket.date || 'empty'}-${index}`"
        :class="[
          'h-6 max-w-full justify-self-center rounded-compact',
          bucketClass(bucket.availability_rate, bucket.total_count),
        ]"
        :style="{ width: bucketWidth }"
      />
    </div>
    <div class="w-[96px] shrink-0 text-left">
      <div class="text-base font-semibold leading-5 text-gray-900 dark:text-white">
        {{ rateLabel }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type {
  MarketplaceGroupAvailability,
  MarketplaceGroupAvailabilityDay,
} from '@/api/marketplace.types'

// GroupAvailabilityBar.vue renders a group's reported availability over a fixed
// number of time buckets. Missing earlier buckets appear as empty cells, and
// longer histories retain their newest entries. Summary labels distinguish
// unavailable measurements from a measured zero success rate.

// props accepts the optional server aggregate; missing data still produces the
// default window and empty cells so neighboring groups retain comparable geometry.
const props = defineProps<{
  // Server aggregate and buckets; null or omission uses an empty seven-day window.
  availability?: MarketplaceGroupAvailability | null
}>()

// Localized labels describe the displayed measurement window and distinguish
// empty history from a numerical rate in both visible and accessible text.
const { t } = useI18n()

// Window and bucket durations determine the target cell count; each duration is
// clamped to one or more before converting the day window into minutes.
const windowDays = computed(() => Math.max(props.availability?.window_days ?? 7, 1))
const bucketMinutes = computed(() => Math.max(props.availability?.bucket_minutes ?? 24 * 60, 1))
const targetBucketCount = computed(() =>
  Math.max(Math.ceil((windowDays.value * 24 * 60) / bucketMinutes.value), 1),
)

// normalizedBuckets keeps the newest target-sized suffix, padding missing older
// observations on the left without changing the supplied server bucket objects.
const normalizedBuckets = computed<MarketplaceGroupAvailabilityDay[]>(() => {
  const buckets = props.availability?.days ?? []
  const target = targetBucketCount.value
  if (buckets.length >= target) {
    return buckets.slice(buckets.length - target)
  }
  return [
    ...Array.from({ length: target - buckets.length }, () => ({
      date: '',
      success_count: 0,
      total_count: 0,
      availability_rate: null,
    })),
    ...buckets,
  ]
})

// Grid gaps and cell widths become smaller as bucket density grows, allowing
// the history to share the available row width with its fixed summary label.
const barGridStyle = computed(() => ({
  gap:
    normalizedBuckets.value.length > 360
      ? '0'
      : normalizedBuckets.value.length > 180
        ? '1px'
        : '2px',
  gridTemplateColumns: `repeat(${normalizedBuckets.value.length}, minmax(0, 1fr))`,
}))

const bucketWidth = computed(() => {
  const count = normalizedBuckets.value.length
  if (count <= 30) {
    return '8px'
  }
  if (count <= 90) {
    return '5px'
  }
  if (count <= 180) {
    return '4px'
  }
  return '100%'
})

// Summary labels use the aggregate rate rather than averaging the visible cells;
// inaccessible measurements receive the translated no-data wording and window hint.
const rateLabel = computed(() => {
  const rate = props.availability?.availability_rate
  if (typeof rate !== 'number') {
    return t('marketplace.availabilityNoData')
  }
  return `${(rate * 100).toFixed(2)}%`
})

const tooltip = computed(() => {
  const availability = props.availability
  if (!availability || typeof availability.availability_rate !== 'number') {
    return t('marketplace.availabilityHintNoData', {
      days: windowDays.value,
    })
  }
  return t('marketplace.availabilityHint', {
    days: windowDays.value,
    rate: rateLabel.value,
    success: availability.success_count,
    total: availability.total_count,
  })
})

const ariaLabel = computed(
  () => `${t('marketplace.availabilityWindow', { days: windowDays.value })}: ${rateLabel.value}`,
)

// bucketClass maps measured success rates to the shared availability color bands.
// A zero or missing request count, or a nonnumeric rate, uses the empty color.
// Numeric rates otherwise use the existing inclusive threshold comparisons.
function bucketClass(rate?: number | null, totalCount?: number): string {
  if (!totalCount || typeof rate !== 'number') {
    return 'bg-gray-200 dark:bg-dark-700'
  }
  if (rate >= 0.995) {
    return 'bg-emerald-500'
  }
  if (rate >= 0.98) {
    return 'bg-lime-500'
  }
  if (rate >= 0.9) {
    return 'bg-amber-400'
  }
  return 'bg-rose-400'
}
</script>
