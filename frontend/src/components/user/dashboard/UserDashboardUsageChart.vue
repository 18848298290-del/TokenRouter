<template>
  <div class="space-y-4" :aria-busy="loading">
    <!-- 指标卡展示所选范围的合计与环比，点击后在下方折线图中查看该指标 -->
    <div
      class="dash-rise-item grid grid-cols-2 gap-4 lg:grid-cols-4"
      :style="{ '--rise-i': 0 }"
      role="tablist"
      :aria-label="t('dashboard.usageChart.metricsLabel')"
    >
      <button
        v-for="item in metricTabs"
        :key="item.key"
        type="button"
        role="tab"
        :data-testid="`usage-metric-${item.key}`"
        :aria-selected="metric === item.key"
        class="metric-card card flex min-w-0 flex-col gap-1 overflow-hidden p-4 text-left transition-colors duration-fast sm:gap-3"
        :class="metric === item.key
          ? 'border-primary-600 dark:border-primary-500/70'
          : 'hover:border-black/20 dark:hover:border-dark-500'"
        @click="selectMetric(item.key)"
      >
        <div class="flex min-w-0 items-center gap-2">
          <span
            class="flex h-5 w-5 shrink-0 items-center justify-center rounded-compact sm:h-7 sm:w-7"
            :class="[METRIC_TONES[item.key].tile, METRIC_TONES[item.key].icon]"
          >
            <Icon :name="item.icon" size="sm" />
          </span>
          <span class="truncate text-xs font-medium text-gray-500 dark:text-dark-400">{{ item.label }}</span>
        </div>
        <div class="flex min-w-0 items-end justify-between gap-3">
          <Skeleton v-if="!loaded" :width="96" :height="32" />
          <span v-else class="min-w-0 truncate text-2xl font-semibold tracking-tight tabular-nums text-gray-900 dark:text-dark-50">
            <!-- 滚动中的数字只做视觉展示，读屏读取最终值 -->
            <span aria-hidden="true">{{ item.animatedValue }}</span>
            <span class="sr-only">{{ item.value }}</span>
          </span>
          <!-- 只画走势线，选中的指标用品牌色 -->
          <UsageSparkline
            v-if="loaded"
            class="metric-sparkline h-8 w-20 shrink-0"
            :class="metric === item.key ? 'text-primary-600 dark:text-primary-500' : 'text-gray-400 dark:text-dark-500'"
            :values="item.series"
            :data-testid="`usage-sparkline-${item.key}`"
          />
        </div>
        <Skeleton v-if="!loaded" :width="120" :height="20" />
        <span v-else class="flex min-w-0 flex-wrap items-center gap-1 text-xs text-gray-500 dark:text-dark-400 sm:gap-2" :data-testid="`usage-delta-${item.key}`">
          <template v-if="item.delta">
            <span
              class="inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 font-medium tabular-nums"
              :class="deltaToneClasses[item.key === 'cacheHitRate' ? item.delta.tone : 'flat']"
            >
              <Icon :name="deltaIcons[item.delta.tone]" size="xs" :animate-on-hover="false" />
              {{ item.delta.text }}
            </span>
            <span class="sm:hidden">{{ t('dashboard.usageChart.vsPreviousShort') }}</span>
            <span class="hidden sm:inline">{{ t('dashboard.usageChart.vsPrevious') }}</span>
          </template>
          <span v-else class="truncate">{{ t('dashboard.usageChart.noPrevious') }}</span>
        </span>
      </button>
    </div>

    <!-- 折线图与模型排行并排，窄于 xl 时排行排到图表下方 -->
    <div class="dash-rise-item grid grid-cols-1 gap-4 xl:grid-cols-3" :style="{ '--rise-i': 1 }">
      <div ref="chartCardRef" class="card flex min-w-0 flex-col p-4 xl:col-span-2" data-testid="usage-chart-card">
        <div class="mb-4 flex min-h-7 flex-wrap items-center justify-between gap-2">
          <div class="flex items-baseline gap-2">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-dark-50" data-testid="usage-chart-title">{{ chartTitle }}</h3>
            <span class="text-xs text-gray-500 dark:text-dark-400">{{ granularityHint }}</span>
          </div>
          <!-- 图例兼开关：Token 按分项开关，其余指标开关上一周期对比线 -->
          <div class="flex flex-wrap gap-2">
            <template v-if="metric === 'tokens'">
              <button
                v-for="series in tokenSeries"
                :key="series.key"
                type="button"
                class="legend-chip"
                :class="hiddenTokenSeries.includes(series.key) ? 'legend-chip-off' : 'legend-chip-on'"
                :aria-pressed="!hiddenTokenSeries.includes(series.key)"
                :data-testid="`usage-series-${series.key}`"
                @click="toggleTokenSeries(series.key)"
              >
                <span
                  class="h-2 w-2 rounded-full"
                  :class="{ 'opacity-30': hiddenTokenSeries.includes(series.key) }"
                  :style="{ backgroundColor: series.color }"
                ></span>
                {{ series.label }}
              </button>
            </template>
            <template v-else>
              <span class="legend-chip legend-chip-on" data-testid="usage-legend-current">
                <span class="h-0.5 w-3 rounded-full" :style="{ backgroundColor: mainColor }"></span>
                {{ t('dashboard.usageChart.currentPeriod') }}
              </span>
              <button
                type="button"
                class="legend-chip"
                :class="showPrevious ? 'legend-chip-on' : 'legend-chip-off'"
                :aria-pressed="showPrevious"
                data-testid="usage-series-previous"
                @click="showPrevious = !showPrevious"
              >
                <span class="w-3 border-t-2 border-dashed" :style="{ borderColor: themeColors.muted }" :class="{ 'opacity-30': !showPrevious }"></span>
                {{ t('dashboard.usageChart.previousPeriod') }}
              </button>
            </template>
          </div>
        </div>

        <div class="relative h-64 sm:h-80">
          <ChartSkeleton v-if="!loaded" variant="plot" height="100%" />
          <div
            v-else-if="!hasUsage"
            data-testid="usage-chart-empty"
            class="flex h-full flex-col items-center justify-center gap-2 text-sm text-gray-500 dark:text-dark-400"
          >
            <Icon name="chart" size="lg" class="text-gray-300 dark:text-dark-600" />
            {{ t('dashboard.usageChart.empty') }}
          </div>
          <template v-else>
            <!-- 切换指标或重新取数时重建图表，与首次打开播放同一套入场动画 -->
            <Line ref="lineRef" :key="chartKey" :data="chartData" :options="chartOptions" :plugins="chartPlugins" />
            <!-- 未结束时段的末端呼吸点，位置由图表插件在每次绘制后写入 -->
            <span
              v-if="livePoint"
              data-testid="usage-live-point"
              class="pointer-events-none absolute"
              :style="{ left: `${livePoint.x}px`, top: `${livePoint.y}px`, color: mainColor }"
              aria-hidden="true"
            >
              <span class="live-point-ping absolute -left-1.5 -top-1.5 h-3 w-3 animate-ping rounded-full bg-current opacity-60 motion-reduce:animate-none"></span>
              <span class="absolute -left-1 -top-1 h-2 w-2 rounded-full bg-current ring-2 ring-white dark:ring-dark-900"></span>
            </span>
          </template>
        </div>
      </div>

      <UserDashboardTopModels class="xl:col-span-1" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { usePreferredReducedMotion } from '@vueuse/core'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Tooltip,
  type Chart,
  type ChartDataset,
  type Plugin,
  type ScriptableLineSegmentContext,
  type TooltipItem,
} from 'chart.js'
import { Line } from 'vue-chartjs'
import ChartSkeleton from '@/components/common/ChartSkeleton.vue'
import Skeleton from '@/components/common/Skeleton.vue'
import Icon from '@/components/icons/Icon.vue'
import type { IconName } from '@/components/icons/registry'
import UsageSparkline from './UsageSparkline.vue'
import UserDashboardTopModels from './UserDashboardTopModels.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { useChartTheme, CHART_SERIES_COLORS, CHART_TICK_FONT_SIZE } from '@/composables/useChartTheme'
import { useCountUp } from '@/composables/useCountUp'
import { externalTooltipHandler, hideExternalTooltip } from '@/utils/chartExternalTooltip'
import { formatNumberLocaleString as formatNumber, formatTokensK } from '@/utils/format'
import type { TrendDataPoint } from '@/types'
import { cacheHitRateOf } from './usageChartData'
import { CHART_REVEAL_MS, COUNT_UP_MS } from './dashboardMotion'
import { METRIC_TONES } from './metricTones'
import { injectUsageChartState, type UsageMetric } from './usageChartState'

