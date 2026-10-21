<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-col gap-2 sm:flex-row sm:items-center">
          <div class="input-icon-wrap min-w-0 w-full flex-1 sm:w-64 sm:flex-none sm:max-w-none">
            <Icon name="search" size="md" class="input-icon text-gray-400" />
            <input
              v-model="filters.search"
              type="text"
              class="input input-has-icon"
              :placeholder="t('admin.affiliates.records.searchPlaceholder')"
              @input="debounceLoad"
            />
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <DateRangePicker
              v-model:start-date="dateRangeStart"
              v-model:end-date="dateRangeEnd"
              @change="handleDateRangeChange"
            />
            <button
              class="btn btn-secondary shrink-0 btn-icon"
              :disabled="loading"
              :title="t('common.refresh')"
              @click="loadRecords"
            >
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          :columns="columns"
          :data="records"
          :loading="loading"
          :server-side-sort="true"
          default-sort-key="created_at"
          default-sort-order="desc"
          :sort-storage-key="sortStorageKey"
          :column-order-storage-key="`${sortStorageKey}-columns`"
          @sort="handleSort"
        >
          <template #cell-inviter="{ row }">
            <UserCell
              :id="row.inviter_id"
              :email="row.inviter_email"
              :username="row.inviter_username"
              :clickable="props.type !== 'transfers'"
              @open="openUserOverview"
            />
          </template>
          <template #cell-invitee="{ row }">
            <UserCell
              :id="row.invitee_id"
              :email="row.invitee_email"
              :username="row.invitee_username"
              :clickable="props.type !== 'transfers'"
              @open="openUserOverview"
            />
          </template>
          <template #cell-user="{ row }">
            <UserCell
              :id="row.user_id"
              :email="row.user_email"
              :username="row.username"
              :clickable="true"
              @open="openUserOverview"
            />
          </template>
          <template #cell-aff_code="{ row }">
            <span class="font-mono text-sm text-gray-700 dark:text-gray-300">{{
              row.aff_code || '-'
            }}</span>
          </template>
          <template #cell-order="{ row }">
            <div class="space-y-0.5">
              <div class="font-mono text-sm text-gray-900 dark:text-white">#{{ row.order_id }}</div>
              <div class="max-w-56 truncate text-sm text-gray-500 dark:text-dark-400">{{
                row.out_trade_no
              }}</div>
            </div>
          </template>
          <template #cell-payment_type="{ row }">
            {{ t('payment.methods.' + row.payment_type, row.payment_type || '-') }}
          </template>
          <template #cell-order_status="{ row }">
            <OrderStatusBadge :status="row.order_status" />
          </template>
          <template #cell-total_rebate="{ row }">
            <AmountText :value="row.total_rebate" />
          </template>
          <template #cell-order_amount="{ row }">
            <span class="text-sm text-gray-900 dark:text-white">{{
              formatAffiliateOrderAmount(row)
            }}</span>
          </template>
          <template #cell-pay_amount="{ row }">
            <span class="text-sm text-gray-900 dark:text-white">{{
              formatPaymentAmount(row.pay_amount, row.pay_currency)
            }}</span>
          </template>
          <template #cell-rebate_amount="{ row }">
            <AmountText :value="row.rebate_amount" strong />
          </template>
          <template #cell-amount="{ row }">
            <AmountText :value="row.amount" strong />
          </template>
          <template #cell-balance_after="{ row }">
            <NullableAmountText :value="row.balance_after" />
          </template>
          <template #cell-available_quota_after="{ row }">
            <NullableAmountText :value="row.available_quota_after" />
          </template>
          <template #cell-frozen_quota_after="{ row }">
            <NullableAmountText :value="row.frozen_quota_after" />
          </template>
          <template #cell-history_quota_after="{ row }">
            <NullableAmountText :value="row.history_quota_after" />
          </template>
          <template #cell-created_at="{ row }">
            <span class="text-sm text-gray-700 dark:text-gray-300">{{
              formatDateTime(row.created_at)
            }}</span>
          </template>
        </DataTable>
      </template>

      <template #pagination>
        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </template>
    </TablePageLayout>

    <BaseDialog
      :show="overviewDialog"
      :title="t('admin.affiliates.overview.title')"
      width="normal"
      @close="overviewDialog = false"
    >
      <ContentSkeleton v-if="overviewLoading" variant="detail" :rows="6" class="py-4" />
      <div v-else-if="selectedOverview" class="space-y-4">
        <div
          class="rounded-surface border border-gray-100 bg-gray-50 p-4 dark:border-dark-700 dark:bg-dark-800"
        >
          <div class="font-mono text-sm text-gray-900 dark:text-white"
            >#{{ selectedOverview.user_id }}</div
          >
          <div class="mt-1 text-sm font-medium text-gray-900 dark:text-white">{{
            selectedOverview.email || '-'
          }}</div>
          <div class="mt-0.5 text-sm text-gray-500 dark:text-dark-400">{{
            selectedOverview.username || '-'
          }}</div>
        </div>
        <div class="grid gap-3 sm:grid-cols-2">
          <OverviewStat
            :label="t('admin.affiliates.overview.affCode')"
            :value="selectedOverview.aff_code || '-'"
            mono
          />
          <OverviewStat
            :label="t('admin.affiliates.overview.rebateRate')"
            :value="formatPercent(selectedOverview.rebate_rate_percent)"
          />
          <OverviewStat
            :label="t('admin.affiliates.overview.invitedCount')"
            :value="String(selectedOverview.invited_count)"
          />
          <OverviewStat
            :label="t('admin.affiliates.overview.rebatedInviteeCount')"
            :value="String(selectedOverview.rebated_invitee_count)"
          />
          <OverviewStat
            :label="t('admin.affiliates.overview.availableQuota', { unitName: balanceUnitName })"
            :value="formatAffiliateAmount(selectedOverview.available_quota)"
          />
          <OverviewStat
            :label="t('admin.affiliates.overview.historyQuota', { unitName: balanceUnitName })"
            :value="formatAffiliateAmount(selectedOverview.history_quota)"
          />
        </div>
      </div>
    </BaseDialog>
  </AppLayout>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { computed, defineComponent, h, onMounted, reactive, ref, type PropType } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/table/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import OrderStatusBadge from '@/components/payment/OrderStatusBadge.vue'
