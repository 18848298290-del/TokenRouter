<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import BalanceIcon from '@/components/common/BalanceIcon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'

/**
 * QuotaBadge.vue displays a local spending quota as used and limit amounts formatted in USD. A
 * caller may supply a compact period label or use the balance icon. Badge color distinguishes
 * normal usage, the warning threshold, and an exhausted quota.
 */

/**
 * props supplies quota amounts and an optional period label; both amounts share the USD
 * formatter used by the badge.
 */
const props = defineProps<{
  used: number // consumed quota in USD
  limit: number // configured quota in USD
  /**
   * Optional text replaces the balance icon, allowing callers to distinguish daily and weekly
   * quota badges.
   */
  label?: string
}>()

/**
 * Translation and the USD formatter provide quota status hints and compact two-decimal amount
 * labels for this badge.
 */
const { t } = useI18n()
const { formatUsdAmount } = useBalanceDisplay()

/**
 * badgeClass and tooltip classify quota exhaustion, with a yellow warning once usage reaches
 * eighty percent of the supplied limit.
 */
const badgeClass = computed(() => {
  if (props.used >= props.limit) {
    return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-400'
  }
  // Warn before exhaustion at eighty percent usage.
  if (props.used >= props.limit * 0.8) {
    return 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900/30 dark:text-yellow-400'
  }
  return 'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400'
})

const tooltip = computed(() => {
  if (props.used >= props.limit) {
    return t('admin.providers.capacity.quota.exceeded')
  }
  return t('admin.providers.capacity.quota.normal')
})

/**
 * fmt formats a quota amount in USD with two fractional digits.
 */
const fmt = (v: number) => formatUsdAmount(v, { fractionDigits: 2 })
</script>

<template>
  <span
    :class="[
      'inline-flex items-center gap-1 rounded-compact px-1.5 py-px text-xs font-medium leading-tight',
      badgeClass,
    ]"
    :title="tooltip"
  >
    <span v-if="label" class="font-semibold opacity-70">{{ label }}</span>
    <BalanceIcon v-else svg="" size="xs" />
    <span class="font-mono">{{ fmt(used) }}</span>
    <span class="text-gray-400 dark:text-gray-500">/</span>
    <span class="font-mono">{{ fmt(limit) }}</span>
  </span>
</template>