// UserDashboardUsageChart.vue renders selectable usage totals and their time
// series from the dashboard's shared query state. Each card presents the selected
// period alongside its previous period comparison. Request, token, and cost
// changes use neutral colors because increases alone do not establish a better
// result; cache hit rate changes retain directional status colors.
//
// Selecting a metric changes the chart and model ranking together. Token series
// can be hidden independently, while other metrics support a previous period
// overlay. Incomplete time buckets use dashed segments and an animated endpoint,
// with custom drawing plugins positioning peak and average annotations relative
// to the chart scales.

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Tooltip)

type TokenSeriesKey = 'input_tokens' | 'output_tokens' | 'cache_creation_tokens' | 'cache_read_tokens'

// 数据集上的自定义标记：previous 表示上一周期对比线。
type UsageDataset = ChartDataset<'line', Array<number | null>> & {
  previous?: boolean
}

// 请求数和消费折线使用品牌色，浅色取 primary-600，深色取 primary-500。
const BRAND_LINE_COLOR = { light: '#12A7E8', dark: '#00D2FF' }
// 卡片底色，悬停点和峰值环的描边与之相同，看起来像镂空。
const CARD_SURFACE = { light: '#FFFFFF', dark: '#0F0F10' }
// 未结束时段与上一周期对比线的虚线样式。
const OPEN_SEGMENT_DASH = [4, 4]
const PREVIOUS_DASH = [4, 4]
// 峰值与均值标签胶囊的尺寸。
const LABEL_HEIGHT = 18
const LABEL_PADDING_X = 6
const LABEL_GAP = 10