import type { Column } from '@/components/common/table/types'
import { useAppStore } from '@/stores/app'
import {
  affiliatesAPI,
  type AffiliateInviteRecord,
  type AffiliateRebateRecord,
  type AffiliateTransferRecord,
  type AffiliateUserOverview,
  type ListAffiliateRecordsParams,
} from '@/api/admin/affiliates'
import type { PaginatedResponse } from '@/api/common.types'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { formatPaymentAmount } from '@/utils/payment/currency'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime as formatDisplayDateTime } from '@/utils/format'

// AdminAffiliateRecordsTable.vue displays invitation, rebate, or transfer records using
// the endpoint selected by its type prop. Column choices and saved sort keys belong to
// that record type, and user cells can open an affiliate account overview.
//
// Monetary display distinguishes account balance units from payment currencies.
// Invitation rebates, transfer amounts, and balance-order amounts use the configured
// balance formatter, while subscription order prices and payment amounts retain their
// currency formatter. Historical transfer snapshots can be absent, so their amount
// cells render a dash for nullish values and format an actual zero as a value. Search,
// dates, and pagination build a shared query shape for the three endpoints.

// RecordType selects the affiliate endpoint and table column family.
type RecordType = 'invites' | 'rebates' | 'transfers'
// AffiliateRecord represents a row returned by one affiliate records endpoint.
type AffiliateRecord = AffiliateInviteRecord | AffiliateRebateRecord | AffiliateTransferRecord

// The required record type chooses endpoint dispatch, visible columns, and the
// browser-storage namespace for sorting preferences.
const props = defineProps<{
  type: RecordType // selected record kind
}>()

// Translation, notifications, and balance formatting supply the shared record table and
// its optional affiliate overview dialog.
const { t } = useI18n()
const appStore = useAppStore()
const { balanceUnitName, formatBalanceAmount } = useBalanceDisplay()
// List rows, search/date filters, pagination, and dialog state track the table and
// independently loaded user overview.
const loading = ref(false)
const records = ref<AffiliateRecord[]>([])
const filters = reactive({
  search: '', // record search text
  start_at: '', // first record date
  end_at: '', // last record date
})
const dateRangeStart = ref('')
const dateRangeEnd = ref('')
const pagination = reactive({
  page: 1, // requested page number
  page_size: 20, // requested row count
  total: 0, // matching row count
})
const overviewDialog = ref(false)
const overviewLoading = ref(false)
const selectedOverview = ref<AffiliateUserOverview | null>(null)
let debounceTimer: ReturnType<typeof setTimeout> | null = null

