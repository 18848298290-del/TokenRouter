<script setup lang="ts">
import RequestIdLink from '@/components/common/RequestIdLink.vue'
import RequestIdSearch from '@/components/common/RequestIdSearch.vue'
import TableSkeletonBody from '@/components/common/TableSkeletonBody.vue'
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { computed, ref, watch } from 'vue'
import { useMediaQuery } from '@vueuse/core'
import { TABLE_DESKTOP_MEDIA_QUERY } from '@/constants/layout'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import { useAppStore } from '@/stores'
import { opsAPI, type OpsRequestDetailsParams, type OpsRequestDetail } from '@/api/admin/ops'
import { parseTimeRangeMinutes, formatDateTime } from '../utils/opsFormatters'

// OpsRequestDetailsModal.vue lists request outcomes for dashboard presets and
// opens individual error details. Request-ID searches omit time parameters,
// while ordinary queries use explicit timestamps derived from the same
// custom-range validity check as the displayed label. Missing custom bounds use
// the one-hour relative range.
//
// The parent can reopen this dialog with resumeState to retain the current page
// and search. Fresh openings reset pagination and request identity. Filter and
// preset changes reset to the first page before fetching, while page changes
// reuse the current constraints. Retrieval failures clear the displayed records
// and report the API error through the application store.

// OpsRequestDetailsPreset describes a dashboard drilldown and optional
// request-selection constraints.
export interface OpsRequestDetailsPreset {
  title: string // drilldown dialog title text
  // kind selects request outcomes; fetchData uses all when the preset omits this
  // optional filter.
  kind?: OpsRequestDetailsParams['kind']
  // sla_only adds the API SLA-only flag when true; false or omission leaves that
  // request predicate absent.
  sla_only?: boolean
  // sort selects the backend request order; omission uses created_at_desc when
  // the request dialog builds its query.
  sort?: OpsRequestDetailsParams['sort']
  min_duration_ms?: number // milliseconds
  max_duration_ms?: number // milliseconds
}

// Props combines dialog visibility, dashboard filters, and the selected
// drilldown preset.
interface Props {
  modelValue: boolean // owner-controlled dialog visibility
  timeRange: string // selected history range token
  // customStartTime supplies the explicit interval start when custom mode has
  // both bounds; otherwise consumers use the one-hour range.
  customStartTime?: string | null
  // customEndTime supplies the explicit interval end when custom mode has both
  // bounds; otherwise consumers use the one-hour range.
  customEndTime?: string | null
  preset: OpsRequestDetailsPreset // parent-selected request drilldown settings
  // platform filters by the recorded execution platform; an empty value leaves
  // all platforms eligible for display.
  platform?: string
  // groupId selects a positive persisted group identifier; null or omission
  // leaves the query unfiltered by group.
  groupId?: number | null
  // resumeState skips the fresh-opening reset when returning from a detail
  // dialog, retaining the current filters and pagination.
  resumeState?: boolean
}

// Bindings publish visibility and error-navigation events while the component
// owns its current request list and pagination.
const props = defineProps<Props>()
const emit = defineEmits<{
  // update:modelValue asks the parent to change request-dialog visibility state.
  (e: 'update:modelValue', value: boolean): void
  // openErrorDetail forwards a persisted error identifier for single-record
  // inspection.
  (e: 'openErrorDetail', errorId: number): void
}>()

// Translation and application notifications provide request-list labels and
// retrieval failure feedback for the open dialog.
const { t } = useI18n()
const appStore = useAppStore()

// isDesktopViewport uses the shared table breakpoint to choose a desktop table
// or narrow request cards.
const isDesktopViewport = useMediaQuery(TABLE_DESKTOP_MEDIA_QUERY)

