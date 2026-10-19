<template>
  <div v-if="hasDisplayPricing" class="min-w-0">
    <!-- 展开/收起触发条：右下角箭头指示面板状态，展开时向上、收起时向下。 -->
    <button
      type="button"
      class="mt-3 flex w-full items-center justify-between gap-2 border-t border-gray-100 pt-3 text-sm font-medium text-primary-600 transition hover:text-primary-700 dark:border-dark-700 dark:text-primary-300 dark:hover:text-primary-200"
      data-testid="model-pricing-toggle"
      :aria-expanded="expanded"
      @click="expanded = !expanded"
    >
      <span class="inline-flex items-center gap-1.5">
        <Icon name="eye" size="sm" />
        {{ expanded ? t('marketplace.collapsePricing') : t('marketplace.viewPricing') }}
      </span>
      <Icon name="chevronDown" class="transition-transform duration-normal" :class="{ 'rotate-180': expanded }" size="sm" :animate-on-hover="false" />
    </button>

    <!-- 收起后保留上下文区间和 fast mode，退出期间同步折叠高度。 -->
    <Collapse :open="expanded">
      <div class="pt-3">
        <!-- 右上角：上下文区间、fast mode、推理档位和分时切换，定价行随选择联动。 -->
        <div
          v-if="selectableIntervals.length > 0 || hasFastPricing || hasMaxEffortPricing || timeOptions.length > 0"
          class="mb-3 flex flex-wrap items-center justify-end gap-2"
        >
          <div
            v-segmented
            v-if="selectableIntervals.length > 0"
            class="segmented max-w-full flex-wrap"
            data-testid="pricing-interval-switch"
          >
            <button
              v-for="(item, index) in selectableIntervals"
              :key="item.key"
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': index === activeIntervalIndex }"
              @click="selectedIntervalIndex = index"
            >
              {{ formatCompactTokenRange(item.interval.min_tokens, item.interval.max_tokens) }}
            </button>
          </div>
          <div
            v-segmented
            v-if="hasFastPricing"
            class="segmented max-w-full flex-wrap"
            data-testid="pricing-fast-switch"
          >
            <button
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': !fastMode }"
              @click="fastMode = false"
            >
              {{ t('marketplace.pricingStandard') }}
            </button>
            <button
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': fastMode }"
              @click="fastMode = true"
            >
              {{ t('marketplace.pricingFast') }}
            </button>
          </div>
          <div
            v-segmented
            v-if="hasMaxEffortPricing"
            class="segmented max-w-full flex-wrap"
            data-testid="pricing-effort-switch"
          >
            <button
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': !maxEffortMode }"
              @click="maxEffortMode = false"
            >
              {{ t('marketplace.pricingDefaultEffort') }}
            </button>
            <button
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': maxEffortMode }"
              :title="t('marketplace.pricingMaxEffortHint')"
              @click="maxEffortMode = true"
            >
              {{ t('marketplace.pricingMaxEffort') }}
            </button>
          </div>
          <div
            v-segmented
            v-if="timeOptions.length > 0"
            class="segmented max-w-full flex-wrap"
            data-testid="pricing-time-switch"
          >
            <button
              v-for="(option, index) in timeOptions"
              :key="option.key"
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold tabular-nums"
              :class="{ 'segmented-item-active': index === activeTimeIndex }"
              @click="selectedTimeIndex = index"
            >
              {{ option.label }}
            </button>
          </div>
        </div>

        <!-- 定价信息在窄卡片内换行，抽屉宽度随父网格收缩。 -->
        <div v-if="activeRows.length > 0" class="space-y-2.5" data-testid="pricing-rows">
          <div
            v-for="row in activeRows"
            :key="row.key"
            class="flex items-baseline justify-between gap-3 border-b border-gray-100 pb-2 text-sm dark:border-dark-700"
          >
            <span class="min-w-0 max-w-[45%] shrink-0 break-words text-gray-500 dark:text-dark-400">{{ row.label }}</span>
            <span class="min-w-0 break-words text-right font-medium [overflow-wrap:anywhere] tabular-nums text-gray-900 dark:text-white">{{ row.value }}</span>
          </div>
        </div>
        <p v-else class="text-sm text-gray-400 dark:text-dark-500">
          {{ t('marketplace.pricingUnavailable') }}
        </p>

        <!-- 分时价格的时区和生效日写在价格行下方，切换项只放时段。 -->
        <p
          v-if="timePricingNote"
          class="mt-2.5 text-xs leading-relaxed text-gray-400 dark:text-dark-500"
          data-testid="pricing-time-note"
        >
          {{ timePricingNote }}
        </p>
      </div>
    </Collapse>
  </div>