// columns selects invitation, rebate, or transfer fields and marks the sorting keys
// supported by the chosen record endpoint.
const columns = computed<Column[]>(() => {
  if (props.type === 'invites') {
    return [
      { key: 'inviter', label: t('admin.affiliates.records.inviter'), sortable: true },
      { key: 'invitee', label: t('admin.affiliates.records.invitee'), sortable: true },
      { key: 'aff_code', label: t('admin.affiliates.records.affCode'), sortable: true },
      {
        key: 'total_rebate',
        label: t('admin.affiliates.records.totalRebate', { unitName: balanceUnitName.value }),
        sortable: true,
      },
      { key: 'created_at', label: t('admin.affiliates.records.invitedAt'), sortable: true },
    ]
  }
  if (props.type === 'rebates') {
    return [
      { key: 'order', label: t('admin.affiliates.records.order'), sortable: true },
      { key: 'inviter', label: t('admin.affiliates.records.inviter'), sortable: true },
      { key: 'invitee', label: t('admin.affiliates.records.invitee'), sortable: true },
      { key: 'order_amount', label: t('admin.affiliates.records.orderAmount'), sortable: true },
      { key: 'pay_amount', label: t('admin.affiliates.records.payAmount'), sortable: true },
      {
        key: 'rebate_amount',
        label: t('admin.affiliates.records.rebateAmount', { unitName: balanceUnitName.value }),
      },
      { key: 'payment_type', label: t('admin.affiliates.records.paymentType'), sortable: true },
      { key: 'order_status', label: t('admin.affiliates.records.orderStatus'), sortable: true },
      { key: 'created_at', label: t('admin.affiliates.records.rebatedAt'), sortable: true },
    ]
  }
  return [
    { key: 'user', label: t('admin.affiliates.records.user'), sortable: true },
    {
      key: 'amount',
      label: t('admin.affiliates.records.transferAmount', { unitName: balanceUnitName.value }),
      sortable: true,
    },
    {
      key: 'balance_after',
      label: t('admin.affiliates.records.balanceAfter', { unitName: balanceUnitName.value }),
      sortable: true,
    },
    {
      key: 'available_quota_after',
      label: t('admin.affiliates.records.availableQuotaAfter', { unitName: balanceUnitName.value }),
      sortable: true,
    },
    {
      key: 'frozen_quota_after',
      label: t('admin.affiliates.records.frozenQuotaAfter', { unitName: balanceUnitName.value }),
      sortable: true,
    },
    {
      key: 'history_quota_after',
      label: t('admin.affiliates.records.historyQuotaAfter', { unitName: balanceUnitName.value }),
      sortable: true,
    },
    { key: 'created_at', label: t('admin.affiliates.records.transferredAt'), sortable: true },
  ]
})

// sortStorageKey isolates sorting preferences for each affiliate record type displayed
// through the shared table component.
const sortStorageKey = computed(() => `admin-affiliate-${props.type}-table-sort`)

// loadInitialSortState restores a sortable column recognized by the current record
// type. Missing or invalid preferences use descending creation time, and an accepted
// key uses ascending only for the exact stored asc value.
function loadInitialSortState(): {
  sort_by: string // server sorting field
  sort_order: 'asc' | 'desc' // server sorting direction
} {
  const fallback = { sort_by: 'created_at', sort_order: 'desc' as 'asc' | 'desc' }
  try {
    const raw = localStorage.getItem(sortStorageKey.value)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as {
      key?: string // selected column key
      order?: string // selected ordering direction
    }
    const key = typeof parsed.key === 'string' ? parsed.key : ''
    // Validate persisted ordering against the current record type so a column from
    // another table cannot become an unsupported endpoint sort key.
    if (!columns.value.some((column) => column.key === key && column.sortable)) return fallback
    return {
      sort_by: key,
      sort_order: parsed.order === 'asc' ? 'asc' : 'desc',
    }
  } catch {
    // Unreadable sort preferences use descending creation time so the record table
    // can still issue a supported initial query.
    return fallback
  }
}

// sortState holds validated persisted ordering and is replaced by the DataTable sort
// event before record reloads.
const sortState = reactive(loadInitialSortState())

// userTimezone returns the browser's resolved timezone for date-range interpretation by
// the endpoint. If Intl resolution throws, UTC is supplied so record queries still
// contain a usable timezone identifier for the request.
function userTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    // Use UTC when the browser cannot resolve its timezone so the endpoint still
    // receives a concrete zone for date-range interpretation.
    return 'UTC'
  }
}

