<template>
  <BaseDialog
    :show="show"
    :title="t('admin.announcements.readStatus')"
    width="extra-wide"
    @close="handleClose"
  >
    <div class="space-y-4">
      <div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <div class="flex-1">
          <input
            v-model="search"
            type="text"
            class="input"
            :placeholder="t('admin.announcements.searchUsers')"
            @input="handleSearch"
          />
        </div>
        <button
          @click="load"
          :disabled="loading"
          class="btn btn-secondary"
          :title="t('common.refresh')"
        >
          <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
        </button>
      </div>

      <!-- The table and pagination share one rounded border. -->
      <div class="card overflow-hidden">
        <DataTable
          column-order-storage-key="admin-announcement-read-status-column-order"
          :columns="columns"
          :data="items"
          :loading="loading"
          :server-side-sort="true"
          default-sort-key="email"
          default-sort-order="asc"
          @sort="handleSort"
        >
          <template #cell-email="{ value }">
            <span class="font-medium text-gray-900 dark:text-white">{{ value }}</span>
          </template>

          <template #cell-balance="{ value }">
            <span class="font-medium text-gray-900 dark:text-white">{{
              formatBalanceAmount(Number(value ?? 0), { fractionDigits: 2 })
            }}</span>
          </template>

          <template #cell-eligible="{ value }">
            <span :class="['badge', value ? 'badge-success' : 'badge-gray']">
              {{ value ? t('admin.announcements.eligible') : t('common.no') }}
            </span>
          </template>

          <template #cell-read_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">
              {{ value ? formatDateTime(value) : t('admin.announcements.unread') }}
            </span>
          </template>
        </DataTable>

        <Pagination
          v-if="pagination.total > 0"
          :page="pagination.page"
          :total="pagination.total"
          :page-size="pagination.page_size"
          @update:page="handlePageChange"
          @update:pageSize="handlePageSizeChange"
        />
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { formatDateTime } from '@/utils/format'
import type { AnnouncementUserReadStatus } from '@/api/announcements.types'
import type { Column } from '@/components/common/table/types'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'

import BaseDialog from '@/components/common/BaseDialog.vue'
import DataTable from '@/components/common/table/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'

// AnnouncementReadStatusDialog.vue lists users, eligibility, and read timestamps for
// one announcement. Search, sorting, and pagination share a cancellable request, so
// every replacement request claims ownership before publishing rows or clearing the
// loading indicator. Closing resets the displayed query and cancels the delayed search
// as well as the current request.
//
// The controller identity check supplements the abort signal because a request can
// settle after another load starts. The show and announcement watchers initiate loads
// separately, while unmount releases pending work without resetting the display. The
// dialog emits close after cleanup so its parent can hide it with no queued search left
// to restart loading.

// The translation, notification, and balance services supply presentation values for
// the read-status table and its request failures.
const { t } = useI18n()
const appStore = useAppStore()
const { formatBalanceAmount } = useBalanceDisplay()

// props identifies the announcement and visibility supplied by the parent that owns
// opening and closing this dialog.
const props = defineProps<{
  show: boolean // Parent-controlled dialog visibility.
  // announcementId selects the read-status subject; null prevents loading until the
  // parent supplies an announcement.
  announcementId: number | null
}>()

// emit asks the parent to close after this component releases its pending request and
// search timer.
const emit = defineEmits<{
  (e: 'close'): void // Requests parent-owned dialog closure.
}>()

// loading and search hold request feedback and the search text consumed by the next
// read-status query.
const loading = ref(false)
const search = ref('')

// pagination stores the server page metadata alongside the persisted initial page size
// used by the table controls.
const pagination = reactive({
  page: 1, // One-based result page.
  page_size: getPersistedPageSize(), // Rows per page.
  total: 0, // Matching record count.
  pages: 0, // Available page count.
})

// sortState supplies the server sort field and direction, resetting the page whenever
// the administrator changes ordering.
const sortState = reactive({
  sort_by: 'email', // Server ordering field.
  sort_order: 'asc' as 'asc' | 'desc', // Server ordering direction.
})

// items holds the last accepted read-status response for rendering email, eligibility,
// balance, and read timestamps in the table.
const items = ref<AnnouncementUserReadStatus[]>([])

// columns describes the localized read-status cells and marks the three fields that the
// server can sort for this table.
const columns = computed<Column[]>(() => [
  {
    key: 'email', // Table column identifier.
    label: t('common.email'), // Displayed option label.
    sortable: true, // Server sorting enabled.
  },
  {
    key: 'username', // Table column identifier.
    label: t('admin.users.columns.username'), // Displayed option label.
    sortable: true, // Server sorting enabled.
  },
  {
    key: 'balance', // Table column identifier.
    label: t('common.balance'), // Displayed option label.
    sortable: true, // Server sorting enabled.
  },
  {
    key: 'eligible', // Table column identifier.
    label: t('admin.announcements.eligible'), // Displayed option label.
  },
  {
    key: 'read_at', // Table column identifier.
    label: t('admin.announcements.readAt'), // Displayed option label.
  },
])

