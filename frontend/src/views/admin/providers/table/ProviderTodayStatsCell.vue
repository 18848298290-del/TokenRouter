<template>
  <div>
    <!-- Loading state -->
    <div v-if="props.loading && !props.stats" class="space-y-0.5">
      <div class="skeleton h-3 w-12"></div>
      <div class="skeleton h-3 w-16"></div>
      <div class="skeleton h-3 w-10"></div>
    </div>

    <!-- Error state -->
    <div v-else-if="props.error && !props.stats" class="text-xs text-red-500">
      {{ props.error }}
    </div>

    <!-- Stats data -->
    <div v-else-if="props.stats" class="space-y-0.5 text-xs">
      <!-- Requests -->
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400"
          >{{ t('admin.providers.stats.requests') }}:</span
        >
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          formatNumber(props.stats.requests)
        }}</span>
      </div>
      <!-- Tokens -->
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400"
          >{{ t('admin.providers.stats.tokens') }}:</span
        >
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          formatTokens(props.stats.tokens)
        }}</span>
      </div>
      <!-- Cost (Provider) -->
      <div class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('usage.providerBilled') }}:</span>
        <span class="font-medium text-emerald-600 dark:text-emerald-400">{{
          formatUsdAmount(props.stats.cost, { fractionDigits: 2 })
        }}</span>
      </div>
      <!-- User charges use the configured balance display unit. -->
      <div v-if="props.stats.user_cost != null" class="flex items-center gap-1">
        <span class="text-gray-500 dark:text-gray-400">{{ t('usage.userBilled') }}:</span>
        <span class="font-medium text-gray-700 dark:text-gray-300">{{
          formatBalanceAmount(props.stats.user_cost, { fractionDigits: 2 })
        }}</span>
      </div>
    </div>

    <!-- No data -->
    <div v-else class="text-xs text-gray-400">-</div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import type { WindowStats } from '@/api/admin/providers.types'
import { formatNumber } from '@/utils/format'

/**
 * ProviderTodayStatsCell.vue displays today's provider request, token, and cost observations.
 * Existing statistics remain visible during loading or errors, and user charges appear when the
 * optional observation is present, including zero. Provider cost uses USD formatting while user
 * charges use the configured balance display.
 */

/**
 * props supplies statistics and request feedback; the template prefers available observations
 * over loading or error placeholders.
 */
const props = withDefaults(
  defineProps<{
    /**
     * Optional observations remain visible during refresh; null lets the template show loading,
     * failure, or missing-data feedback.
     */
    stats?: WindowStats | null
    loading?: boolean // parent statistics request activity
    error?: string | null // optional statistics request error
  }>(),
  {
    stats: null,
    loading: false,
    error: null,
  },
)

/**
 * Translation and balance formatters distinguish provider USD cost from user charges rendered
 * in the configured balance unit.
 */
const { t } = useI18n()
const { formatBalanceAmount, formatUsdAmount } = useBalanceDisplay()

/**
 * formatTokens abbreviates local token counts using one decimal for thousands and two for
 * millions. Counts below one thousand remain integers, matching the compact precision of this
 * table cell.
 */
const formatTokens = (tokens: number): string => {
  // Million and thousand suffixes use different compact table precisions.
  if (tokens >= 1000000) {
    return `${(tokens / 1000000).toFixed(2)}M`
  } else if (tokens >= 1000) {
    return `${(tokens / 1000).toFixed(1)}K`
  }
  return tokens.toString()
}
</script>