const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()
const { colors: themeColors, isDark } = useChartTheme()
const reducedMotion = usePreferredReducedMotion()
const {
  metric,
  activeRange,
  loading,
  loaded,
  current,
  previous,
  previousTotals,
  totals,
  selectMetric,
} = injectUsageChartState()

const hiddenTokenSeries = ref<TokenSeriesKey[]>([])
const showPrevious = ref(true)
const lineRef = ref<{ chart?: Chart } | null>(null)
const chartCardRef = ref<HTMLElement | null>(null)
const livePoint = ref<{ x: number; y: number } | null>(null)

const chartTitle = computed(() => t('dashboard.usageChart.trendTitle', {
  metric: t(`dashboard.usageChart.metrics.${metric.value}`),
}))

const granularityHint = computed(() => (
  activeRange.value.granularity === 'hour' ? t('dashboard.usageChart.byHour') : t('dashboard.usageChart.byDay')
))

const deltaToneClasses = {
  up: 'bg-emerald-500/10 text-emerald-600 dark:text-emerald-400',
  down: 'bg-red-500/10 text-red-600 dark:text-red-400',
  flat: 'bg-gray-100 text-gray-500 dark:bg-dark-800 dark:text-dark-300',
}

const deltaIcons: Record<keyof typeof deltaToneClasses, IconName> = {
  up: 'trendingUp',
  down: 'trendingDown',
  flat: 'minus',
}

type Delta = { text: string; tone: keyof typeof deltaToneClasses }