</template>

<script setup lang="ts">
import { vSegmented } from '@/directives/segmented'
import Collapse from '@/components/common/Collapse.vue'
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatCompactTokenRange } from '@/utils/formatters'
import { formatPriceNumber, pricingKind, videoPricingEntries } from '@/utils/marketplacePricing'
import type { MarketplaceModel, MarketplaceModelPricing, MarketplacePricingInterval } from '@/types'

// ModelPricingPanel.vue displays public model prices in the existing marketplace layout.
// Backend quotes already contain the group multiplier; rows add the selected
// balance unit and billing unit. Named video dimensions and the explicit default
// retain separate labels, including when their amounts are zero.

// 完整定价面板在卡片内展开、收起，并提供上下文区间与 fast mode 切换。
const props = defineProps<{
  model: MarketplaceModel
}>()

const { t } = useI18n()
const { balanceUnitName } = useBalanceDisplay()

const expanded = ref(false)
const fastMode = ref(false)
const maxEffortMode = ref(false)
const selectedIntervalIndex = ref(0)


interface PricingRow {
  key: string
  label: string
  value: string
}

function hasPositiveValue(value?: number | null): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

function formatPrice(value: number): string {
  return `${formatPriceNumber(value)} ${balanceUnitName.value}`
}

// token 单价乘上当前选中的推理档位和分时倍率。
function formatPerMillion(value: number): string {
  return `${formatPrice(value * tokenPriceFactor.value * 1_000_000)} ${t('usage.perMillionTokens')}`
}

function formatPerImage(value: number): string {
  return `${formatPrice(value)} ${t('marketplace.perImage')}`
}

// 构建定价行。

function tokenPricingRowsFromValues(pricing: MarketplaceModelPricing | MarketplacePricingInterval): PricingRow[] {
  const rows: PricingRow[] = []

  if (hasPositiveValue(pricing.input_price_per_token)) {
    rows.push({ key: 'input', label: t('marketplace.input'), value: formatPerMillion(pricing.input_price_per_token) })
  }
  if (hasPositiveValue(pricing.image_input_price_per_token)) {
    rows.push({ key: 'image_input', label: t('marketplace.imageInput'), value: formatPerMillion(pricing.image_input_price_per_token) })
  }
  if (hasPositiveValue(pricing.output_price_per_token)) {
    rows.push({ key: 'output', label: t('marketplace.output'), value: formatPerMillion(pricing.output_price_per_token) })
  }
  if (hasPositiveValue(pricing.cache_write_price_per_token)) {
    rows.push({ key: 'cache_write', label: t('marketplace.cacheWrite'), value: formatPerMillion(pricing.cache_write_price_per_token) })
  }
  if (hasPositiveValue(pricing.cache_write_1h_price_per_token)) {
    rows.push({ key: 'cache_write_1h', label: t('marketplace.cacheWrite1h'), value: formatPerMillion(pricing.cache_write_1h_price_per_token) })
  }
  if (hasPositiveValue(pricing.cache_read_price_per_token)) {
    rows.push({ key: 'cache_read', label: t('marketplace.cacheRead'), value: formatPerMillion(pricing.cache_read_price_per_token) })
  }
  if (hasPositiveValue(pricing.image_output_price_per_token)) {
    rows.push({ key: 'image_output', label: t('marketplace.imageOutput'), value: formatPerMillion(pricing.image_output_price_per_token) })
  }

  return rows
}