// requestId stores the applied identity search; a nonempty value removes the
// time interval from request queries.
const requestId = ref('')
// Request list state tracks retrieval progress, visible records, and one-based
// pagination for the current drilldown.
const loading = ref(false)
const items = ref<OpsRequestDetail[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = ref(10)

// close asks the parent to hide the request details dialog.
const close = () => emit('update:modelValue', false)

// hasCustomRange requires both custom timestamps so the displayed window and
// generated API parameters use the same interpretation.
const hasCustomRange = computed(
  () => props.timeRange === 'custom' && Boolean(props.customStartTime && props.customEndTime),
)

// rangeLabel displays complete custom bounds or the parsed relative duration,
// rounding durations of at least an hour for the label.
const rangeLabel = computed(() => {
  if (hasCustomRange.value) {
    return t('admin.ops.requestDetails.rangeCustom', {
      start: formatDateTime(props.customStartTime!),
      end: formatDateTime(props.customEndTime!),
    })
  }
  const minutes = parseTimeRangeMinutes(props.timeRange)
  // Round longer ranges to hours for the dialog caption.
  if (minutes >= 60)
    return t('admin.ops.requestDetails.rangeHours', { n: Math.round(minutes / 60) })
  return t('admin.ops.requestDetails.rangeMinutes', { n: minutes })
})

// buildTimeParams returns custom bounds when both are available, otherwise a
// relative interval ending now. Relative minutes become milliseconds before
// generating ISO timestamps, with parseTimeRangeMinutes supplying the one-hour
// default.
function buildTimeParams(): Pick<OpsRequestDetailsParams, 'start_time' | 'end_time'> {
  if (hasCustomRange.value) {
    return { start_time: props.customStartTime!, end_time: props.customEndTime! }
  }
  const minutes = parseTimeRangeMinutes(props.timeRange)
  const endTime = new Date()
  // Convert relative minutes to milliseconds for the request start timestamp.
  const startTime = new Date(endTime.getTime() - minutes * 60 * 1000)
  return {
    start_time: startTime.toISOString(),
    end_time: endTime.toISOString(),
  }
}

// fetchData loads the visible request page using the dashboard preset and active
// filters. Hidden dialogs return immediately, and errors clear rows and totals
// while reporting a notification.
//
// A nonempty request ID suppresses time bounds; otherwise buildTimeParams
// provides the displayed interval. Optional duration and SLA filters remain
// independent of request identity, and kind and sort use preset defaults when
// omitted. Reads publish on completion without cancellation or a generation
// guard, so overlapping calls can finish in a different order.
const fetchData = async () => {
  if (!props.modelValue) return
  loading.value = true
  try {
    const params: OpsRequestDetailsParams = {
      ...(requestId.value ? {} : buildTimeParams()),
      request_id: requestId.value || undefined,
      page: page.value,
      page_size: pageSize.value,
      kind: props.preset.kind ?? 'all',
      sort: props.preset.sort ?? 'created_at_desc',
    }
    if (props.preset.sla_only) params.sla_only = '1'

    const platform = (props.platform || '').trim()
    if (platform) params.platform = platform
    if (typeof props.groupId === 'number' && props.groupId > 0) params.group_id = props.groupId

    if (typeof props.preset.min_duration_ms === 'number')
      params.min_duration_ms = props.preset.min_duration_ms
    if (typeof props.preset.max_duration_ms === 'number')
      params.max_duration_ms = props.preset.max_duration_ms

    const res = await opsAPI.listRequestDetails(params)
    items.value = res.items || []
    total.value = res.total || 0
  } catch (e: any) {
    // Clear the failed request page and total after reporting the error so prior
    // interval records do not remain displayed as current matches.
    console.error('[OpsRequestDetailsModal] Failed to fetch request details', e)
    appStore.showError(e?.message || t('admin.ops.requestDetails.failedToLoad'))
    items.value = []
    total.value = 0
  } finally {
    loading.value = false
  }
}

// A fresh opening resets identity search and pagination, while resumeState
// preserves the list context captured before single-error navigation.
watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      if (props.resumeState) return
      requestId.value = ''
      page.value = 1
      pageSize.value = 10
      fetchData()
    }
  },
)

watch(
  () => [
    props.timeRange,
    props.customStartTime,
    props.customEndTime,
    props.platform,
    props.groupId,
    props.preset.kind,
    props.preset.sla_only,
    props.preset.sort,
    props.preset.min_duration_ms,
    props.preset.max_duration_ms,
  ],
  () => {
    if (!props.modelValue) return
    page.value = 1
    fetchData()
  },
)