// percentDelta 计算环比百分比；上一周期为 0 时无法比较。
const percentDelta = (currentValue: number, previousValue: number | undefined): Delta | null => {
  if (previousValue === undefined || previousValue <= 0) return null
  const change = ((currentValue - previousValue) / previousValue) * 100
  const percent = Math.round(change)
  if (percent === 0) return { text: '0%', tone: 'flat' }
  return { text: `${percent > 0 ? '+' : ''}${percent}%`, tone: percent > 0 ? 'up' : 'down' }
}

// pointDelta 计算命中率的百分点差值。
const pointDelta = (currentValue: number | null, previousValue: number | null | undefined): Delta | null => {
  if (currentValue === null || previousValue === null || previousValue === undefined) return null
  const change = Math.round((currentValue - previousValue) * 10) / 10
  if (change === 0) return { text: '0 pt', tone: 'flat' }
  return { text: `${change > 0 ? '+' : ''}${change.toFixed(1)} pt`, tone: change > 0 ? 'up' : 'down' }
}

const formatPercent = (value: number | null): string => (value === null ? '—' : `${value.toFixed(1)}%`)

// 小额消费显示 4 位小数。
const formatCostTotal = (value: number): string => formatBalanceAmount(value, { fractionDigits: value >= 1 ? 2 : 4 })

// metricValueOf 取某个时段在指定指标下的值，命中率没有输入时为 null。
const metricValueOf = (point: TrendDataPoint, key: UsageMetric): number | null => {
  if (key === 'tokens') return point.total_tokens
  if (key === 'cost') return point.actual_cost
  if (key === 'cacheHitRate') return cacheHitRateOf(point.input_tokens, point.cache_creation_tokens, point.cache_read_tokens)
  return point.requests
}

// 指标卡数字从旧值滚动到新值，请求数在动画中取整。
const animatedRequests = useCountUp(() => totals.value.requests, COUNT_UP_MS)
const animatedTokens = useCountUp(() => totals.value.tokens, COUNT_UP_MS)
const animatedCost = useCountUp(() => totals.value.cost, COUNT_UP_MS)
const animatedHitRate = useCountUp(() => totals.value.cacheHitRate ?? 0, COUNT_UP_MS)

const metricTabs = computed(() => {
  const now = totals.value
  const prev = previousTotals.value
  const seriesOf = (key: UsageMetric) => current.value.map((point) => metricValueOf(point, key))
  return [
    {
      key: 'requests' as const,
      icon: 'swap' as IconName,
      label: t('dashboard.usageChart.metrics.requests'),
      value: formatNumber(now.requests),
      animatedValue: formatNumber(Math.round(animatedRequests.value)),
      delta: percentDelta(now.requests, prev?.requests),
      series: seriesOf('requests'),
    },
    {
      key: 'tokens' as const,
      icon: 'layers' as IconName,
      label: t('dashboard.usageChart.metrics.tokens'),
      value: formatTokensK(now.tokens),
      animatedValue: formatTokensK(animatedTokens.value),
      delta: percentDelta(now.tokens, prev?.tokens),
      series: seriesOf('tokens'),
    },
    {
      key: 'cost' as const,
      icon: 'dollar' as IconName,
      label: t('dashboard.usageChart.metrics.cost'),
      value: formatCostTotal(now.cost),
      animatedValue: formatCostTotal(animatedCost.value),
      delta: percentDelta(now.cost, prev?.cost),
      series: seriesOf('cost'),
    },
    {
      key: 'cacheHitRate' as const,
      icon: 'database' as IconName,
      label: t('dashboard.usageChart.metrics.cacheHitRate'),
      value: formatPercent(now.cacheHitRate),
      animatedValue: now.cacheHitRate === null ? '—' : formatPercent(animatedHitRate.value),
      delta: pointDelta(now.cacheHitRate, prev?.cacheHitRate),
      series: seriesOf('cacheHitRate'),
    },
  ]
})

const tokenSeries = computed(() => [
  { key: 'input_tokens' as const, label: t('dashboard.usageChart.series.input'), color: CHART_SERIES_COLORS.input },
  { key: 'output_tokens' as const, label: t('dashboard.usageChart.series.output'), color: CHART_SERIES_COLORS.output },
  { key: 'cache_creation_tokens' as const, label: t('dashboard.usageChart.series.cacheCreation'), color: CHART_SERIES_COLORS.cacheCreation },
  { key: 'cache_read_tokens' as const, label: t('dashboard.usageChart.series.cacheRead'), color: CHART_SERIES_COLORS.cacheRead },
])

