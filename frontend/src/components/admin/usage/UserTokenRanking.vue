<template>
  <!-- The parent usage card supplies this ranking's surface and shared filters; this component owns its rows and limit selector. -->
  <div>
    <!-- Ranking description and result limit -->
    <div
      class="flex flex-wrap items-center justify-between gap-3 border-b border-gray-100 px-4 py-3 dark:border-dark-700/50 sm:px-6"
    >
      <p class="text-xs text-gray-400 dark:text-gray-500">{{
        t('admin.usage.tokenRanking.subtitle')
      }}</p>
      <div class="flex items-center gap-3">
        <span v-if="!loading && items.length > 0" class="text-xs text-gray-400 dark:text-gray-500">
          {{ t('admin.usage.tokenRanking.userCount', { count: items.length }) }}
        </span>
        <div class="w-28">
          <Select v-model="limit" :options="limitOptions" @change="load" />
        </div>
      </div>
    </div>

    <!-- User token ranking table -->
    <div class="overflow-x-auto">
      <table
        :aria-busy="loading"
        class="w-full min-w-max divide-y divide-gray-200 dark:divide-dark-700"
      >
        <thead class="bg-gray-50 dark:bg-dark-800">
          <tr>
            <th
              class="w-16 px-4 py-3 text-left text-xs font-medium tracking-wider text-gray-500 dark:text-dark-400 sm:px-6"
              >#</th
            >
            <th
              class="px-4 py-3 text-left text-xs font-medium tracking-wider text-gray-500 dark:text-dark-400"
            >
              {{ t('admin.usage.tokenRanking.columns.user') }}
            </th>
            <th
              v-for="col in sortableColumns"
              :key="col.key"
              class="cursor-pointer select-none whitespace-nowrap px-4 py-3 text-right text-xs font-medium uppercase tracking-wider transition-colors hover:bg-gray-100 dark:hover:bg-dark-700"
              :class="
                sortBy === col.key
                  ? 'text-primary-600 dark:text-primary-400'
                  : 'text-gray-500 dark:text-dark-400'
              "
              @click="setSort(col.key)"
            >
              {{ t(col.label) }}
              <span v-if="sortBy === col.key" aria-hidden="true">↓</span>
            </th>
          </tr>
        </thead>
        <TableSkeletonBody v-if="loading" :columns="sortableColumns.length + 2" />
        <tbody
          v-else
          class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900"
        >
          <tr v-if="items.length === 0">
            <td
              :colspan="sortableColumns.length + 2"
              class="py-12 text-center text-sm text-gray-400"
            >
              {{ t('admin.dashboard.noDataAvailable') }}
            </td>
          </tr>
          <tr
            v-for="(item, index) in items"
            v-else
            :key="item.user_id"
            class="cursor-pointer transition-colors hover:bg-gray-50 dark:hover:bg-dark-700/40"
            :title="t('admin.usage.tokenRanking.rowHint')"
            @click="$emit('select-user', item.user_id, item.email)"
          >
            <td class="px-4 py-3 sm:px-6">
              <span
                v-if="index < 3"
                class="inline-flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold"
                :class="RANK_BADGE_CLASSES[index]"
                >{{ index + 1 }}</span
              >
              <span
                v-else
                class="inline-block w-6 text-center text-sm tabular-nums text-gray-400"
                >{{ index + 1 }}</span
              >
            </td>
            <td
              class="max-w-[260px] truncate px-4 py-3 text-sm font-medium text-gray-700 dark:text-gray-200"
              :title="item.email"
            >
              {{ item.email || `User #${item.user_id}` }}
              <span class="ml-1 font-normal text-gray-400 dark:text-gray-500"
                >#{{ item.user_id }}</span
              >
            </td>
            <td
              class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400"
              >{{ item.requests.toLocaleString() }}</td
            >
            <td
              class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400"
              >{{ fmtTokens(item.input_tokens) }}</td
            >
            <td
              class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400"
              >{{ fmtTokens(item.output_tokens) }}</td
            >
            <td
              class="whitespace-nowrap px-4 py-3 text-right text-sm tabular-nums text-gray-500 dark:text-gray-400"
              >{{ fmtTokens(item.cache_tokens) }}</td
            >
            <td
              class="whitespace-nowrap px-4 py-3 text-right text-sm font-medium tabular-nums text-gray-900 dark:text-gray-100"
              >{{ fmtTokens(item.total_tokens) }}</td
            >
            <td
              class="whitespace-nowrap px-4 py-3 text-right text-sm font-medium tabular-nums text-green-600 dark:text-green-400"
              >{{ fmtCost(item.actual_cost) }}</td
            >
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<script setup lang="ts">
import TableSkeletonBody from '@/components/common/TableSkeletonBody.vue'
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { getUserBreakdown, type UserBreakdownParams } from '@/api/admin/dashboard'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatCompactNumber } from '@/utils/format'
import type { UserBreakdownItem } from '@/api/admin/dashboard.types'
import Select from '@/components/common/Select.vue'