// fast mode 价格行：只取 fast_* 字段，没有 fast 定价时返回空列表。
function fastTokenPricingRows(pricing: MarketplaceModelPricing | MarketplacePricingInterval): PricingRow[] {
  const rows: PricingRow[] = []

  if (hasPositiveValue(pricing.fast_input_price_per_token)) {
    rows.push({ key: 'fast_input', label: t('marketplace.fastInput'), value: formatPerMillion(pricing.fast_input_price_per_token) })
  }
  if (hasPositiveValue(pricing.fast_image_input_price_per_token)) {
    rows.push({ key: 'fast_image_input', label: t('marketplace.fastImageInput'), value: formatPerMillion(pricing.fast_image_input_price_per_token) })
  }
  if (hasPositiveValue(pricing.fast_output_price_per_token)) {
    rows.push({ key: 'fast_output', label: t('marketplace.fastOutput'), value: formatPerMillion(pricing.fast_output_price_per_token) })
  }
  if (hasPositiveValue(pricing.fast_cache_write_price_per_token)) {
    rows.push({ key: 'fast_cache_write', label: t('marketplace.fastCacheWrite'), value: formatPerMillion(pricing.fast_cache_write_price_per_token) })
  }
  if (hasPositiveValue(pricing.fast_cache_write_1h_price_per_token)) {
    rows.push({ key: 'fast_cache_write_1h', label: t('marketplace.fastCacheWrite1h'), value: formatPerMillion(pricing.fast_cache_write_1h_price_per_token) })
  }
  if (hasPositiveValue(pricing.fast_cache_read_price_per_token)) {
    rows.push({ key: 'fast_cache_read', label: t('marketplace.fastCacheRead'), value: formatPerMillion(pricing.fast_cache_read_price_per_token) })
  }
  if (hasPositiveValue(pricing.fast_image_output_price_per_token)) {
    rows.push({ key: 'fast_image_output', label: t('marketplace.fastImageOutput'), value: formatPerMillion(pricing.fast_image_output_price_per_token) })
  }

  return rows
}

// 价格为 0 表示免费，priced 状态下缺少正价时展示 0。
function zeroTokenPricingRows(): PricingRow[] {
  return [
    { key: 'input', label: t('marketplace.input'), value: formatPerMillion(0) },
    { key: 'output', label: t('marketplace.output'), value: formatPerMillion(0) },
  ]
}

/**
 * videoPricingRows formats video dimensions using the displayed balance unit.
 * Named dimensions use their supplied labels; the explicit default uses a
 * translated label, and every row retains the per-second billing unit.
 */
function videoPricingRows(pricing: MarketplaceModelPricing): PricingRow[] {
  return videoPricingEntries(pricing).map(item => ({
    key: item.key,
    label: item.resolution ?? t('marketplace.defaultVideoPrice'),
    value: `${formatPrice(item.price)} ${t('marketplace.perSecond')}`,
  }))
}

function imagePricingRows(pricing: MarketplaceModelPricing): PricingRow[] {
  const values = [
    { key: '1k', label: '1K', price: pricing.image_price_1k },
    { key: '2k', label: '2K', price: pricing.image_price_2k },
    { key: '4k', label: '4K', price: pricing.image_price_4k },
  ]

  return values.flatMap((item) => {
    if (typeof item.price !== 'number' || !Number.isFinite(item.price) || item.price < 0) {
      return []
    }

    return [{
      key: item.key,
      label: item.label,
      value: formatPerImage(item.price),
    }]
  })
}

const hasDisplayPricing = computed(() => pricingKind(props.model.pricing) !== 'unpriced')

// 后端返回有定价的区间，JSON 省略的零价字段仍表示免费区间。
const selectableIntervals = computed(() =>
  (props.model.pricing.context_intervals ?? [])
    .map((interval, index) => ({ interval, key: `${interval.min_tokens}-${interval.max_tokens ?? 'up'}-${index}` }))
)

const activeIntervalIndex = computed(() =>
  Math.min(selectedIntervalIndex.value, Math.max(0, selectableIntervals.value.length - 1))
)

// 定价数据来源：选中区间优先，否则用模型顶层价格。
const activeSource = computed<MarketplaceModelPricing | MarketplacePricingInterval>(() =>
  selectableIntervals.value[activeIntervalIndex.value]?.interval ?? props.model.pricing
)

// standardRows selects video, image, or token rows for the expanded quote.
const standardRows = computed<PricingRow[]>(() => {
  if (pricingKind(props.model.pricing) === 'video') {
    return videoPricingRows(props.model.pricing)
  }
  if (pricingKind(props.model.pricing) === 'image') {
    return imagePricingRows(props.model.pricing)
  }

  const rows = tokenPricingRowsFromValues(activeSource.value)
  if (rows.length > 0) {
    return rows
  }
  return props.model.pricing.price_status === 'priced' ? zeroTokenPricingRows() : []
})

const fastRows = computed(() => fastTokenPricingRows(activeSource.value))

// 当前定价来源存在 fast mode 加价时才展示切换。
const hasFastPricing = computed(() => pricingKind(props.model.pricing) === 'token' && fastRows.value.length > 0)

// 分时切换项：multiplier 是该时段乘到 token 单价上的倍率，其他时段为 1。
interface TimeOption {
  key: string
  label: string
  multiplier: number
  start: number
  end: number
}

