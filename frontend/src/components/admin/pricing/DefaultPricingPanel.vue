<template>
  <TablePageLayout>
    <template #filters>
      <div class="space-y-2">
        <div class="flex flex-wrap items-start justify-between gap-2">
          <div class="flex min-w-0 flex-1 flex-nowrap items-center gap-2">
            <div class="input-icon-wrap min-w-0 flex-1 sm:flex-none sm:w-64">
              <Icon name="search" size="md" class="input-icon text-gray-400 dark:text-gray-500" />
              <input v-model="search" class="input input-has-icon" :placeholder="t('admin.pricing.defaults.search')" :aria-label="t('admin.pricing.defaults.search')" />
            </div>
            <FilterDropdown :active-count="activeFilterCount" @reset="platform = ''; mode = ''">
              <FilterField :label="t('admin.pricing.defaults.columns.platform')">
                <Select v-model="platform" :options="platformOptions" :aria-label="t('admin.pricing.defaults.columns.platform')" />
              </FilterField>
              <FilterField :label="t('admin.pricing.defaults.columns.billing_mode')">
                <Select v-model="mode" :options="modeOptions" :aria-label="t('admin.pricing.defaults.columns.billing_mode')" />
              </FilterField>
            </FilterDropdown>
          </div>
          <div class="ml-auto flex shrink-0 flex-wrap items-center justify-end gap-2">
            <button type="button" class="btn btn-secondary btn-icon" :disabled="loading || updating" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="load">
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button type="button" class="btn btn-primary whitespace-nowrap" :disabled="loading || updating" :title="t('admin.pricing.defaults.updateHint')" @click="updateCatalog">
              <Icon :name="updating ? 'refresh' : 'download'" size="sm" class="mr-2" :class="updating ? 'animate-spin' : ''" />
              {{ t(updating ? 'admin.pricing.defaults.updating' : 'admin.pricing.defaults.update') }}
            </button>
          </div>
        </div>
        <ModelCatalogInfo :version="catalogVersion" :updated-at="updatedAt" />
        <p v-if="notice" role="status" class="text-sm text-emerald-600 dark:text-emerald-400">{{ notice }}</p>
        <p v-if="error || catalogError" role="alert" class="text-sm text-red-600">{{ error || catalogError }}</p>
      </div>
    </template>
    <template #table>
      <DataTable column-order-storage-key="default-pricing-column-order" :columns="columns" :data="items" :loading="loading">
        <template #cell-platform="{ row }"><PlatformBadge :platform="row.platform" /></template>
        <template #cell-billing_mode="{ row }"><BillingModeBadge :mode="row.billing_mode" /></template>
        <template #cell-price="{ row }">
          <span v-if="row.price_status === 'unpriced'">{{ t('admin.pricing.defaults.unpriced') }}</span>
          <div v-else class="space-y-1">
            <div v-for="price in row.prices.slice(0, 2)" :key="price.key" class="text-sm">{{ priceLabel(price) }}: {{ formatPrice(price) }}</div>
          </div>
        </template>
        <template #cell-actions="{ row }">
          <button type="button" class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-primary-600 dark:hover:bg-dark-700 dark:hover:text-primary-400" @click="selected = row">
            <Icon name="eye" size="sm" />
            <span class="text-xs">{{ t('admin.pricing.defaults.details') }}</span>
          </button>
        </template>
        <template #empty><p class="p-6 text-center text-sm text-gray-500">{{ t('admin.pricing.defaults.empty') }}</p></template>
      </DataTable>
    </template>
    <template #pagination>
      <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="page = $event" @update:page-size="pageSize = $event; page = 1" />
    </template>
  </TablePageLayout>
  <BaseDialog :show="!!selected" :title="selected?.model || ''" @close="selected = null">
    <div v-if="selected" class="space-y-4">
      <!-- 平台与计费方式徽章：与表格列共用同一组件。 -->
      <div class="flex flex-wrap items-center gap-2">
        <PlatformBadge :platform="selected.platform" />
        <BillingModeBadge :mode="selected.billing_mode" />
        <span v-if="selected.source" class="text-xs text-gray-500">{{ t(`admin.modelAttributes.sources.${selected.source}`, selected.source) }}</span>
      </div>
      <p v-if="selected.price_status === 'unpriced'" class="text-sm text-gray-400 dark:text-dark-500">{{ t('admin.pricing.defaults.unpriced') }}</p>
      <template v-else>
        <!-- 上下文与模式是两个独立开关，与模型广场定价面板同款；价格行展示当前组合应用后的单价。 -->
        <div v-if="availableContexts.length > 1 || availableTiers.length > 1" class="flex flex-wrap items-center justify-end gap-2">
          <div v-segmented v-if="availableContexts.length > 1" class="segmented max-w-full flex-wrap" data-testid="pricing-context-switch">
            <button
              v-for="context in availableContexts"
              :key="context"
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': context === activeContext }"
              @click="selectedContext = context"
            >
              {{ contextRangeLabel(context) }}
            </button>
          </div>
          <div v-segmented v-if="availableTiers.length > 1" class="segmented max-w-full flex-wrap" data-testid="pricing-tier-switch">
            <button
              v-for="tier in availableTiers"
              :key="tier"
              type="button"
              class="segmented-item px-2 py-0.5 text-xs font-semibold"
              :class="{ 'segmented-item-active': tier === activeTier }"
              @click="selectedTier = tier"
            >
              {{ t(`admin.pricing.defaults.tiers.${tier}`) }}
            </button>
          </div>
        </div>
        <!-- 价格行与模型广场卡片定价一致：弱化标签、等宽数字、细分隔线。 -->
        <dl class="space-y-2.5">
          <div v-for="price in activePrices" :key="price.key" class="flex items-baseline justify-between gap-3 border-b border-gray-100 pb-2 text-sm dark:border-dark-700">
            <dt class="min-w-0 max-w-[45%] shrink-0 break-words text-gray-500 dark:text-dark-400">{{ priceLabel(price) }}<span v-if="selected.price_sources?.[price.key.replace(/^(?:fast_|flex_)/, '')]" class="ml-1 text-xs">({{ t(`admin.modelAttributes.sources.${selected.price_sources[price.key.replace(/^(?:fast_|flex_)/, '')]}`) }})</span></dt>
            <dd class="min-w-0 break-words text-right font-medium tabular-nums [overflow-wrap:anywhere] text-gray-900 dark:text-white">{{ formatPrice(price) }}</dd>
          </div>
        </dl>
      </template>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { vSegmented } from '@/directives/segmented'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useDebounceFn } from '@vueuse/core'