// UserTokenRanking.vue loads administrator user rankings for shared usage filters and a
// selected metric. User selections emit both identity and email so the usage page can
// filter records and fill its search label. Displayed cost is the user charge in the
// configured balance unit.
//
// Each load receives a monotonically increasing sequence. The newest started request alone
// can replace rows, clear them after failure, or release the loading flag. Changing filters
// or sort therefore keeps late responses from overwriting newer rankings, while existing
// rows remain present beneath the loading presentation until that request finishes.

// props supplies the date range and shared filters combined with this component's ranking
// sort and limit.
const props = defineProps<{
  startDate: string // beginning calendar date
  endDate: string // ending calendar date
  // filters supplies shared query predicates; date, sort, limit, and a nonempty model prop
  // override matching entries.
  filters: Record<string, unknown>
  model?: string // optional overriding model filter
}>()

defineEmits<{
  // select-user publishes row identity and its readable email for parent filter selection.
  (e: 'select-user', userId: number, email: string): void
}>()

// Translation and balance formatting keep ranking labels and user charge amounts consistent
// with the surrounding usage page.
const { t } = useI18n()
const { formatBalanceAmount } = useBalanceDisplay()

// SortKey selects a server-supported metric for descending user ranking.
type SortKey = NonNullable<UserBreakdownParams['sort_by']>
// sortableColumns maps ranking metrics to translated headers; each key is also accepted by
// the breakdown API.
const sortableColumns: {
  key: SortKey // server ranking metric key
  label: string // translated table header key
}[] = [
  { key: 'requests', label: 'admin.usage.tokenRanking.columns.requests' },
  { key: 'input_tokens', label: 'admin.usage.tokenRanking.columns.inputTokens' },
  { key: 'output_tokens', label: 'admin.usage.tokenRanking.columns.outputTokens' },
  { key: 'cache_tokens', label: 'admin.usage.tokenRanking.columns.cacheTokens' },
  { key: 'total_tokens', label: 'admin.usage.tokenRanking.columns.totalTokens' },
  { key: 'actual_cost', label: 'admin.usage.tokenRanking.columns.cost' },
]

// limitOptions exposes the supported top-user counts chosen by the ranking limit selector
// in this table.
const limitOptions = [
  { value: 20, label: 'Top 20' },
  { value: 50, label: 'Top 50' },
  { value: 100, label: 'Top 100' },
  { value: 200, label: 'Top 200' },
]

// RANK_BADGE_CLASSES assigns gold, silver, and bronze styling to the first three displayed
// rank positions.
const RANK_BADGE_CLASSES = [
  'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-400',
  'bg-gray-200 text-gray-600 dark:bg-gray-500/20 dark:text-gray-300',
  'bg-orange-100 text-orange-700 dark:bg-orange-500/20 dark:text-orange-400',
]

// Ranking state holds displayed users, loading progress, the selected sort metric, and the
// result limit for requests.
const items = ref<UserBreakdownItem[]>([])
const loading = ref(false)
const sortBy = ref<SortKey>('total_tokens')
const limit = ref(50)
// reqSeq identifies the newest ranking load whose response may replace rows or finish
// loading.
let reqSeq = 0

// fmtTokens formats ranking token counts with the shared compact number formatter.
const fmtTokens = (v: number) => formatCompactNumber(v)
// fmtCost formats actual user charges with four decimal places and the configured balance
// unit.
const fmtCost = (v: number) => formatBalanceAmount(v, { fractionDigits: 4 })

// setSort reloads the ranking when a different metric is selected. Selecting the active
// metric does nothing, keeping the current rows and avoiding a duplicate breakdown request.
const setSort = (key: SortKey) => {
  if (sortBy.value === key) return
  sortBy.value = key
  load()
}

// load retrieves users using shared filters, selected dates, sort metric, and limit. A
// nonempty model prop overrides a model already present in the shared filter object.
//
// The request sequence advances before fetching, and responses compare it before publishing
// rows. The same comparison protects the error path and loading cleanup, so an older
// failure cannot erase newer users or hide the newest loading indicator. A current failure
// produces an empty ranking.
const load = async () => {
  // Capture ranking ownership before awaiting users so successful rows and
  // loading cleanup belong to the latest requested ranking.
  const seq = ++reqSeq
  loading.value = true
  try {
    const params: UserBreakdownParams = {
      ...props.filters,
      start_date: props.startDate,
      end_date: props.endDate,
      sort_by: sortBy.value,
      limit: limit.value,
    }
    if (props.model) params.model = props.model
    const res = await getUserBreakdown(params)
    if (seq !== reqSeq) return
    items.value = res.users || []
  } catch {
    // An obsolete rejection leaves current users untouched; a current failure clears rows
    // so the ranking displays its existing empty state.
    if (seq !== reqSeq) return
    items.value = []
  } finally {
    if (seq === reqSeq) loading.value = false
  }
}

// Reload the ranking when shared filters, either date endpoint, or the selected model changes.
watch(
  () => [props.startDate, props.endDate, props.model, JSON.stringify(props.filters)],
  () => load(),
  { immediate: true },
)

defineExpose({ reload: load })
</script>
