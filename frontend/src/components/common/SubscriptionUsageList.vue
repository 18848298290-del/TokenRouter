<template>
  <div>
    <div v-for="subscription in sortedSubscriptions" :key="subscription.id" :class="itemClass">
      <div class="mb-2 flex items-center justify-between gap-2">
        <span class="min-w-0 truncate text-sm font-medium text-gray-900 dark:text-white">
          {{ subscription.plan?.name || `Plan #${subscription.plan_id}` }}
        </span>
        <span
          class="shrink-0 whitespace-nowrap text-xs"
          :class="getDaysRemainingClass(subscription.expires_at)"
        >
          {{ formatExpiration(subscription.expires_at) }}
        </span>
      </div>

      <div
        v-if="isUnlimited(subscription)"
        class="flex items-center gap-2 rounded-control bg-gradient-to-r from-emerald-50 to-teal-50 px-2.5 py-1.5 dark:from-emerald-900/20 dark:to-teal-900/20"
      >
        <span class="text-lg text-emerald-600 dark:text-emerald-400">∞</span>
        <span class="text-xs font-medium text-emerald-700 dark:text-emerald-300">
          {{ t('subscriptionProgress.unlimited') }}
        </span>
      </div>

      <!-- Each quota period shares the same three grid columns, letting the widest label and amount align every progress bar in the remaining space. -->
      <div v-else class="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-2 gap-y-1.5">
        <template v-for="window in usageWindows(subscription)" :key="window.key">
          <span class="whitespace-nowrap text-xs text-gray-500">
            {{ window.label }}
          </span>
          <div class="h-1.5 rounded-full bg-gray-200 dark:bg-dark-600">
            <div
              class="h-1.5 rounded-full transition-[width,background-color]"
              :class="getProgressBarClass(window.used, window.limit)"
              :style="{ width: getProgressWidth(window.used, window.limit) }"
            />
          </div>
          <span class="whitespace-nowrap text-right text-xs text-gray-500">
            {{ formatUsage(window.used, window.limit) }}
          </span>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useSubscriptionUsage } from '@/composables/useSubscriptionUsage'
import type { UserSubscription } from '@/api/subscriptions.types'

/**
 * SubscriptionUsageList.vue renders subscriptions in usage order with their expiration
 * and quota windows. Unlimited plans receive a dedicated label, while limited plans
 * share aligned label, progress, and amount columns; callers supply row spacing for
 * popovers or page cards.
 */

/**
 * props supplies subscriptions and caller owned row classes so the same usage rows fit
 * header popovers and page cards.
 */
const props = withDefaults(
  defineProps<{
    subscriptions: UserSubscription[] /* subscription usage records */

    /**
     * itemClass belongs to the caller and supplies row spacing and separators for
     * either a popover or a page card.
     */
    itemClass?: string
  }>(),
  {
    itemClass: '',
  },
)

/**
 * The i18n translator resolves this component's message keys using the current
 * application locale and its configured translation catalog.
 */
const { t } = useI18n()
/**
 * The subscription usage composable supplies quota windows, progress formatting, usage
 * ordering, and expiration labels shared by every rendered subscription row.
 */
const {
  usageWindows,
  isUnlimited,
  sortByUsage,
  getProgressBarClass,
  getProgressWidth,
  formatUsage,
  formatExpiration,
  getDaysRemainingClass,
} = useSubscriptionUsage()

/**
 * sortedSubscriptions uses the shared usage ordering so the most constrained plans
 * appear first in the displayed list.
 */
const sortedSubscriptions = computed(() => sortByUsage(props.subscriptions))
</script>