import { listDefaultPricing, updateDefaultPricing, type DefaultModelPrice, type DefaultPriceValue } from '@/api/admin/pricing'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Select from '@/components/common/Select.vue'
import FilterDropdown from '@/components/common/FilterDropdown.vue'
import FilterField from '@/components/common/FilterField.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import PlatformBadge from '@/components/common/PlatformBadge.vue'
import BillingModeBadge from '@/components/common/BillingModeBadge.vue'
import ModelCatalogInfo from '@/components/admin/ModelCatalogInfo.vue'
import Icon from '@/components/icons/Icon.vue'
import { formatCompactTokenRange } from '@/utils/formatters'
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'

// DefaultPricingPanel.vue lists catalog prices and their available unit dimensions.
// Detail controls select token context and service tiers, while video records
// show their complete resolution table. Media labels remain independent of token
// prefixes because administrators can configure custom resolution names.

const { t } = useI18n()
const items = ref<DefaultModelPrice[]>([])
const selected = ref<DefaultModelPrice | null>(null)
const search = ref('')
const platform = ref('')
const mode = ref('')
const page = ref(1)
const pageSize = ref(20)
const total = ref(0)
const loading = ref(false)
const updating = ref(false)
const notice = ref('')
const activeFilterCount = computed(() => Number(!!platform.value) + Number(!!mode.value))
const error = ref('')
const updatedAt = ref('')
const catalogVersion = ref('')
const catalogError = ref('')
let controller: AbortController | undefined
let updateController: AbortController | undefined
let disposed = false
const platforms = ref<string[]>([])
const platformOptions = computed(() => [{ value: '', label: t('admin.pricing.defaults.allPlatforms') }, ...platforms.value.map(value => ({ value, label: t(`admin.groups.platforms.${value}`, value) }))])
const modeOptions = computed(() => [{ value: '', label: t('admin.pricing.defaults.allModes') }, ...['token', 'image', 'video', 'per_request'].map(value => ({ value, label: t(`admin.pricing.defaults.modes.${value}`) }))])
const columns = computed(() => ['model', 'platform', 'billing_mode', 'price', 'actions'].map(key => ({ key, label: t(`admin.pricing.defaults.columns.${key}`) })))
/**
 * priceLabel displays media dimensions verbatim and translates token bucket names.
 * Video dimensions may start with token modifier prefixes, so their USD/s unit
 * identifies labels that must remain intact in both summaries and details.
 */
function priceLabel(price: DefaultPriceValue): string {
  if (price.unit === 'USD/s') return price.key
  const stripped = price.key.replace(/^(?:long_)?(?:fast_|flex_)?/, '')
  return t(`admin.pricing.defaults.keys.${stripped}`, stripped)
}

// 弹窗价格开关：上下文（标准/长上下文）与模式（标准/Fast/Flex）相互独立。
// 后端返回的 key 形如 long_fast_input（上下文前缀在前），两个开关组合出当前价格集。