const SECONDS_PER_DAY = 24 * 60 * 60

// 后端接受 HH:mm 和 HH:mm:ss，整分钟的时刻统一显示为 HH:mm，结束时间 00:00 显示为 24:00。
function formatPeriodTime(value: string, end: boolean): string {
  const short = value.endsWith(':00') && value.length === 8 ? value.slice(0, 5) : value
  return end && short === '00:00' ? '24:00' : short
}

function periodSeconds(value: string, end: boolean): number {
  const [hours = 0, minutes = 0, seconds = 0] = value.split(':').map(Number)
  const total = hours * 3600 + minutes * 60 + seconds
  return end && total === 0 ? SECONDS_PER_DAY : total
}

// 读取规则时区下的当前时刻；时区无效时返回 null，切换默认停在其他时段。
function currentZonedTime(timezone: string): { weekend: boolean; second: number } | null {
  try {
    const parts = new Intl.DateTimeFormat('en-US', {
      timeZone: timezone,
      weekday: 'short',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hourCycle: 'h23',
    }).formatToParts(new Date())
    const part = (type: string) => parts.find((item) => item.type === type)?.value ?? ''
    const weekday = part('weekday')
    return {
      weekend: weekday === 'Sat' || weekday === 'Sun',
      second: Number(part('hour')) * 3600 + Number(part('minute')) * 60 + Number(part('second')),
    }
  } catch {
    return null
  }
}

// Max 推理档位有独立倍率时才展示档位切换。
const hasMaxEffortPricing = computed(() =>
  pricingKind(props.model.pricing) === 'token' && hasPositiveValue(props.model.pricing.max_reasoning_effort_multiplier)
)

const timeOptions = computed<TimeOption[]>(() => {
  const timePricing = props.model.pricing.time_pricing
  if (pricingKind(props.model.pricing) !== 'token' || !timePricing || timePricing.periods.length === 0) {
    return []
  }
  const periods = timePricing.periods
    .map((period) => ({
      key: `${period.start_time}-${period.end_time}`,
      label: `${formatPeriodTime(period.start_time, false)}-${formatPeriodTime(period.end_time, true)}`,
      multiplier: period.multiplier,
      start: periodSeconds(period.start_time, false),
      end: periodSeconds(period.end_time, true),
    }))
    .sort((a, b) => a.start - b.start)
  const coveredSeconds = periods.reduce((sum, period) => sum + period.end - period.start, 0)
  // 时段之外按 1x 计费；仅工作日生效时周末也属于其他时段。
  if (timePricing.weekdays_only || coveredSeconds < SECONDS_PER_DAY) {
    return [{ key: 'other', label: t('marketplace.timePricingOtherHours'), multiplier: 1, start: 0, end: 0 }, ...periods]
  }
  return periods
})

// 默认选中规则时区下当前所在的时段，展开面板看到的就是此刻的价格。
const selectedTimeIndex = ref<number | null>(null)

const currentTimeIndex = computed(() => {
  const timePricing = props.model.pricing.time_pricing
  const now = timePricing ? currentZonedTime(timePricing.timezone) : null
  if (!timePricing || !now || (timePricing.weekdays_only && now.weekend)) {
    return 0
  }
  const index = timeOptions.value.findIndex((option) => option.key !== 'other' && now.second >= option.start && now.second < option.end)
  return Math.max(index, 0)
})

const activeTimeIndex = computed(() =>
  Math.min(selectedTimeIndex.value ?? currentTimeIndex.value, Math.max(0, timeOptions.value.length - 1))
)

const tokenPriceFactor = computed(() => {
  const timeMultiplier = timeOptions.value[activeTimeIndex.value]?.multiplier ?? 1
  const effortMultiplier = maxEffortMode.value && hasMaxEffortPricing.value
    ? props.model.pricing.max_reasoning_effort_multiplier ?? 1
    : 1
  return timeMultiplier * effortMultiplier
})

const timePricingNote = computed(() => {
  const timePricing = props.model.pricing.time_pricing
  if (timeOptions.value.length === 0 || !timePricing) {
    return ''
  }
  const key = timePricing.weekdays_only ? 'marketplace.timePricingNoteWeekdays' : 'marketplace.timePricingNoteEveryDay'
  return t(key, { timezone: timePricing.timezone })
})

const activeRows = computed(() => {
  if (fastMode.value && fastRows.value.length > 0) {
    return fastRows.value
  }
  return standardRows.value
})
</script>