// buildParams combines pagination, trimmed search, date filters, sort, and browser
// timezone. Empty search and date values become undefined so the selected endpoint
// receives optional conditions for the current record list.
function buildParams(): ListAffiliateRecordsParams {
  return {
    page: pagination.page,
    page_size: pagination.page_size,
    search: filters.search.trim() || undefined,
    start_at: filters.start_at || undefined,
    end_at: filters.end_at || undefined,
    sort_by: sortState.sort_by,
    sort_order: sortState.sort_order,
    timezone: userTimezone(),
  }
}

// fetchRecords dispatches record queries to the endpoint selected by the type prop.
async function fetchRecords(
  params: ListAffiliateRecordsParams,
): Promise<PaginatedResponse<AffiliateRecord>> {
  if (props.type === 'invites') {
    return affiliatesAPI.listInviteRecords(params)
  }
  if (props.type === 'rebates') {
    return affiliatesAPI.listRebateRecords(params)
  }
  return affiliatesAPI.listTransferRecords(params)
}

// loadRecords replaces rows and total from the selected record endpoint. Missing
// response collections become empty rows, and request failure shows a translated error
// while leaving the previously displayed records available.
async function loadRecords() {
  loading.value = true
  try {
    const res = await fetchRecords(buildParams())
    // Missing response collections represent an empty page here, so initialize rows
    // and total together before the table leaves its loading state.
    records.value = res.items || []
    pagination.total = res.total || 0
  } catch (error) {
    appStore.showError(
      extractI18nErrorMessage(error, t, 'admin.affiliates.errors', t('common.error')),
    )
  } finally {
    loading.value = false
  }
}

// debounceLoad replaces pending search work with a delayed first-page reload. The timer
// belongs to this component instance and is replaced on subsequent input; the callback
// reads current filter values when it runs.
function debounceLoad() {
  if (debounceTimer) clearTimeout(debounceTimer)
  debounceTimer = setTimeout(() => reloadFromFirstPage(), 300)
}

// handleDateRangeChange applies picker dates and reloads records from the first page.
function handleDateRangeChange(range: {
  startDate: string // first selected date
  endDate: string // last selected date
  preset: string | null // selected date preset
}) {
  filters.start_at = range.startDate
  filters.end_at = range.endDate
  reloadFromFirstPage()
}

// reloadFromFirstPage resets pagination before starting a fresh affiliate record
// request.
function reloadFromFirstPage() {
  pagination.page = 1
  void loadRecords()
}

// handlePageChange selects the requested page and starts its affiliate record load.
function handlePageChange(page: number) {
  pagination.page = page
  void loadRecords()
}

// handlePageSizeChange applies a new row count and requests the first page.
function handlePageSizeChange(size: number) {
  pagination.page_size = size
  pagination.page = 1
  void loadRecords()
}

// handleSort updates server ordering and reloads affiliate records from page one.
function handleSort(key: string, order: 'asc' | 'desc') {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  void loadRecords()
}

// formatAffiliateAmount renders an affiliate balance value using the configured account
// unit.
function formatAffiliateAmount(value: number | null | undefined): string {
  return formatBalanceAmount(value)
}

// formatAffiliateOrderAmount selects account units for balance orders and currency for
// subscriptions. Balance-order amounts describe credited units, while other order
// amounts retain their order currency when passed to the payment formatter.
function formatAffiliateOrderAmount(row: AffiliateRebateRecord): string {
  if (row.order_type === 'balance') {
    return formatAffiliateAmount(row.order_amount)
  }
  return formatPaymentAmount(row.order_amount, row.order_currency)
}

// formatPercent renders a percentage rounded to at most two decimal places. Nullish or
// otherwise falsy values become zero before conversion, and the result appends the
// percent sign for the overview.
function formatPercent(value: number | null | undefined): string {
  // Round percentages to two decimal places before displaying the overview label.
  const roundedValue = Math.round(Number(value || 0) * 100) / 100
  return `${Number.isInteger(roundedValue) ? roundedValue.toString() : roundedValue.toString()}%`
}

// formatDateTime formats a populated record timestamp or returns a dash. Empty and
// nullish values use that placeholder so rows without a timestamp remain readable in
// the shared records table.
function formatDateTime(value: string | null | undefined): string {
  return value ? formatDisplayDateTime(value) : '-'
}

// openUserOverview opens a cleared overview dialog and requests one user's affiliate
// summary. Zero identifiers are ignored; request failure closes the dialog and reports
// an error, and loading ends when the request settles.
async function openUserOverview(userId: number) {
  if (!userId) return
  overviewDialog.value = true
  overviewLoading.value = true
  selectedOverview.value = null
  try {
    selectedOverview.value = await affiliatesAPI.getUserOverview(userId)
  } catch (error) {
    overviewDialog.value = false
    appStore.showError(
      extractI18nErrorMessage(error, t, 'admin.affiliates.errors', t('common.error')),
    )
  } finally {
    overviewLoading.value = false
  }
}