type PricingContext = 'standard' | 'long_context' | number
type PricingTier = 'standard' | 'fast' | 'flex'
const selectedContext = ref<PricingContext>('standard')
const selectedTier = ref<PricingTier>('standard')
function comboPrefix(context: PricingContext, tier: PricingTier): string {
  return (context === 'long_context' ? 'long_' : '') + (tier === 'fast' ? 'fast_' : tier === 'flex' ? 'flex_' : '')
}
function comboOf(key: string): { context: PricingContext; tier: PricingTier } {
  const context: PricingContext = key.startsWith('long_') ? 'long_context' : 'standard'
  const rest = context === 'long_context' ? key.slice('long_'.length) : key
  const tier: PricingTier = rest.startsWith('fast_') ? 'fast' : rest.startsWith('flex_') ? 'flex' : 'standard'
  return { context, tier }
}
// availableContexts lists token ranges while video prices retain a single view.
const availableContexts = computed<PricingContext[]>(() => {
  if (selected.value?.billing_mode === 'video') return ['standard']
  if (selected.value?.context_intervals?.length) return selected.value.context_intervals.map((_, index) => index)
  if (!selected.value?.prices.some(price => comboOf(price.key).context === 'long_context')) return ['standard']
  return ['standard', 'long_context']
})
// availableTiers exposes service tiers only where token prices define them.
const availableTiers = computed<PricingTier[]>(() => {
  if (!selected.value || selected.value.billing_mode === 'video') return ['standard']
  const present = new Set(selected.value.prices.map(price => comboOf(price.key).tier))
  return (['standard', 'fast', 'flex'] as PricingTier[]).filter(tier => tier === 'standard' || present.has(tier))
})
const activeContext = computed<PricingContext>(() => availableContexts.value.includes(selectedContext.value) ? selectedContext.value : availableContexts.value[0] ?? 'standard')
const activeTier = computed<PricingTier>(() => availableTiers.value.includes(selectedTier.value) ? selectedTier.value : 'standard')
// 上下文开关使用与模型广场一致的紧凑区间文案：0-272k / 272k+。
function contextRangeLabel(context: PricingContext): string {
  if (typeof context === 'number') {
    const interval = selected.value?.context_intervals?.[context]
    return interval ? formatCompactTokenRange(interval.min_tokens, interval.max_tokens) : ''
  }
  const threshold = selected.value?.long_context_threshold ?? 0
  return context === 'long_context' ? formatCompactTokenRange(threshold, null) : formatCompactTokenRange(0, threshold)
}
// activePrices selects token modifiers or every available video resolution quote.
const activePrices = computed(() => {
  if (selected.value?.billing_mode === 'video') return selected.value.prices
  const prefix = comboPrefix(activeContext.value, activeTier.value)
  const prices = typeof activeContext.value === 'number' ? selected.value?.context_intervals?.[activeContext.value]?.prices : selected.value?.prices
  return (prices ?? []).filter(price => {
    if (!price.key.startsWith(prefix)) return false
    // 排除更长的组合前缀，保证每个组合只匹配自己的价格集。
    return !/^(?:long_|fast_|flex_)/.test(price.key.slice(prefix.length))
  })
})
watch(selected, () => { selectedContext.value = 'standard'; selectedTier.value = 'standard' })
function formatPrice(price: DefaultPriceValue): string {
  if (price.value == null) return t('admin.pricing.defaults.notApplicable')
  const unit = price.unit === 'multiplier' ? '×' : price.unit
  return `${price.value.toLocaleString(undefined, { maximumSignificantDigits: 8 })} ${unit}`
}
async function load() {
  if (disposed) return
  controller?.abort()
  const current = new AbortController()
  controller = current
  loading.value = true
  error.value = ''
  try {
    const result = await listDefaultPricing({ page: page.value, page_size: pageSize.value, search: search.value, platform: platform.value, billing_mode: mode.value }, current.signal)
    if (current.signal.aborted) return
    items.value = result.items
    total.value = result.total
    updatedAt.value = result.last_updated
    catalogVersion.value = result.version ?? ''
    catalogError.value = result.last_error ?? ''
    platforms.value = result.platforms ?? platforms.value
  } catch {
    if (!current.signal.aborted) error.value = t('admin.pricing.loadError')
  } finally { if (!current.signal.aborted) loading.value = false }
}

// 更新与只读刷新分开，更新成功后保留当前筛选条件重新查询一次。
async function updateCatalog() {
  if (updating.value) return
  updating.value = true
  error.value = ''
  notice.value = ''
  const current = new AbortController()
  updateController = current
  try {
    await updateDefaultPricing(current.signal)
    if (disposed || current.signal.aborted) return
    notice.value = t('admin.pricing.defaults.updateSuccess')
    await load()
  } catch {
    if (!disposed && !current.signal.aborted) error.value = t('admin.pricing.defaults.updateError')
  } finally {
    updating.value = false
  }
}

const searchChanged = useDebounceFn(() => { if (page.value !== 1) page.value = 1; else void load() }, SEARCH_DEBOUNCE_MS)
watch(search, searchChanged)
watch([platform, mode], () => { if (page.value !== 1) page.value = 1; else void load() })
watch([page, pageSize], load, { immediate: true })
onBeforeUnmount(() => {
  disposed = true
  controller?.abort()
  updateController?.abort()
})
</script>