// currentController owns the active load; only that controller may publish rows or
// clear the shared loading state.
let currentController: AbortController | null = null
// searchDebounceTimer delays typed searches and is cleared when another search, dialog
// close, or unmount supersedes it.
let searchDebounceTimer: number | null = null

// resetDialogState clears rows, query text, and page metadata for the next opening. The
// persisted page size stays available, while the sort returns to ascending email order.
function resetDialogState() {
  loading.value = false
  search.value = ''
  items.value = []
  pagination.page = 1
  pagination.total = 0
  pagination.pages = 0
  sortState.sort_by = 'email'
  sortState.sort_order = 'asc'
}

// cancelPendingLoad clears the queued search and aborts the current read-status
// request. Pass true when closing to reset visible state; unmount uses the default to
// release resources alone.
function cancelPendingLoad(resetState = false) {
  if (searchDebounceTimer) {
    window.clearTimeout(searchDebounceTimer)
    searchDebounceTimer = null
  }
  // Aborting the previous owner prevents its pending response from continuing as the
  // active dialog request after replacement or closure.
  currentController?.abort()
  currentController = null
  if (resetState) {
    resetDialogState()
  }
}

// load requests the selected announcement page while the dialog is visible and has an
// announcement ID. It aborts the previous request and assigns a new controller before
// awaiting the server response.
//
// Both signal state and controller identity gate row publication, error reporting, and
// final cleanup. A superseded request therefore cannot replace newer rows or clear
// their loading indicator, even if its rejection arrives after the replacement request
// starts.
async function load() {
  if (!props.show || !props.announcementId) return

  // Abort the previous request before assigning its replacement so its later completion
  // fails the controller-identity check used for publication and cleanup.
  currentController?.abort()
  const requestController = new AbortController()
  currentController = requestController
  const { signal } = requestController

  try {
    loading.value = true
    const res = await adminAPI.announcements.getReadStatus(
      props.announcementId,
      pagination.page,
      pagination.page_size,
      {
        search: search.value, // User search text.
        sort_by: sortState.sort_by, // Server ordering field.
        sort_order: sortState.sort_order, // Server ordering direction.
      },
      {
        signal, // Active request cancellation signal.
      },
    )

    // A completed request may already have been superseded; check ownership again
    // before publishing its rows and pagination into the dialog.
    if (signal.aborted || currentController !== requestController) return

    items.value = res.items
    pagination.total = res.total
    pagination.pages = res.pages
    pagination.page = res.page
    pagination.page_size = res.page_size
  } catch (error: any) {
    // Cancelled and superseded requests are expected during searches and closure, so
    // suppress their failures instead of showing an error for discarded work.
    if (
      signal.aborted ||
      currentController !== requestController ||
      error?.name === 'AbortError' ||
      error?.code === 'ERR_CANCELED'
    ) {
      return
    }
    console.error('Failed to load read status:', error)
    appStore.showError(
      error.response?.data?.detail || t('admin.announcements.failedToLoadReadStatus'),
    )
  } finally {
    // Only the current request clears pending feedback, because an earlier request can
    // finish after a newer load has started.
    if (currentController === requestController) {
      loading.value = false
      currentController = null
    }
  }
}

// handlePageChange selects the requested page and starts another read-status load.
function handlePageChange(page: number) {
  pagination.page = page
  load()
}

// handlePageSizeChange applies the selected row count and reloads from the first page.
// Resetting the page before the request prevents a previous page offset from being
// reused with the changed size.
function handlePageSizeChange(pageSize: number) {
  pagination.page_size = pageSize
  pagination.page = 1
  load()
}

// handleSort stores the selected server field and direction before reloading the first
// page. Starting from page one presents the beginning of the newly ordered result set
// to the administrator.
function handleSort(key: string, order: 'asc' | 'desc') {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  load()
}

// handleSearch replaces the pending search with a three-hundred-millisecond delay. When
// the timer fires it resets the page and loads the current query; close and unmount
// clear the timer.
function handleSearch() {
  // Replace the pending search delay so typing multiple characters schedules one load
  // using the query present when it fires.
  if (searchDebounceTimer) window.clearTimeout(searchDebounceTimer)
  searchDebounceTimer = window.setTimeout(() => {
    pagination.page = 1
    load()
  }, 300)
}

// handleClose cancels request and timer work before notifying the parent. It also
// resets rows, search text, and ordering so the next opening starts with a clean query.
function handleClose() {
  cancelPendingLoad(true)
  emit('close')
}

// Visibility and announcement changes drive separate reloads; closing cancels pending
// work so hidden content cannot schedule another search request.
watch(
  () => props.show,
  (v) => {
    if (!v) {
      cancelPendingLoad(true)
      return
    }
    pagination.page = 1
    load()
  },
)

// Changing the announcement while visible starts another page-one request, letting load
// replace the previous controller before results from different announcements can
// compete.
watch(
  () => props.announcementId,
  () => {
    if (!props.show) return
    pagination.page = 1
    load()
  },
)

// Unmount releases the controller and search timer because this component owns both
// resources independently of the parent visibility flag.
onUnmounted(() => {
  cancelPendingLoad()
})
</script>
