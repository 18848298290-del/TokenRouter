<template>
  <div class="card p-4">
    <h3 class="mb-4 text-sm font-semibold text-gray-900 dark:text-white">
      {{ t('admin.dashboard.tokenUsageTrend') }}
    </h3>
    <ChartSkeleton v-if="loading" variant="plot" />
    <div v-else-if="trendData.length > 0 && chartData" class="h-48">
      <Line :data="chartData" :options="lineOptions" />
    </div>
    <div
      v-else
      class="flex h-48 items-center justify-center text-sm text-gray-500 dark:text-gray-400"
    >
      {{ t('admin.dashboard.noDataAvailable') }}
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import ChartSkeleton from '@/components/common/ChartSkeleton.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import {
  useChartTheme,
  CHART_SERIES_COLORS,
  CHART_TICK_FONT_SIZE,
  CHART_LEGEND_FONT_SIZE,
} from '@/composables/useChartTheme'
import { externalTooltipHandler, hideExternalTooltip } from '@/utils/chartExternalTooltip'
import type { TrendDataPoint } from '@/api/usage.types'
import { formatTokens } from '@/utils/format'

// TokenUsageTrend.vue plots token categories and cache-read percentages across supplied
// time buckets. The percentage uses input, cache creation, and cache read together as
// the prompt-side total, while output tokens remain a separate series. A zero prompt
// total produces zero percent so empty buckets stay displayable.
//
// The chart abbreviates date labels according to granularity but retains full source
// dates for tooltip titles. Dataset positions stay aligned with the supplied trend
// array, allowing tooltip footers to retrieve the matching bucket's actual and optional
// standard cost. Theme text and grid colors update independently of the semantic
// token-series palette.
ChartJS.register(
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler,
)

// Hide the shared HTML tooltip before this chart unmounts so its bucket details cannot
// remain above a subsequent page.
onBeforeUnmount(hideExternalTooltip)

// Translations and balance formatters provide the chart title and tooltip costs using
// the active language and the configured currency display.
const { t } = useI18n()
const { formatBalanceAmount, formatUsdAmount } = useBalanceDisplay()

// props supplies ordered trend buckets and display choices; daily labels and
// standard-cost tooltip details are enabled by the component defaults.
const props = withDefaults(
  defineProps<{
    trendData: TrendDataPoint[] // Ordered usage buckets
    loading?: boolean // Parent loading state
    // Selects compact daily or hourly axis labels; full bucket dates remain available in
    // tooltip titles.
    granularity?: 'day' | 'hour'
    // Includes standard cost in the tooltip footer by default; disabling it leaves the
    // actual charge visible.
    showStandardCost?: boolean
  }>(),
  {
    granularity: 'day',
    showStandardCost: true,
  },
)

// Theme colors govern text and grid lines, while the shared semantic palette keeps token
// categories recognizable across theme changes.
const { colors: themeColors } = useChartTheme()
const chartColors = computed(() => ({
  text: themeColors.value.text,
  grid: themeColors.value.grid,
  ...CHART_SERIES_COLORS,
}))

// formatHourLabel extracts hours and minutes for compact hourly axis labels. Inputs
// lacking the expected time separator retain their supplied text, while tooltip titles
// independently read the complete bucket date from trendData.
const formatHourLabel = (value: string): string => {
  const match = value.match(/(?:T|\s)(\d{2}:\d{2})/)
  return match?.[1] || value
}

// formatDayLabel extracts a padded month and day for daily axis labels. Inputs that do
// not match the supported numeric date prefix retain their text; tooltip titles still
// use the complete date from the matching bucket.
const formatDayLabel = (value: string): string => {
  const match = value.match(/^(?:\d{4}[-/])?(\d{1,2})[-/](\d{1,2})/)
  if (!match) return value
  return `${match[1].padStart(2, '0')}-${match[2].padStart(2, '0')}`
}

// chartLabels applies the selected abbreviation to every supplied bucket in order,
// keeping labels aligned with dataset values and tooltip indices.
const chartLabels = computed(() =>
  props.trendData.map((point) =>
    props.granularity === 'hour' ? formatHourLabel(point.date) : formatDayLabel(point.date),
  ),
)