// searchRequests resets the selected page before requesting records for the
// applied search. The delegated fetchData checks dialog visibility, so a hidden
// dialog keeps the reset page without starting a request.
function searchRequests() {
  page.value = 1
  void fetchData()
}

// handlePageChange stores the selected page before requesting its records with
// the current filters. The delegated fetchData checks dialog visibility and
// skips the network request while the dialog is hidden.
function handlePageChange(next: number) {
  page.value = next
  fetchData()
}

// handlePageSizeChange updates the page length and returns to page one before
// reloading. This keeps the next request aligned with the new offset implied by
// its page size.
function handlePageSizeChange(next: number) {
  pageSize.value = next
  page.value = 1
  fetchData()
}

// openErrorDetail forwards a recorded error identifier to the dashboard. Missing
// or zero identifiers produce no event, allowing successful request rows to
// share the same action handler safely.
function openErrorDetail(errorId: number | null | undefined) {
  if (!errorId) return
  emit('openErrorDetail', errorId)
}

// kindBadgeClass marks error rows red and other request outcomes green.
const kindBadgeClass = (kind: string) => {
  if (kind === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
}
</script>

<template>
  <BaseDialog
    :show="modelValue"
    :title="props.preset.title || t('admin.ops.requestDetails.title')"
    width="full"
    @close="close"
  >
    <template #default>
      <div class="flex h-full min-h-0 flex-col">
        <div class="mb-4 flex flex-shrink-0 flex-wrap items-center justify-between gap-2">
          <div class="min-w-0 text-xs text-gray-500 dark:text-gray-400">
            {{
              requestId
                ? t('requests.allTimeRange')
                : t('admin.ops.requestDetails.rangeLabel', { range: rangeLabel })
            }}
          </div>
          <div class="flex min-w-0 items-center gap-2">
            <RequestIdSearch v-model="requestId" class="w-full sm:w-64" @search="searchRequests" />
            <button type="button" class="btn btn-secondary btn-sm h-9 shrink-0" @click="fetchData">
              {{ t('common.refresh') }}
            </button>
          </div>
        </div>

        <!-- Table -->
        <div class="flex min-h-0 flex-1 flex-col">
          <div
            v-if="!loading && items.length === 0"
            class="rounded-surface border border-dashed border-gray-200 p-10 text-center dark:border-dark-700"
          >
            <div class="text-sm font-medium text-gray-600 dark:text-gray-300">
              {{ t('admin.ops.requestDetails.empty') }}
            </div>
            <div class="mt-1 text-xs text-gray-400">
              {{ t('admin.ops.requestDetails.emptyHint') }}
            </div>
          </div>

          <div
            v-else
            class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-surface border border-gray-200 dark:border-dark-700"
          >
            <div class="min-h-0 flex-1 overflow-auto">
              <div v-if="!isDesktopViewport" class="divide-y divide-gray-100 dark:divide-dark-800">
                <ContentSkeleton v-if="loading" :rows="5" class="p-4" />
                <div v-else v-for="(row, idx) in items" :key="idx" class="space-y-2 p-4">
                  <div class="flex flex-wrap items-center gap-2">
                    <span
                      class="rounded-full px-2 py-1 text-xs font-bold"
                      :class="kindBadgeClass(row.kind)"
                    >
                      {{
                        row.kind === 'error'
                          ? t('admin.ops.requestDetails.kind.error')
                          : t('admin.ops.requestDetails.kind.success')
                      }}
                    </span>
                    <span class="text-xs font-medium text-gray-700 dark:text-gray-200">{{
                      (row.platform || 'unknown').toUpperCase()
                    }}</span>
                    <span class="ml-auto text-xs text-gray-500 dark:text-gray-400">{{
                      formatDateTime(row.created_at)
                    }}</span>
                  </div>
                  <div class="break-all text-xs text-gray-600 dark:text-gray-300">
                    {{ row.model || '-' }}
                  </div>
                  <div
                    class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-gray-600 dark:text-gray-300"
                  >
                    <span>{{
                      typeof row.duration_ms === 'number' ? `${row.duration_ms} ms` : '-'
                    }}</span>
                    <span>{{ row.status_code ?? '-' }}</span>
                  </div>
                  <RequestIdLink v-if="row.request_id" :value="row.request_id" />
                  <button
                    v-if="row.kind === 'error' && row.error_id"
                    class="w-full rounded-control bg-red-50 px-3 py-1.5 text-xs font-bold text-red-600 hover:bg-red-100 dark:bg-red-900/20 dark:text-red-300 dark:hover:bg-red-900/30"
                    @click="openErrorDetail(row.error_id)"
                  >
                    {{ t('admin.ops.requestDetails.viewError') }}
                  </button>
                </div>
              </div>
              <table
                v-else
                :aria-busy="loading"
                class="min-w-full divide-y divide-gray-200 dark:divide-dark-700"
              >
                <thead class="sticky top-0 z-10 bg-gray-50 dark:bg-dark-950">
                  <tr>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.time') }}
                    </th>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.kind') }}
                    </th>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.platform') }}
                    </th>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.model') }}
                    </th>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.duration') }}
                    </th>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.status') }}
                    </th>
                    <th
                      class="px-4 py-3 text-left text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.requestId') }}
                    </th>
                    <th
                      class="px-4 py-3 text-right text-xs font-bold tracking-wider text-gray-500 dark:text-gray-400"
                    >
                      {{ t('admin.ops.requestDetails.table.actions') }}
                    </th>
                  </tr>
                </thead>
                <TableSkeletonBody v-if="loading" :columns="8" />
                <tbody
                  v-else
                  class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-900"
                >
                  <tr
                    v-for="(row, idx) in items"
                    :key="idx"
                    class="hover:bg-gray-50 dark:hover:bg-dark-700/50"
                  >
                    <td
                      class="whitespace-nowrap px-4 py-3 text-xs text-gray-600 dark:text-gray-300"
                    >
                      {{ formatDateTime(row.created_at) }}
                    </td>
                    <td class="whitespace-nowrap px-4 py-3">
                      <span
                        class="rounded-full px-2 py-1 text-xs font-bold"
                        :class="kindBadgeClass(row.kind)"
                      >
                        {{
                          row.kind === 'error'
                            ? t('admin.ops.requestDetails.kind.error')
                            : t('admin.ops.requestDetails.kind.success')
                        }}
                      </span>
                    </td>
                    <td
                      class="whitespace-nowrap px-4 py-3 text-xs font-medium text-gray-700 dark:text-gray-200"
                    >
                      {{ (row.platform || 'unknown').toUpperCase() }}
                    </td>
                    <td
                      class="max-w-[240px] truncate px-4 py-3 text-xs text-gray-600 dark:text-gray-300"
                      :title="row.model || ''"
                    >
                      {{ row.model || '-' }}
                    </td>
                    <td
                      class="whitespace-nowrap px-4 py-3 text-xs text-gray-600 dark:text-gray-300"
                    >
                      {{ typeof row.duration_ms === 'number' ? `${row.duration_ms} ms` : '-' }}
                    </td>
                    <td
                      class="whitespace-nowrap px-4 py-3 text-xs text-gray-600 dark:text-gray-300"
                    >
                      {{ row.status_code ?? '-' }}
                    </td>
                    <td class="px-4 py-3">
                      <RequestIdLink v-if="row.request_id" :value="row.request_id" />
                      <span v-else class="text-xs text-gray-400">-</span>
                    </td>
                    <td class="whitespace-nowrap px-4 py-3 text-right">
                      <button
                        v-if="row.kind === 'error' && row.error_id"
                        class="rounded-control bg-red-50 px-3 py-1.5 text-xs font-bold text-red-600 hover:bg-red-100 dark:bg-red-900/20 dark:text-red-300 dark:hover:bg-red-900/30"
                        @click="openErrorDetail(row.error_id)"
                      >
                        {{ t('admin.ops.requestDetails.viewError') }}
                      </button>
                      <span v-else class="text-xs text-gray-400">-</span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>

            <Pagination
              v-if="!loading"
              :total="total"
              :page="page"
              :page-size="pageSize"
              @update:page="handlePageChange"
              @update:pageSize="handlePageSizeChange"
            />
          </div>
        </div>
      </div>
    </template>
  </BaseDialog>
</template>
