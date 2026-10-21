<script setup lang="ts">
/**
 * CapacityBadge.vue renders a compact current-versus-maximum capacity badge with optional
 * suffix and hover text. The caller supplies formatted amounts and the color class, so this
 * display component does not infer units, utilization thresholds, or provider eligibility.
 */
defineProps<{
  colorClass: string // caller-selected badge color classes
  tooltip?: string // optional badge hover explanation
  current: string | number // formatted current capacity value
  max: string | number // formatted maximum capacity value
  suffix?: string // optional trailing unit text
}>()
</script>

<template>
  <span
    :class="[
      'inline-flex items-center gap-1 rounded-compact px-1.5 py-px text-xs font-medium leading-tight',
      colorClass,
    ]"
    :title="tooltip"
  >
    <slot />
    <!-- Numeric spans establish the badge baseline so mobile-card labels align with the amount rather than the surrounding padded badge box. -->
    <span class="self-baseline font-mono">{{ current }}</span>
    <span class="self-baseline text-gray-400 dark:text-gray-500">/</span>
    <span class="self-baseline font-mono">{{ max }}</span>
    <span v-if="suffix" class="self-baseline text-xs opacity-60">{{ suffix }}</span>
  </span>
</template>