const hasUsage = computed(() => totals.value.requests > 0 || totals.value.tokens > 0 || totals.value.cost > 0)

// 横轴按粒度只显示时分或月日，完整时间留给提示标题。
const shortLabel = (date: string): string => (
  activeRange.value.granularity === 'hour' ? date.slice(11, 16) : date.slice(5, 10)
)
const axisLabels = computed(() => current.value.map((point) => shortLabel(point.date)))

const brandColor = computed(() => (isDark.value ? BRAND_LINE_COLOR.dark : BRAND_LINE_COLOR.light))
const surfaceColor = computed(() => (isDark.value ? CARD_SURFACE.dark : CARD_SURFACE.light))

// 单指标主线的颜色，命中率沿用序列色。
const mainColor = computed(() => (metric.value === 'cacheHitRate' ? CHART_SERIES_COLORS.cacheHitRate : brandColor.value))

// 窗口包含当前时刻时，最后一个时段（今天或当前小时）还没结束，数据不完整。
const openBucketIndex = computed(() => (
  activeRange.value.endAt.getTime() > Date.now() && current.value.length > 1 ? current.value.length - 1 : null
))

// 尚在累计的末段使用虚线标记。
const segmentStyle = {
  borderDash: (context: ScriptableLineSegmentContext) => (
    context.p1DataIndex === openBucketIndex.value ? OPEN_SEGMENT_DASH : undefined
  ),
}

// lineDataset 生成统一样式的折线数据集；monotone 插值保证曲线不越过真实数据点，线下不填充。
const lineDataset = (label: string, data: Array<number | null>, color: string): UsageDataset => ({
  label,
  data,
  borderColor: color,
  backgroundColor: color,
  borderWidth: 2,
  fill: false,
  cubicInterpolationMode: 'monotone' as const,
  segment: segmentStyle,
  pointRadius: 0,
  pointHoverRadius: 5,
  pointHoverBorderWidth: 2,
  pointHoverBorderColor: surfaceColor.value,
  pointHoverBackgroundColor: color,
  spanGaps: true,
})

const chartData = computed(() => {
  const points = current.value
  if (metric.value === 'tokens') {
    const visible = tokenSeries.value.filter((series) => !hiddenTokenSeries.value.includes(series.key))
    return {
      labels: axisLabels.value,
      datasets: visible.map((series) => ({
        ...lineDataset(series.label, points.map((point) => point[series.key]), series.color),
        borderWidth: 1.75,
      })),
    }
  }
  const datasets: UsageDataset[] = [lineDataset(
    metric.value === 'cost' ? t('dashboard.usageChart.series.actualCost') : t(`dashboard.usageChart.metrics.${metric.value}`),
    points.map((point) => metricValueOf(point, metric.value)),
    mainColor.value,
  )]
  if (showPrevious.value && previous.value.length > 0) {
    // 上一周期按下标与本期对齐，只取本期已有的时段；order 更大的数据集画在下层。
    datasets.push({
      label: t('dashboard.usageChart.previousPeriod'),
      data: previous.value.slice(0, points.length).map((point) => metricValueOf(point, metric.value)),
      borderColor: themeColors.value.muted,
      backgroundColor: themeColors.value.muted,
      borderWidth: 1.5,
      borderDash: PREVIOUS_DASH,
      fill: false,
      cubicInterpolationMode: 'monotone' as const,
      pointRadius: 0,
      pointHoverRadius: 3,
      pointHoverBorderWidth: 0,
      pointHoverBackgroundColor: themeColors.value.muted,
      spanGaps: true,
      order: 1,
      previous: true,
    })
  }
  return { labels: axisLabels.value, datasets }
})