// chartData builds token counts and a percentage series from the supplied buckets. An
// empty list returns null for the template empty state. Cache-read percentage uses the
// complete prompt-side token count, adding cache creation and cache read to ordinary
// input tokens.
//
// Each dataset maps the same bucket order, while the percentage dataset selects a
// separate right axis capped at one hundred. Zero prompt-side totals yield zero percent,
// preventing division by zero and keeping empty time buckets aligned with the other
// series.
const chartData = computed(() => {
  if (!props.trendData?.length) return null

  return {
    labels: chartLabels.value,
    datasets: [
      {
        label: 'Input',
        data: props.trendData.map((d) => d.input_tokens),
        borderColor: chartColors.value.input,
        backgroundColor: `${chartColors.value.input}20`,
        fill: true,
        tension: 0.3,
      },
      {
        label: 'Output',
        data: props.trendData.map((d) => d.output_tokens),
        borderColor: chartColors.value.output,
        backgroundColor: `${chartColors.value.output}20`,
        fill: true,
        tension: 0.3,
      },
      {
        label: 'Cache Creation',
        data: props.trendData.map((d) => d.cache_creation_tokens),
        borderColor: chartColors.value.cacheCreation,
        backgroundColor: `${chartColors.value.cacheCreation}20`,
        fill: true,
        tension: 0.3,
      },
      {
        label: 'Cache Read',
        data: props.trendData.map((d) => d.cache_read_tokens),
        borderColor: chartColors.value.cacheRead,
        backgroundColor: `${chartColors.value.cacheRead}20`,
        fill: true,
        tension: 0.3,
      },
      {
        label: 'Cached Input %',
        data: props.trendData.map((d) => {
          // Input, cache creation, and cache read represent distinct prompt categories;
          // include all three so cache hits use the full prompt denominator.
          const inputSideTokens = d.input_tokens + d.cache_creation_tokens + d.cache_read_tokens
          return inputSideTokens > 0 ? (d.cache_read_tokens / inputSideTokens) * 100 : 0
        }),
        borderColor: chartColors.value.cacheHitRate,
        backgroundColor: `${chartColors.value.cacheHitRate}20`,
        fill: false,
        tension: 0.3,
        yAxisID: 'yPercent',
      },
    ],
  }
})

// lineOptions combines token and percentage axes with a shared HTML tooltip; tooltip
// indices retrieve full dates and costs from the supplied trend buckets.
const lineOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: {
    intersect: false,
    mode: 'index' as const,
  },
  plugins: {
    legend: {
      position: 'top' as const,
      labels: {
        color: chartColors.value.text,
        usePointStyle: true,
        pointStyle: 'circle',
        padding: 15,
        font: {
          size: CHART_LEGEND_FONT_SIZE,
        },
      },
    },
    tooltip: {
      enabled: false,
      external: externalTooltipHandler,
      callbacks: {
        title: (tooltipItems: any[]) => {
          const dataIndex = tooltipItems[0]?.dataIndex
          return dataIndex !== undefined ? props.trendData[dataIndex]?.date || '' : ''
        },
        label: (context: any) => {
          if (context.dataset.yAxisID === 'yPercent') {
            return `${context.dataset.label}: ${formatPercent(context.raw)}`
          }
          return `${context.dataset.label}: ${formatTokens(context.raw)}`
        },
        footer: (tooltipItems: any) => {
          const dataIndex = tooltipItems[0]?.dataIndex
          if (dataIndex !== undefined && props.trendData[dataIndex]) {
            const data = props.trendData[dataIndex]
            const actual = formatBalanceAmount(data.actual_cost, { fractionDigits: 4 })
            if (!props.showStandardCost) return `Actual: ${actual}`
            return `Actual: ${actual} | Standard: ${formatUsdAmount(data.cost, { fractionDigits: 4 })}`
          }
          return ''
        },
      },
    },
  },
  scales: {
    x: {
      grid: {
        color: chartColors.value.grid,
      },
      ticks: {
        color: chartColors.value.text,
        autoSkip: true,
        maxRotation: props.granularity === 'hour' ? 0 : 50,
        minRotation: props.granularity === 'hour' ? 0 : 0,
        font: {
          size: CHART_TICK_FONT_SIZE,
        },
      },
    },
    y: {
      grid: {
        color: chartColors.value.grid,
      },
      ticks: {
        color: chartColors.value.text,
        font: {
          size: CHART_TICK_FONT_SIZE,
        },
        callback: (value: string | number) => formatTokens(Number(value)),
      },
    },
    yPercent: {
      position: 'right' as const,
      min: 0,
      max: 100,
      grid: {
        drawOnChartArea: false,
      },
      ticks: {
        color: chartColors.value.cacheHitRate,
        font: {
          size: CHART_TICK_FONT_SIZE,
        },
        callback: (value: string | number) => `${value}%`,
      },
    },
  },
}))

// formatPercent displays cache percentages with two fractional digits.
//
// Values below one hundred are truncated before formatting so near-complete cache
// coverage does not round upward to a displayed one hundred percent.
const formatPercent = (value: number): string => {
  // Truncate partial coverage before decimal formatting so a value below one hundred
  // remains visibly below complete cache coverage.
  const displayValue = value < 100 ? Math.floor(value * 100) / 100 : value
  return `${displayValue.toFixed(2)}%`
}
</script>
