<template>
  <div class="card p-4">
    <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">
      {{ t('payment.admin.dailyRevenue') }}
    </h3>
    <div class="h-64">
      <ChartSkeleton v-if="loading" height="100%" />
      <Line v-else-if="chartData" :data="chartData" :options="chartOptions" />
      <div
        v-else
        class="flex h-full items-center justify-center text-sm text-gray-500 dark:text-gray-400"
      >
        {{ t('payment.admin.noData') }}
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import ChartSkeleton from '@/components/common/ChartSkeleton.vue'
import { computed, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  Legend,
  Filler,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import { externalTooltipHandler, hideExternalTooltip } from '@/utils/chartExternalTooltip'
import type { DailyPaymentStats } from '@/api/payment.types'

// DailyRevenueChart.vue renders daily payment revenue by currency together with a separate
// order count series. Every currency encountered in the supplied days receives a dataset
// using the same date labels, and a day lacking that currency contributes zero. Sorting
// currency identifiers keeps dataset ordering independent of object property order in
// individual daily results.
//
// Amounts share the left axis and counts use the right axis so order counts retain their
// own scale. The component returns no chart data for an empty series, allowing the
// template to show its empty state. Tooltips use the shared external renderer and are
// hidden when the component unmounts.
ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip, Legend, Filler)

// The external tooltip lives outside the canvas; hide it on unmount so a removed chart
// leaves no visible tooltip behind.
onBeforeUnmount(hideExternalTooltip)

// t resolves the locale labels used by payment controls, validation messages, and
// displayed order information.
const { t } = useI18n()

// props supplies parent-owned records and presentation state consumed by this component
// and its event handlers.
const props = defineProps<{
  data: DailyPaymentStats[] /* payment data for display */
  loading?: boolean /* parent data loading flag */
}>()

// colors pairs stroke and fill colors for currency revenue datasets; indices cycle when
// currencies outnumber these pairs.
const colors = [
  ['rgb(59, 130, 246)', 'rgba(59, 130, 246, 0.1)'],
  ['rgb(168, 85, 247)', 'rgba(168, 85, 247, 0.1)'],
  ['rgb(245, 158, 11)', 'rgba(245, 158, 11, 0.1)'],
  ['rgb(239, 68, 68)', 'rgba(239, 68, 68, 0.1)'],
]

// chartData derives aligned currency and order-count datasets from daily results; empty
// data selects the template empty state.
const chartData = computed(() => {
  if (!props.data || props.data.length === 0) return null
  const currencies = [...new Set(props.data.flatMap((day) => Object.keys(day.amount)))].sort()
  return {
    labels: props.data.map((d) => d.date),
    datasets: [
      ...currencies.map((currency, index) => {
        const [borderColor, backgroundColor] = colors[index % colors.length]
        return {
          label: `${currency} ${t('payment.admin.revenue')}`,
          // A currency absent on one day contributes zero at that date, keeping every
          // currency dataset aligned with the shared daily labels.
          data: props.data.map((day) => day.amount[currency] || 0),
          borderColor,
          backgroundColor,
          fill: true,
          tension: 0.3,
          pointRadius: 3,
          pointHoverRadius: 5,
        }
      }),
      {
        label: t('payment.admin.orderCount'),
        data: props.data.map((d) => d.count),
        borderColor: 'rgb(16, 185, 129)',
        backgroundColor: 'rgba(16, 185, 129, 0.1)',
        fill: false,
        tension: 0.3,
        pointRadius: 3,
        pointHoverRadius: 5,
        yAxisID: 'y1',
      },
    ],
  }
})

// chartOptions separates revenue and count axes and delegates tooltips to the shared
// external renderer for dashboard presentation.
const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  interaction: { mode: 'index' as const, intersect: false },
  scales: {
    y: {
      type: 'linear' as const,
      display: true,
      position: 'left' as const,
      title: { display: true, text: t('payment.admin.revenue') },
    },
    y1: {
      type: 'linear' as const,
      display: true,
      position: 'right' as const,
      title: { display: true, text: t('payment.admin.orderCount') },
      grid: { drawOnChartArea: false },
    },
  },
  plugins: {
    legend: { position: 'top' as const },
    tooltip: {
      enabled: false,
      external: externalTooltipHandler,
    },
  },
}
</script>
