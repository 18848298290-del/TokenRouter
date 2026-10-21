<template>
  <div v-if="usage" class="text-sm">
    <div class="flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.users.today') }}:</span>
      <span class="font-medium text-gray-900 dark:text-white">{{
        formatBalanceAmount(usage.today_actual_cost, { fractionDigits: 4 })
      }}</span>
    </div>
    <div class="mt-0.5 flex items-center gap-1.5">
      <span class="text-gray-500 dark:text-gray-400">{{ t('admin.users.total') }}:</span>
      <span class="font-medium text-gray-900 dark:text-white">{{
        formatBalanceAmount(usage.total_actual_cost, { fractionDigits: 4 })
      }}</span>
    </div>
  </div>
  <span v-else class="text-sm text-gray-400 dark:text-gray-500">—</span>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { PlatformUsage } from '@/api/admin/dashboard'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'

// PlatformCostCell.vue renders a platform-specific cost summary inside a user table cell.
// The optional usage record supplies today and lifetime charges; missing records render a
// placeholder, and populated costs use the configured balance unit.

// The parent supplies one optional platform cost record; the template keeps its missing-
// record presentation separate from measured zero charges.
defineProps<{
  // usage supplies one platform's daily and lifetime costs; omission selects the empty cell
  // presentation.
  usage?: PlatformUsage
}>()

// Translation and balance formatting supply the daily and lifetime charge labels and
// amounts displayed inside the cell.
const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()
</script>
