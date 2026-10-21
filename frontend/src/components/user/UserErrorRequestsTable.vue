<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <div class="card flex min-h-0 flex-1 flex-col overflow-hidden">
      <IpGeoBatchToolbar :ips="rows.map((r) => r.client_ip)" @failed="emit('ipGeoBatchFailed')" />

      <DataTable
        class="min-h-0 flex-1 overflow-auto"
        column-order-storage-key="user-error-requests-column-order"
        :columns="columns"
        :data="rows"
        :loading="loading"
        clickable-rows
        server-side-sort
        default-sort-key="created_at"
        default-sort-order="desc"
        @sort="onSort"
        @rowClick="(row) => openDetail(row.id)"
      >
        <template #cell-request_id="{ row }">
          <RequestIdLink :value="row.request_id" />
        </template>
        <template #cell-model="{ row }">
          <span v-if="row.model" class="text-sm font-medium text-gray-900 dark:text-white">{{
            row.model
          }}</span>
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-key_name="{ row }">
          <div class="text-sm">
            <span class="text-gray-900 dark:text-white">{{ row.key_name || '-' }}</span>
            <span
              v-if="row.key_deleted"
              class="ml-1 inline-flex items-center rounded-compact px-1 py-px text-xs font-medium leading-tight bg-rose-100 text-rose-600 ring-1 ring-inset ring-rose-200 dark:bg-rose-500/20 dark:text-rose-400 dark:ring-rose-500/30"
              >{{ t('usage.errors.keyDeleted') }}</span
            >
          </div>
        </template>

        <template #cell-endpoint="{ row }">
          <div class="max-w-[320px] text-xs">
            <div class="break-all text-gray-700 dark:text-gray-300">
              <span class="font-medium text-gray-500 dark:text-gray-400"
                >{{ t('usage.inbound') }}:</span
              >
              <span class="ml-1">{{ row.inbound_endpoint?.trim() || '-' }}</span>
            </div>
          </div>
        </template>

        <template #cell-status="{ row }">
          <span
            class="inline-flex items-center rounded-compact px-2 py-0.5 text-xs font-medium"
            :class="statusClass(row.status_code)"
          >
            {{ row.status_code || '-' }}
          </span>
        </template>

        <template #cell-category="{ row }">
          <span class="text-sm text-gray-900 dark:text-white">{{
            t('usage.errors.categories.' + row.category)
          }}</span>
        </template>

        <template #cell-message="{ row }">
          <span
            v-if="row.message"
            class="block max-w-[280px] truncate text-sm text-gray-600 dark:text-gray-400"
            :title="row.message"
            >{{ row.message }}</span
          >
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-group="{ row }">
          <span
            v-if="row.group_name"
            class="inline-flex items-center rounded-compact px-2 py-0.5 text-xs font-medium bg-indigo-100 text-indigo-800 dark:bg-indigo-900 dark:text-indigo-200"
            >{{ row.group_name }}</span
          >
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-type="{ row }">
          <span
            v-if="requestTypeBadge(row)"
            class="inline-flex items-center rounded-compact px-2 py-0.5 text-xs font-medium"
            :class="requestTypeBadge(row)!.className"
            >{{ requestTypeBadge(row)!.label }}</span
          >
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #cell-platform="{ row }">
          <span class="text-sm text-gray-900 dark:text-white">{{ row.platform || '-' }}</span>
        </template>

        <template #cell-client_ip="{ row }">
          <div @click.stop>
            <div v-if="row.client_ip">
              <span class="text-sm font-mono text-gray-600 dark:text-gray-400">{{
                row.client_ip
              }}</span>
              <IpGeoCell :ip="row.client_ip" />
            </div>
            <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
          </div>
        </template>

        <template #cell-created_at="{ row }">
          <span class="text-sm text-gray-600 dark:text-gray-400">{{
            formatDateTime(row.created_at)
          }}</span>
        </template>

        <template #cell-user_agent="{ row }">
          <span
            v-if="row.user_agent"
            class="block max-w-[320px] truncate text-sm text-gray-600 dark:text-gray-400"
            :title="row.user_agent"
            >{{ row.user_agent }}</span
          >
          <span v-else class="text-sm text-gray-400 dark:text-gray-500">-</span>
        </template>

        <template #empty><EmptyState :message="t('usage.errors.empty')" /></template>
      </DataTable>
      <!-- Pagination stays inside the table surface and outside the scrolling body. -->
      <Pagination
        v-if="total > 0"
        :page="page"
        :page-size="pageSize"
        :total="total"
        @update:page="$emit('update:page', $event)"
        @update:pageSize="$emit('update:pageSize', $event)"
      />
    </div>

    <UserErrorDetailModal v-model:show="showDetail" :error-id="selectedId" />
  </div>
