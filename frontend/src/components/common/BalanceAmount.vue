<template>
  <span class="inline-flex items-center gap-1 whitespace-nowrap tabular-nums">
    <BalanceIcon v-if="hasCustomBalanceIcon" :size="iconSize" aria-hidden="true" />
    <span>{{ formattedAmount }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import BalanceIcon from '@/components/common/BalanceIcon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'

/**
 * BalanceAmount.vue renders an account amount with the configured currency symbol or
 * balance icon. Formatting comes from useBalanceDisplay so callers share decimal
 * precision, grouping, and the decision to omit the text symbol when a custom icon is
 * present.
 */

/**
 * props controls the source amount, decimal precision, grouping, and icon size
 * forwarded to the balance presentation helpers.
 */
const props = withDefaults(
  defineProps<{
    /**
     * amount is formatted through useBalanceDisplay; missing or nonfinite values
     * use its two-decimal zero placeholder, including when a custom icon is shown.
     */
    amount: number | null | undefined
    fractionDigits?: number /* decimal fraction digits */
    iconSize?: 'xs' | 'sm' | 'md' | 'lg' | 'xl' /* balance icon size */
    useGrouping?: boolean /* numeric grouping preference */
  }>(),
  {
    fractionDigits: 2,
    iconSize: 'sm',
    useGrouping: true,
  },
)

/**
 * The balance display composable supplies configuration and formatting together so the
 * icon and amount use the same unit settings.
 */
const { hasCustomBalanceIcon, formatBalanceAmount } = useBalanceDisplay()

/**
 * formattedAmount pairs the numeric text with the configured icon, suppressing the
 * text symbol when that icon supplies the unit.
 */
const formattedAmount = computed(() =>
  formatBalanceAmount(props.amount, {
    fractionDigits: props.fractionDigits,
    useGrouping: props.useGrouping,
    withSymbol: !hasCustomBalanceIcon.value,
  }),
)
</script>