// formatAxisValue 格式化纵轴刻度，消费刻度不带货币符号以免挤占宽度。
const formatAxisValue = (value: number): string => {
  if (metric.value === 'tokens') return formatTokensK(value)
  if (metric.value === 'cacheHitRate') return `${value}%`
  if (metric.value === 'cost') return String(Math.round(value * 10000) / 10000)
  return formatNumber(value)
}

// formatValue 格式化提示和标注中的单个数值。
const formatValue = (value: number | null): string => {
  if (value === null) return '—'
  if (metric.value === 'tokens') return formatTokensK(value)
  if (metric.value === 'cacheHitRate') return formatPercent(value)
  if (metric.value === 'cost') return formatBalanceAmount(value, { fractionDigits: 4 })
  // 请求数的均值可能带小数，较大时取整，较小时保留一位。
  return formatNumber(value >= 100 ? Math.round(value) : Math.round(value * 10) / 10)
}

// annotation 是单指标的峰值与均值；均值只统计已结束的时段，未结束的时段还在累计。
const annotation = computed(() => {
  if (metric.value === 'tokens' || !hasUsage.value) return null
  const values = current.value.map((point) => metricValueOf(point, metric.value))
  let peakIndex = -1
  values.forEach((value, index) => {
    if (value !== null && (peakIndex < 0 || value > (values[peakIndex] as number))) peakIndex = index
  })
  if (peakIndex < 0 || !values[peakIndex]) return null
  const closed = values.filter((value, index): value is number => value !== null && index !== openBucketIndex.value)
  const average = closed.length > 0 ? closed.reduce((sum, value) => sum + value, 0) / closed.length : null
  return { peakIndex, peakValue: values[peakIndex] as number, average }
})

const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { intersect: false, mode: 'index' as const },
  // 标签胶囊画在绘图区上方，留出空间避免被裁切。
  layout: { padding: { top: LABEL_HEIGHT / 2 } },
  plugins: {
    legend: { display: false },
    tooltip: {
      enabled: false,
      external: externalTooltipHandler,
      // Token 按数值从大到小列出；单指标本期排在上一周期之前。
      itemSort: (a: TooltipItem<'line'>, b: TooltipItem<'line'>) => (
        metric.value === 'tokens'
          ? ((b.raw as number | null) ?? 0) - ((a.raw as number | null) ?? 0)
          : a.datasetIndex - b.datasetIndex
      ),
      callbacks: {
        title: (items: TooltipItem<'line'>[]) => current.value[items[0]?.dataIndex]?.date ?? '',
        label: (context: TooltipItem<'line'>) => {
          const dataset = context.dataset as UsageDataset
          const value = formatValue(context.raw as number | null)
          if (!dataset.previous) return `${dataset.label}: ${value}`
          const previousDate = previous.value[context.dataIndex]?.date
          return `${dataset.label}${previousDate ? ` (${shortLabel(previousDate)})` : ''}: ${value}`
        },
        footer: (items: TooltipItem<'line'>[]) => {
          if (metric.value !== 'tokens' || items.length < 2) return ''
          const sum = items.reduce((total, item) => total + ((item.raw as number | null) ?? 0), 0)
          return `${t('dashboard.usageChart.series.total')}: ${formatTokensK(sum)}`
        },
      },
    },
  },
  scales: {
    x: {
      grid: { display: false },
      ticks: {
        color: themeColors.value.text,
        autoSkip: true,
        maxTicksLimit: 8,
        maxRotation: 0,
        font: { size: CHART_TICK_FONT_SIZE },
      },
    },
    y: {
      beginAtZero: true,
      // 顶部留白，峰值标签不贴着图表上沿；命中率固定 0-100%。
      ...(metric.value === 'cacheHitRate' ? { max: 100 } : { grace: '15%' }),
      grid: { color: themeColors.value.grid, drawTicks: false },
      border: { display: false, dash: [3, 3] },
      ticks: {
        color: themeColors.value.text,
        padding: 8,
        maxTicksLimit: 5,
        font: { size: CHART_TICK_FONT_SIZE },
        callback: (value: string | number) => formatAxisValue(Number(value)),
      },
    },
  },
}))

