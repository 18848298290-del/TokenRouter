<template>
  <div class="card p-4">
    <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">
      {{ t('payment.admin.purchaseDistribution') }}
    </h3>
    <div
      v-if="!items?.length"
      class="flex h-48 items-center justify-center text-sm text-gray-500 dark:text-gray-400"
    >
      {{ t('payment.admin.noData') }}
    </div>
    <div v-else class="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(260px,0.9fr)]">
      <div class="min-w-0">
        <p class="mb-3 text-center text-xs font-medium text-gray-500 dark:text-gray-400">
          {{ t('payment.admin.amountShare') }}
        </p>
        <div class="mx-auto h-52 max-w-52">
          <Doughnut :data="amountChartData" :options="amountChartOptions" />
        </div>
      </div>
      <div class="min-w-0">
        <p class="mb-3 text-center text-xs font-medium text-gray-500 dark:text-gray-400">
          {{ t('payment.admin.countShare') }}
        </p>
        <div class="mx-auto h-52 max-w-52">
          <Doughnut :data="countChartData" :options="countChartOptions" />
        </div>
      </div>
      <div class="max-h-64 min-w-0 overflow-y-auto">
        <table class="w-full text-xs">
          <thead>
            <tr class="text-gray-500 dark:text-gray-400">
              <th class="pb-2 text-left">{{ t('payment.admin.purchaseItem') }}</th>
              <th class="pb-2 text-right">{{ t('payment.admin.revenue') }}</th>
              <th class="pb-2 text-right">{{ t('payment.admin.orderCount') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(item, index) in sortedItems"
              :key="distributionKey(item, index)"
              class="border-t border-gray-100 dark:border-dark-600"
            >
              <td class="py-2">
                <div class="flex min-w-0 items-center gap-2">
                  <span
                    class="h-2.5 w-2.5 shrink-0 rounded-full"
                    :style="{ backgroundColor: chartColors[index % chartColors.length] }"
                  ></span>
                  <span
                    class="truncate font-medium text-gray-900 dark:text-white"
                    :title="displayLabel(item)"
                  >
                    {{ displayLabel(item) }}
                  </span>
                </div>
              </td>
              <td class="py-2 text-right font-medium text-gray-900 dark:text-white">
                ${{ formatMoney(item.amount) }}
              </td>
              <td class="py-2 text-right text-gray-600 dark:text-gray-400">
                {{ item.count.toLocaleString() }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { Chart as ChartJS, ArcElement, Tooltip, Legend, type TooltipItem } from 'chart.js'
import { Doughnut } from 'vue-chartjs'
import { externalTooltipHandler, hideExternalTooltip } from '@/utils/chartExternalTooltip'
import { CHART_PALETTE } from '@/composables/useChartTheme'
import type { DashboardStats } from '@/api/payment.types'

// PurchaseDistributionChart.vue displays payment purchase distribution as amount and count
// doughnuts alongside detail rows. Both charts derive labels, colors, and dataset
// positions from the same sorted item sequence. Amount determines ordering first, with
// count resolving equal amounts, so a row's color identifies the same purchase category in
// either chart.
//
// Chart tooltip percentages use the total of the active dataset, which makes amount shares
// and purchase-count shares distinct. Balance purchases receive a translated label even
// when the backend label is a technical identifier. Empty items select the template's
// empty state, and the shared external tooltip is hidden when the component unmounts.
ChartJS.register(ArcElement, Tooltip, Legend)

// The shared tooltip is rendered outside the chart canvas; hide it on unmount so page
// navigation removes any visible chart tooltip.
onBeforeUnmount(hideExternalTooltip)

// t resolves the locale labels used by payment controls, validation messages, and
// displayed order information.
const { t } = useI18n()

// PurchaseDistributionItem names one purchase-category entry returned by the payment
// dashboard API.
type PurchaseDistributionItem = DashboardStats['purchase_distribution'][number]

// props supplies parent-owned records and presentation state consumed by this component
// and its event handlers.
const props = defineProps<{
  items: PurchaseDistributionItem[] /* ordered display item collection */
}>()

// chartColors shares the dashboard palette across amount charts, count charts, and detail
// row markers for each sorted category.
const chartColors = CHART_PALETTE

// sortedItems sorts a copied array by amount and then count so parent data remains
// unchanged during chart preparation.
const sortedItems = computed(() => {
  return [...(props.items || [])].sort((a, b) => {
    if (b.amount === a.amount) return b.count - a.count
    return b.amount - a.amount
  })
})

// amountChartData assigns purchase amounts to the shared sorted category labels and colors
// used by the count chart.
const amountChartData = computed(() => ({
  labels: sortedItems.value.map(displayLabel),
  datasets: [
    {
      data: sortedItems.value.map((item) => item.amount),
      backgroundColor: chartColors.slice(0, sortedItems.value.length),
      borderWidth: 0,
    },
  ],
}))

// countChartData assigns purchase counts to the shared sorted category labels and colors
// used by the amount chart.
const countChartData = computed(() => ({
  labels: sortedItems.value.map(displayLabel),
  datasets: [
    {
      data: sortedItems.value.map((item) => item.count),
      backgroundColor: chartColors.slice(0, sortedItems.value.length),
      borderWidth: 0,
    },
  ],
}))

// amountChartOptions formats tooltip values as dollar amounts while the shared chart
// factory computes this dataset percentage.
const amountChartOptions = computed(() => makeChartOptions((value) => `$${formatMoney(value)}`))
// countChartOptions formats tooltip values as localized counts while the shared chart
// factory computes this dataset percentage.
const countChartOptions = computed(() => makeChartOptions((value) => value.toLocaleString()))

// makeChartOptions configures doughnut tooltips using the supplied value formatter.
// Percentages are calculated against the current dataset total and show zero when that
// total is not positive.
function makeChartOptions(formatValue: (value: number) => string) {
  return {
    responsive: true,
    maintainAspectRatio: false,
    plugins: {
      legend: {
        display: false,
      },
      tooltip: {
        enabled: false,
        external: externalTooltipHandler,
        callbacks: {
          label: (context: TooltipItem<'doughnut'>) => {
            const value = Number(context.raw || 0)
            const total = context.dataset.data.reduce((sum, item) => sum + Number(item || 0), 0)
            // Use the active dataset total for this share and show zero for an empty
            // total, preventing an undefined tooltip percentage.
            const percentage = total > 0 ? ((value / total) * 100).toFixed(1) : '0.0'
            return `${context.label}: ${formatValue(value)} (${percentage}%)`
          },
        },
      },
    },
  }
}

// displayLabel returns a localized name for a purchase distribution row. Balance rows use
// the pay-as-you-go label, while unnamed subscription rows use the generic subscription
// label for display.
function displayLabel(item: PurchaseDistributionItem): string {
  // The API uses a technical balance category label; translate that category here so
  // detail rows and chart labels share user-readable text.
  if (item.type === 'balance') return t('payment.admin.payAsYouGo')
  return item.label || t('payment.admin.subscriptionOrder')
}

// distributionKey combines purchase kind with plan identity for repeated detail rows. A
// missing plan identifier uses its label, then its index, so unlabeled entries still
// receive a key.
function distributionKey(item: PurchaseDistributionItem, index: number): string {
  return `${item.type}-${item.plan_id || item.label || index}`
}

// formatMoney renders a purchase amount with two decimal places for chart details and
// tooltips. Falsy inputs, including NaN, use zero before numeric conversion; the returned
// text has no currency symbol.
function formatMoney(value: number): string {
  // Displayed purchase amounts use two decimal places for the chart details.
  return Number(value || 0).toFixed(2)
}
</script>
