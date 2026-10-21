<template>
  <div class="flex items-center">
    <span
      :class="[
        'inline-flex items-center gap-1 rounded-compact px-2 py-0.5 text-xs font-medium',
        statusClass,
      ]"
    >
      <!-- Four-square grid icon -->
      <Icon name="grid" size="xs" class="h-3 w-3" />
      <!-- The numeric baseline aligns this badge with labels in the mobile user cards. -->
      <span class="self-baseline font-mono">{{ current }}</span>
      <span class="self-baseline text-gray-400 dark:text-gray-500">/</span>
      <span class="self-baseline font-mono">{{ max }}</span>
    </span>
  </div>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
import { computed } from 'vue'

// UserConcurrencyCell.vue displays a user's current concurrency against its configured
// limit. Positive limits that are reached use the full-state color, other positive
// usage uses the active color, and zero usage uses the idle color. A nonpositive
// maximum skips the full-state branch while remaining visible as its numeric value.
//
// The cell combines the observed count with the configured limit rather than treating
// positive usage as saturation by itself. Its template displays both numeric inputs as
// supplied, and the computed classes keep this classification synchronized with prop
// updates. Baseline alignment lets the same badge sit beside labels in mobile cards.

// props supplies the observed current concurrency and configured maximum used together
// to render the table cell's count and status color.
const props = defineProps<{
  current: number // Observed concurrent requests.
  // max is displayed as supplied; nonpositive values skip the full-state color even
  // when current usage is positive.
  max: number
}>()

// statusClass derives the badge colors from observed usage and the configured maximum.
// A reached positive maximum selects the full-state colors, while positive usage below
// that limit or with a nonpositive maximum selects the active colors. Zero usage
// selects the idle colors. Returning both light and dark classes keeps the same
// concurrency classification visible as the application changes its configured theme.
const statusClass = computed(() => {
  const { current, max } = props

  if (current >= max && max > 0) {
    return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  }

  if (current > 0) {
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  }

  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
})
</script>