// crosshairPlugin 在悬停的时间点画一条贯穿绘图区的竖线，画在折线下层。
const crosshairPlugin: Plugin<'line'> = {
  id: 'usageCrosshair',
  beforeDatasetsDraw(chart) {
    const active = chart.getActiveElements()
    if (!active.length) return
    const { ctx, chartArea } = chart
    const x = active[0].element.x
    ctx.save()
    ctx.strokeStyle = themeColors.value.muted
    ctx.globalAlpha = 0.5
    ctx.lineWidth = 1
    ctx.setLineDash([3, 3])
    ctx.beginPath()
    ctx.moveTo(x, chartArea.top)
    ctx.lineTo(x, chartArea.bottom)
    ctx.stroke()
    ctx.restore()
  },
}

// drawLabel 画一个圆角标签胶囊，x 为左边缘，y 为上边缘。
const drawLabel = (ctx: CanvasRenderingContext2D, text: string, x: number, y: number, fill: string, color: string) => {
  const width = ctx.measureText(text).width + LABEL_PADDING_X * 2
  ctx.fillStyle = fill
  ctx.beginPath()
  if (typeof ctx.roundRect === 'function') ctx.roundRect(x, y, width, LABEL_HEIGHT, LABEL_HEIGHT / 2)
  else ctx.rect(x, y, width, LABEL_HEIGHT)
  ctx.fill()
  ctx.fillStyle = color
  ctx.fillText(text, x + LABEL_PADDING_X, y + LABEL_HEIGHT / 2)
}

// 描线进度 0-1，绘图区按进度从左向右裁剪；减少动态效果时始终为 1。
let revealProgress = 1
let revealFrame = 0

// revealPlugin 按描线进度裁剪数据集，并在裁剪范围内画峰值与均值标注；描线完成后记录末端呼吸点的位置。
const revealPlugin: Plugin<'line'> = {
  id: 'usageReveal',
  beforeDatasetsDraw(chart) {
    const { ctx, chartArea } = chart
    ctx.save()
    if (revealProgress < 1) {
      ctx.beginPath()
      ctx.rect(chartArea.left - LABEL_GAP, 0, (chartArea.width + LABEL_GAP * 2) * revealProgress, chart.height)
      ctx.clip()
    }
  },
  afterDatasetsDraw(chart) {
    drawAnnotations(chart)
    chart.ctx.restore()
  },
  afterDraw(chart) {
    updateLivePoint(chart)
  },
}

// drawAnnotations 画均值虚线与峰值环，标签胶囊限制在绘图区内。
const drawAnnotations = (chart: Chart) => {
  const info = annotation.value
  const meta = chart.getDatasetMeta(0)
  if (!info || !meta || meta.hidden) return
  const { ctx, chartArea, scales } = chart
  ctx.font = `500 ${CHART_TICK_FONT_SIZE + 1}px ${ChartJS.defaults.font.family}`
  ctx.textBaseline = 'middle'

  if (info.average !== null) {
    const y = scales.y.getPixelForValue(info.average)
    ctx.save()
    ctx.strokeStyle = themeColors.value.muted
    ctx.globalAlpha = 0.6
    ctx.lineWidth = 1
    ctx.setLineDash([2, 4])
    ctx.beginPath()
    ctx.moveTo(chartArea.left, y)
    ctx.lineTo(chartArea.right, y)
    ctx.stroke()
    ctx.restore()
    const text = `${t('dashboard.usageChart.average')} · ${formatValue(info.average)}`
    const width = ctx.measureText(text).width + LABEL_PADDING_X * 2
    drawLabel(ctx, text, chartArea.right - width, y - LABEL_HEIGHT / 2, surfaceColor.value, themeColors.value.muted)
  }

  const point = meta.data[info.peakIndex]
  if (!point || !Number.isFinite(point.x) || !Number.isFinite(point.y)) return
  ctx.save()
  ctx.strokeStyle = mainColor.value
  ctx.fillStyle = surfaceColor.value
  ctx.lineWidth = 2
  ctx.beginPath()
  ctx.arc(point.x, point.y, 4.5, 0, Math.PI * 2)
  ctx.fill()
  ctx.stroke()
  ctx.restore()
  const text = `${t('dashboard.usageChart.peak')} · ${formatValue(info.peakValue)}`
  const width = ctx.measureText(text).width + LABEL_PADDING_X * 2
  const x = Math.min(Math.max(point.x - width / 2, chartArea.left), chartArea.right - width)
  // 上方放不下时改放在峰值点下方。
  const above = point.y - LABEL_GAP - LABEL_HEIGHT
  const y = above >= chartArea.top - LABEL_HEIGHT / 2 ? above : point.y + LABEL_GAP
  drawLabel(ctx, text, x, y, `${mainColor.value}26`, mainColor.value)
}

