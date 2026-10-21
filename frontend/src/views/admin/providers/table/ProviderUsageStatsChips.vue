<template>
  <div class="mb-0.5 flex items-center">
    <div class="flex flex-wrap items-center justify-end gap-1.5 text-xs lg:justify-start">
      <span
        v-for="chip in chips"
        :key="chip.key"
        :data-stat="chip.key"
        :title="chip.hint"
        class="whitespace-nowrap rounded-compact bg-gray-100 px-1.5 py-0.5 dark:bg-dark-700"
      >
        <span class="text-gray-400 dark:text-gray-500">{{ chip.label }}</span
        >{{ ' '
        }}<span class="tabular-nums text-gray-600 dark:text-gray-300">{{ chip.value }}</span>
      </span>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatCompactNumber } from '@/utils/format'

/**
 * ProviderUsageStatsChips.vue renders request counts, token counts, provider cost, and optional
 * user charges for a usage period. Period selection changes the hover explanations. Provider
 * cost uses USD formatting, while user charges use the configured balance unit and remain
 * visible when measured as zero.
 */

/**
 * props supplies observed metrics and their period label, allowing the same chips to describe
 * today or a usage window.
 */
const props = defineProps<{
  stats: {
    requests: number // observed request count
    tokens: number // observed token count
    cost: number // provider cost in USD
    /**
     * When present, this user-charge observation is rendered in the configured balance unit,
     * including measured zero.
     */
    user_cost?: number | null
  } // observed usage metric values

  /**
   * Selects the period wording in hover hints; the supplied statistics retain their original
   * values.
   */
  scope: 'today' | 'window'
}>()

/**
 * Translation and currency formatters label compact counts while keeping provider USD cost
 * distinct from configured user balance charges.
 */
const { t } = useI18n()
const { formatBalanceAmount, formatUsdAmount } = useBalanceDisplay()

/**
 * chips builds the visible metrics in request, token, and provider-cost order, appending user
 * charges only when that observation exists.
 */
const chips = computed(() => {
  const period = t(`admin.providers.usageStats.period.${props.scope}`)
  /**
   * hint resolves the metric explanation with the currently selected usage period.
   */
  const hint = (key: string) => t(`admin.providers.usageStats.hints.${key}`, { period })
  const items = [
    {
      key: 'requests',
      label: t('admin.providers.usageStats.requests'),
      value: formatCompactNumber(props.stats.requests, { allowBillions: false }),
      hint: hint('requests'),
    },
    {
      key: 'tokens',
      label: t('admin.providers.usageStats.tokens'),
      value: formatCompactNumber(props.stats.tokens),
      hint: hint('tokens'),
    },
    {
      key: 'cost',
      label: t('admin.providers.usageStats.cost'),
      value: formatUsdAmount(props.stats.cost, { fractionDigits: 2 }),
      hint: hint('cost'),
    },
  ]
  // A present zero is still an observed user charge; omit the chip only when older data lacks
  // that observation.
  if (props.stats.user_cost != null) {
    items.push({
      key: 'userCost',
      label: t('admin.providers.usageStats.userCost'),
      value: formatBalanceAmount(props.stats.user_cost, { fractionDigits: 2 }),
      hint: hint('userCost'),
    })
  }
  return items
})
</script>