</template>

<script setup lang="ts">
import RequestIdLink from '@/components/common/RequestIdLink.vue'
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import DataTable from '@/components/common/table/DataTable.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import Pagination from '@/components/common/Pagination.vue'
import UserErrorDetailModal from '@/components/user/UserErrorDetailModal.vue'
import IpGeoCell from '@/components/common/IpGeoCell.vue'
import IpGeoBatchToolbar from '@/components/common/IpGeoBatchToolbar.vue'
import { formatDateTime } from '@/utils/format'
import {
  mapErrorSortKey,
  numericRequestTypeKind,
  requestTypeBadgeClass,
  requestTypeLabelKey,
  statusCodeBadgeClass,
} from '@/utils/errorBadges'
import type { UserErrorRequest } from '@/api/usage.types'
import type { Column } from '@/components/common/table/types'

// UserErrorRequestsTable.vue renders user error rows with sortable columns, pagination, and
// a detail dialog. The parent owns queries and visible column selection, while this table
// translates sort keys, publishes page changes, and keeps the selected error identifier for
// inspection.

// props supplies parent-owned error records, paging totals, loading progress, and optional
// column visibility preferences.
const props = defineProps<{
  rows: UserErrorRequest[] // current error page records
  total: number // matching error record count
  loading: boolean // parent query progress flag
  page: number // current one-based page
  pageSize: number // records requested per page
  // visibleColumnKeys selects keys from allColumns; omission shows every column and an
  // empty array hides all selectable columns.
  visibleColumnKeys?: string[]
}>()

// emit forwards page, page-size, sorting, and IP lookup failure events to the query-owning
// page.
const emit = defineEmits<{
  // update:page selects the one-based error page requested from the parent.
  (e: 'update:page', v: number): void
  // update:pageSize requests a new error page size from the parent.
  (e: 'update:pageSize', v: number): void
  // ipGeoBatchFailed asks the parent to report a location batch failure.
  (e: 'ipGeoBatchFailed'): void
  // sort carries the mapped API field and requested server ordering.
  (e: 'sort', sortBy: string, sortOrder: 'asc' | 'desc'): void
}>()

// onSort maps a table column key to the error API sort key before notifying the parent.
function onSort(key: string, order: 'asc' | 'desc') {
  emit('sort', mapErrorSortKey(key), order)
}

// The translation binding supplies column headings and request-type badges for the
// currently rendered error table.
const { t } = useI18n()

// allColumns orders error fields alongside usage details, keeping request identity and user
// agent after the timestamp.
const allColumns = computed<Column[]>(() => [
  { key: 'key_name', label: t('usage.errors.keyName') },
  { key: 'model', label: t('usage.errors.model'), sortable: true },
  { key: 'endpoint', label: t('usage.errors.endpoint') },
  { key: 'client_ip', label: 'IP' },
  { key: 'group', label: t('admin.usage.group') },
  { key: 'type', label: t('usage.type') },
  { key: 'platform', label: t('usage.errors.platform') },
  { key: 'category', label: t('usage.errors.category') },
  { key: 'status', label: t('usage.errors.status'), sortable: true },
  { key: 'message', label: t('usage.errors.message') },
  { key: 'created_at', label: t('usage.errors.time'), sortable: true },
  { key: 'request_id', label: t('requests.id') },
  { key: 'user_agent', label: t('usage.userAgent') },
])

// columns retains canonical table order while selecting the parent-provided keys; omitted
// preferences show every available column.
const columns = computed<Column[]>(() =>
  props.visibleColumnKeys
    ? allColumns.value.filter((c) => props.visibleColumnKeys!.includes(c.key))
    : allColumns.value,
)

// requestTypeBadge resolves the numeric request mode and legacy stream flag into badge text
// and styling. Unrecognized modes return null so the template omits the request-type badge.
function requestTypeBadge(row: UserErrorRequest): {
  label: string // translated request type text
  className: string // request type badge classes
} | null {
  const kind = numericRequestTypeKind(row.request_type, row.stream)
  if (!kind) return null
  return { label: t(requestTypeLabelKey(kind)), className: requestTypeBadgeClass(kind) }
}

// Detail selection state keeps the chosen error identifier beside the visibility flag
// consumed by the nested modal.
const showDetail = ref(false)
const selectedId = ref<number | null>(null)

// openDetail selects an error identifier and opens the nested detail dialog.
function openDetail(id: number) {
  selectedId.value = id
  showDetail.value = true
}

// statusClass reuses the shared HTTP status palette so user errors match other error
// tables.
const statusClass = statusCodeBadgeClass
</script>
