<template>
  <!-- Numeric spans establish each badge baseline so capacity values align with neighboring labels when the badges wrap in mobile cards. -->
  <div :class="layoutClass">
    <div class="flex items-center gap-1">
      <span
        :class="[
          'inline-flex items-center gap-1 rounded-compact px-1.5 py-0.5 text-xs font-medium',
          capacityClass(concurrencyUsed, concurrencyMax),
        ]"
      >
        <Icon name="grid" size="md" class="h-2.5 w-2.5" />
        <span class="self-baseline font-mono">{{ concurrencyUsed }}</span>
        <span class="self-baseline text-gray-400 dark:text-gray-500">/</span>
        <span class="self-baseline font-mono">{{ concurrencyMax }}</span>
      </span>
    </div>

    <div v-if="sessionsMax > 0" class="flex items-center gap-1">
      <span
        :class="[
          'inline-flex items-center gap-1 rounded-compact px-1.5 py-0.5 text-xs font-medium',
          capacityClass(sessionsUsed, sessionsMax),
        ]"
      >
        <Icon name="users" size="md" class="h-2.5 w-2.5" />
        <span class="self-baseline font-mono">{{ sessionsUsed }}</span>
        <span class="self-baseline text-gray-400 dark:text-gray-500">/</span>
        <span class="self-baseline font-mono">{{ sessionsMax }}</span>
      </span>
    </div>

    <div v-if="rpmMax > 0" class="flex items-center gap-1">
      <span
        :class="[
          'inline-flex items-center gap-1 rounded-compact px-1.5 py-0.5 text-xs font-medium',
          capacityClass(rpmUsed, rpmMax),
        ]"
      >
        <Icon name="clock" size="md" class="h-2.5 w-2.5" />
        <span class="self-baseline font-mono">{{ rpmUsed }}</span>
        <span class="self-baseline text-gray-400 dark:text-gray-500">/</span>
        <span class="self-baseline font-mono">{{ rpmMax }}</span>
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed } from 'vue'

/**
 * GroupCapacityBadge.vue shows used and maximum concurrency, session, and request
 * counts for a group. Concurrency remains visible when its limit is zero, while
 * session and RPM badges require positive limits; layout can wrap horizontally in
 * marketplace options.
 */

/**
 * Props describes the caller supplied values consumed by GroupCapacityBadge.
 */
interface Props {
  layout?: 'vertical' | 'horizontal' /* badge arrangement direction */
  /**
   * concurrencyUsed and concurrencyMax remain visible for every maximum value.
   * sessionsUsed and sessionsMax appear together when sessionsMax is positive;
   * rpmUsed and rpmMax appear together when rpmMax is positive.
   */
  concurrencyUsed: number /* occupied concurrency slots */
  concurrencyMax: number /* maximum concurrency slots */
  sessionsUsed: number /* occupied session slots */
  sessionsMax: number /* maximum session slots */
  rpmUsed: number /* observed RPM usage */
  rpmMax: number /* requests per minute limit */
}

/**
 * props supplies used and maximum counters for each capacity dimension and chooses
 * stacked or wrapping badge layout.
 */
const props = withDefaults(defineProps<Props>(), {
  layout: 'vertical',
  concurrencyUsed: 0,
  concurrencyMax: 0,
  sessionsUsed: 0,
  sessionsMax: 0,
  rpmUsed: 0,
  rpmMax: 0,
})

/**
 * layoutClass selects wrapping horizontal badges for compact option rows or stacked
 * badges for group management cells.
 */
const layoutClass = computed(() =>
  props.layout === 'horizontal'
    ? 'flex flex-row flex-wrap items-center gap-1'
    : 'flex flex-col gap-1',
)

/**
 * capacityClass returns usage colors for one capacity counter. A positive exhausted
 * limit is red, other positive usage is yellow, and zero usage is neutral even when
 * the configured maximum is zero.
 */
function capacityClass(used: number, max: number): string {
  if (max > 0 && used >= max) {
    return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  }
  if (used > 0) {
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  }
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
}
</script>