// updateLivePoint 记录未结束时段末端点的位置；描线未完成或该时段不存在时隐藏。
const updateLivePoint = (chart: Chart) => {
  const index = openBucketIndex.value
  const meta = chart.getDatasetMeta(0)
  const point = index === null || metric.value === 'tokens' || revealProgress < 1 ? null : meta?.data[index]
  if (!point || meta.hidden || !Number.isFinite(point.x) || !Number.isFinite(point.y)) {
    if (livePoint.value) livePoint.value = null
    return
  }
  if (livePoint.value?.x === point.x && livePoint.value?.y === point.y) return
  livePoint.value = { x: point.x, y: point.y }
}

const chartPlugins = [crosshairPlugin, revealPlugin]

const cancelReveal = () => {
  if (revealFrame) cancelAnimationFrame(revealFrame)
  revealFrame = 0
}

// playReveal 从左向右描线，各帧沿用当前布局重绘。
const playReveal = () => {
  cancelReveal()
  if (reducedMotion.value === 'reduce' || typeof requestAnimationFrame !== 'function') {
    revealProgress = 1
    return
  }
  revealProgress = 0
  const startedAt = performance.now()
  const tick = (now: number) => {
    const progress = Math.min((now - startedAt) / CHART_REVEAL_MS, 1)
    // 三次缓出：开头快，收尾放慢。
    revealProgress = 1 - Math.pow(1 - progress, 3)
    lineRef.value?.chart?.draw()
    revealFrame = progress < 1 ? requestAnimationFrame(tick) : 0
  }
  revealFrame = requestAnimationFrame(tick)
}

// 图表的 key：切换指标或重新取数（范围、筛选、刷新）时递增，图表重建后从头播放入场动画，与首次打开一致。
const chartVersion = ref(0)
const chartKey = computed(() => `${metric.value}-${chartVersion.value}`)

watch(current, () => {
  chartVersion.value += 1
  playReveal()
})
watch(metric, playReveal)

onBeforeUnmount(() => {
  cancelReveal()
  hideExternalTooltip()
})

const toggleTokenSeries = (key: TokenSeriesKey) => {
  hiddenTokenSeries.value = hiddenTokenSeries.value.includes(key)
    ? hiddenTokenSeries.value.filter((item) => item !== key)
    : [...hiddenTokenSeries.value, key]
}

// 供页面在热力图选中某天后把图表滚动到可见区域。
defineExpose({ chartCardRef })
</script>

<style scoped>
/* 卡片内容宽度够放下数字和走势线时才显示走势线，窄卡片优先保证数字完整。 */
.metric-card {
  container-type: inline-size;
}

.metric-sparkline {
  display: none;
}

@container (min-width: 200px) {
  .metric-sparkline {
    display: block;
  }
}

/* 图例兼开关的胶囊 */
.legend-chip {
  @apply inline-flex items-center gap-2 rounded-full border border-gray-200 px-3 py-1 text-xs transition-colors duration-fast dark:border-dark-600;
}

.legend-chip-on {
  @apply text-gray-700 dark:text-dark-100;
}

.legend-chip-off {
  @apply text-gray-400 dark:text-dark-500;
}
</style>