// UserCell renders an account identifier, email, and username, optionally emitting its
// identifier when the email is clicked.
const UserCell = defineComponent({
  props: {
    id: { type: Number, required: true }, // displayed account identifier
    email: { type: String, default: '' }, // account email text
    username: { type: String, default: '' }, // account username text
    // clickable switches the email into a button and enables emitting the account
    // identifier to the overview dialog owner.
    clickable: { type: Boolean, default: false }, // overview interaction flag
  },
  // open carries the clicked account identifier to the table so it can request that
  // account's affiliate overview.
  emits: ['open'],
  // setup renders account identity and emits its identifier when the email button is clicked.
  setup(cellProps, { emit }) {
    return () =>
      h('div', { class: 'space-y-0.5' }, [
        h('div', { class: 'font-mono text-sm text-gray-900 dark:text-white' }, `#${cellProps.id}`),
        h(
          cellProps.clickable ? 'button' : 'div',
          {
            class: cellProps.clickable
              ? 'max-w-56 truncate text-left text-sm font-medium text-primary-600 hover:text-primary-700 hover:underline dark:text-primary-400 dark:hover:text-primary-300'
              : 'max-w-56 truncate text-sm text-gray-700 dark:text-gray-300',
            type: cellProps.clickable ? 'button' : undefined,
            onClick: cellProps.clickable ? () => emit('open', cellProps.id) : undefined,
          },
          cellProps.email || '-',
        ),
        h(
          'div',
          { class: 'max-w-56 truncate text-sm text-gray-500 dark:text-dark-400' },
          cellProps.username || '-',
        ),
      ])
  },
})

// AmountText formats account-unit values with optional emphasis, while
// NullableAmountText keeps missing historical snapshots distinct from numeric zero.
const AmountText = defineComponent({
  props: {
    value: { type: Number, default: 0 }, // account balance units
    strong: { type: Boolean, default: false }, // emphasized amount styling
  },
  // setup renders a balance amount with optional emphasized text styling.
  setup(amountProps) {
    return () =>
      h(
        'span',
        {
          class: amountProps.strong
            ? 'text-sm font-semibold text-emerald-600 dark:text-emerald-400'
            : 'text-sm text-gray-900 dark:text-white',
        },
        formatAffiliateAmount(amountProps.value),
      )
  },
})

// NullableAmountText displays a dash for absent snapshots and delegates populated
// values to the shared balance amount component.
const NullableAmountText = defineComponent({
  props: {
    // Nullish value means the historical snapshot is unavailable; zero remains a
    // populated amount and is formatted through AmountText.
    value: { type: Number as PropType<number | null | undefined>, default: null }, // historical balance snapshot
  },
  // setup renders missing historical snapshots as a dash and delegates numeric values to
  // AmountText. Null and undefined remain distinct from an observed zero, which receives
  // the configured account-unit formatting.
  setup(amountProps) {
    return () => {
      const value = amountProps.value
      // Historical transfers may lack balance snapshots; render a dash for missing
      // observations while retaining numeric zero as an observed amount.
      if (value === null || value === undefined) {
        return h('span', { class: 'text-sm text-gray-400 dark:text-dark-500' }, '-')
      }
      return h(AmountText, { value })
    }
  },
})

// OverviewStat pairs a translated label with its formatted value and optionally uses
// monospace styling for account identifiers.
const OverviewStat = defineComponent({
  props: {
    label: { type: String, required: true }, // overview field label
    value: { type: String, required: true }, // formatted overview value
    mono: { type: Boolean, default: false }, // monospace value styling
  },
  // setup renders a labeled overview statistic with optional monospace value styling.
  setup(statProps) {
    return () =>
      h(
        'div',
        {
          class:
            'rounded-surface border border-gray-100 bg-white p-3 dark:border-dark-700 dark:bg-dark-900',
        },
        [
          h('div', { class: 'text-sm text-gray-500 dark:text-dark-400' }, statProps.label),
          h(
            'div',
            {
              class: statProps.mono
                ? 'mt-1 font-mono text-base font-semibold text-gray-900 dark:text-white'
                : 'mt-1 text-base font-semibold text-gray-900 dark:text-white',
            },
            statProps.value,
          ),
        ],
      )
  },
})

// Start the selected record endpoint after mounting so the initialized props and
// validated sort state determine the initial table query.
onMounted(() => {
  void loadRecords()
})
</script>
