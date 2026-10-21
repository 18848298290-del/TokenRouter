<template>
  <BaseDialog
    :show="show"
    :title="t('admin.usage.cleanup.title')"
    width="wide"
    @close="handleClose"
  >
    <div class="space-y-4">
      <UsageFilters
        v-model="localFilters"
        v-model:startDate="localStartDate"
        v-model:endDate="localEndDate"
        :exporting="false"
        :show-actions="false"
        @change="noop"
      />

      <div
        class="rounded-surface border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-200"
      >
        {{ t('admin.usage.cleanup.warning') }}
      </div>

      <div class="rounded-surface border border-gray-200 p-4 dark:border-dark-700">
        <div class="flex items-center justify-between">
          <h4 class="text-sm font-semibold text-gray-700 dark:text-gray-200">
            {{ t('admin.usage.cleanup.recentTasks') }}
          </h4>
          <button type="button" class="btn btn-ghost btn-sm" @click="loadTasks">
            {{ t('common.refresh') }}
          </button>
        </div>

        <div class="mt-3 space-y-2">
          <div v-if="tasksLoading" class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.usage.cleanup.loadingTasks') }}
          </div>
          <div v-else-if="tasks.length === 0" class="text-sm text-gray-500 dark:text-gray-400">
            {{ t('admin.usage.cleanup.noTasks') }}
          </div>
          <div v-else class="space-y-2">
            <div
              v-for="task in tasks"
              :key="task.id"
              class="flex flex-col gap-2 rounded-control border border-gray-100 px-3 py-2 text-sm text-gray-600 dark:border-dark-700 dark:text-gray-300"
            >
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="flex items-center gap-2">
                  <span
                    :class="statusClass(task.status)"
                    class="rounded-full px-2 py-0.5 text-xs font-semibold"
                  >
                    {{ statusLabel(task.status) }}
                  </span>
                  <span class="text-xs text-gray-400">#{{ task.id }}</span>
                  <button
                    v-if="canCancel(task)"
                    type="button"
                    class="btn btn-ghost btn-xs text-rose-600 hover:text-rose-700 dark:text-rose-300"
                    @click="openCancelConfirm(task)"
                  >
                    {{ t('admin.usage.cleanup.cancel') }}
                  </button>
                </div>
                <div class="text-xs text-gray-400">
                  {{ formatDateTime(task.created_at) }}
                </div>
              </div>
              <div
                class="flex flex-wrap items-center gap-4 text-xs text-gray-500 dark:text-gray-400"
              >
                <span>{{ t('admin.usage.cleanup.range') }}: {{ formatRange(task) }}</span>
                <span
                  >{{ t('admin.usage.cleanup.deletedRows') }}:
                  {{ task.deleted_rows.toLocaleString() }}</span
                >
              </div>
              <div v-if="task.error_message" class="text-xs text-rose-500">
                {{ task.error_message }}
              </div>
            </div>
          </div>
        </div>

        <Pagination
          v-if="tasksTotal > tasksPageSize"
          class="mt-4"
          :total="tasksTotal"
          :page="tasksPage"
          :page-size="tasksPageSize"
          :page-size-options="[5]"
          :show-page-size-selector="false"
          :show-jump="true"
          @update:page="handleTaskPageChange"
          @update:pageSize="handleTaskPageSizeChange"
        />
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button type="button" class="btn btn-danger" :disabled="submitting" @click="openConfirm">
          {{ submitting ? t('admin.usage.cleanup.submitting') : t('admin.usage.cleanup.submit') }}
        </button>
      </div>
    </template>
  </BaseDialog>

  <ConfirmDialog
    :show="confirmVisible"
    :title="t('admin.usage.cleanup.confirmTitle')"
    :message="t('admin.usage.cleanup.confirmMessage')"
    :confirm-text="t('admin.usage.cleanup.confirmSubmit')"
    danger
    @confirm="submitCleanup"
    @cancel="confirmVisible = false"
  />

  <ConfirmDialog
    :show="cancelConfirmVisible"
    :title="t('admin.usage.cleanup.cancelConfirmTitle')"
    :message="t('admin.usage.cleanup.cancelConfirmMessage')"
    :confirm-text="t('admin.usage.cleanup.cancelConfirm')"
    danger
    @confirm="cancelTask"
    @cancel="cancelConfirmVisible = false"
  />
</template>

<script setup lang="ts">
import { ref, watch, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import UsageFilters from '@/components/admin/usage/UsageFilters.vue'
import { adminUsageAPI } from '@/api/admin/usage'
import type {
  AdminUsageQueryParams,
  UsageCleanupTask,
  CreateUsageCleanupTaskRequest,
} from '@/api/admin/usage'
import { requestTypeToLegacyStream } from '@/utils/usageRequestType'

// UsageCleanupDialog.vue creates filtered usage cleanup tasks and displays their progress
// in a paginated dialog. Opening the dialog copies the current filters into a local draft;
// polling refreshes task status until the dialog closes or unmounts.

// Props supplies the visibility and initial filter values for cleanup drafts.
interface Props {
  show: boolean // dialog visibility state
  // filters supplies the parent selection copied into a mutable local draft whenever the
  // dialog opens.
  filters: AdminUsageQueryParams
  startDate: string // initial beginning calendar date
  endDate: string // initial ending calendar date
}

// props provides parent-owned visibility and filters that resetFilters copies when the
// dialog opens.
const props = defineProps<Props>()
// emit asks the parent to close the cleanup dialog after local transient state has been
// reset.
const emit = defineEmits(['close'])

// Translation and notification services supply cleanup status labels and feedback for task
// API operations.
const { t } = useI18n()
const appStore = useAppStore()

// The local filter draft owns cleanup selection independently of the usage page until the
// next dialog opening.
const localFilters = ref<AdminUsageQueryParams>({})
// localStartDate stores the selected beginning calendar date used by the cleanup payload
// builder.
const localStartDate = ref('')
// localEndDate stores the selected ending calendar date used by the cleanup payload
// builder.
const localEndDate = ref('')

// Task list state tracks the current cleanup page, its server-reported total, and retrieval
// progress.
const tasks = ref<UsageCleanupTask[]>([])
const tasksLoading = ref(false)
const tasksPage = ref(1)
const tasksPageSize = ref(5)
const tasksTotal = ref(0)
// Submission and cancellation controls track separate API operations and their confirmation
// dialogs in this component instance.
const submitting = ref(false)
const confirmVisible = ref(false)
const cancelConfirmVisible = ref(false)
const canceling = ref(false)
// cancelTarget retains the selected task until cancellation completes or closing the dialog
// clears that selection.
const cancelTarget = ref<UsageCleanupTask | null>(null)
// pollTimer owns the periodic task refresh handle and is cleared on close or unmount.
let pollTimer: number | null = null

// noop accepts filter notifications while cleanup waits for explicit confirmation.
const noop = () => {}

// resetFilters copies parent filters and dates into the cleanup draft. It resets task
// pagination to the first page and clears the remembered total before the next retrieval.
const resetFilters = () => {
  localFilters.value = { ...props.filters }
  localStartDate.value = props.startDate
  localEndDate.value = props.endDate
  localFilters.value.start_date = localStartDate.value
  localFilters.value.end_date = localEndDate.value
  tasksPage.value = 1
  tasksTotal.value = 0
}

// startPolling replaces the current refresh timer with a ten-second interval. It delegates
// each tick to loadTasks, whose visibility guard skips retrieval while the dialog is
// hidden.
const startPolling = () => {
  // Replace the previous interval before starting another, so repeated openings do not
  // accumulate independent cleanup task refresh timers.
  stopPolling()
  pollTimer = window.setInterval(() => {
    loadTasks()
  }, 10000)
}

// stopPolling clears the owned refresh interval and forgets its handle. Calling it
// repeatedly is safe because a null handle means this dialog has no active interval.
const stopPolling = () => {
  if (pollTimer !== null) {
    // Release the owned browser interval before forgetting its handle, allowing later
    // openings to create exactly one replacement timer.
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

// handleClose stops polling, clears confirmation and busy state, and emits close. Pending
// API operations are not canceled here, so their completion handlers can still update local
// task state.
const handleClose = () => {
  stopPolling()
  confirmVisible.value = false
  cancelConfirmVisible.value = false
  canceling.value = false
  cancelTarget.value = null
  submitting.value = false
  emit('close')
}

// statusLabel translates recognized cleanup states and returns other status strings
// verbatim. This lets task rows display a server status even when the current locale has no
// mapped label.
const statusLabel = (status: string) => {
  const map: Record<string, string> = {
    pending: t('admin.usage.cleanup.status.pending'),
    running: t('admin.usage.cleanup.status.running'),
    succeeded: t('admin.usage.cleanup.status.succeeded'),
    failed: t('admin.usage.cleanup.status.failed'),
    canceled: t('admin.usage.cleanup.status.canceled'),
  }
  return map[status] || status
}

// statusClass returns the badge palette for a cleanup task state. Unrecognized states
// receive the neutral gray palette so task rendering can continue for future server states.
const statusClass = (status: string) => {
  const map: Record<string, string> = {
    pending: 'bg-amber-100 text-amber-700 dark:bg-amber-500/20 dark:text-amber-200',
    running: 'bg-blue-100 text-blue-700 dark:bg-blue-500/20 dark:text-blue-200',
    succeeded: 'bg-emerald-100 text-emerald-700 dark:bg-emerald-500/20 dark:text-emerald-200',
    failed: 'bg-rose-100 text-rose-700 dark:bg-rose-500/20 dark:text-rose-200',
    canceled: 'bg-gray-200 text-gray-600 dark:bg-dark-600 dark:text-gray-300',
  }
  return map[status] || 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
}

// formatDateTime formats valid timestamps using the browser locale and timezone. Missing
// values produce two dashes, while invalid timestamp strings remain visible verbatim for
// inspection in task ranges.
const formatDateTime = (value?: string | null) => {
  if (!value) return '--'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

// formatRange joins the formatted beginning and ending timestamps of cleanup filters.
const formatRange = (task: UsageCleanupTask) => {
  const start = formatDateTime(task.filters.start_time)
  const end = formatDateTime(task.filters.end_time)
  return `${start} ~ ${end}`
}

// getUserTimezone reads the browser timezone for interpreting cleanup dates. If the Intl
// timezone lookup throws, it returns UTC so the cleanup request still has a timezone value.
const getUserTimezone = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone
  } catch {
    // If the browser cannot resolve its timezone, use UTC so the server still receives an
    // explicit date interpretation.
    return 'UTC'
  }
}

// loadTasks retrieves the selected cleanup task page when the dialog is visible. Successful
// responses replace rows and available pagination fields; failures retain existing rows and
// show a notification.
const loadTasks = async () => {
  if (!props.show) return
  tasksLoading.value = true
  try {
    const res = await adminUsageAPI.listCleanupTasks({
      page: tasksPage.value,
      page_size: tasksPageSize.value,
    })
    tasks.value = res.items || []
    tasksTotal.value = res.total || 0
    if (res.page) {
      tasksPage.value = res.page
    }
    if (res.page_size) {
      tasksPageSize.value = res.page_size
    }
  } catch (error) {
    // Keep the prior task page available after retrieval fails, and report the error so
    // the user can retry the visible listing.
    console.error('Failed to load cleanup tasks:', error)
    appStore.showError(t('admin.usage.cleanup.loadFailed'))
  } finally {
    tasksLoading.value = false
  }
}

// handleTaskPageChange selects the requested page and starts loading its cleanup tasks.
const handleTaskPageChange = (page: number) => {
  tasksPage.value = page
  loadTasks()
}

// handleTaskPageSizeChange accepts a positive finite task page size and reloads page one.
// Invalid sizes are ignored so pagination cannot issue a request with a nonpositive size.
const handleTaskPageSizeChange = (size: number) => {
  if (!Number.isFinite(size) || size <= 0) return
  tasksPageSize.value = size
  tasksPage.value = 1
  loadTasks()
}

// openConfirm opens the confirmation dialog for the current cleanup draft.
const openConfirm = () => {
  confirmVisible.value = true
}

// canCancel reports whether a task remains pending or currently running.
const canCancel = (task: UsageCleanupTask) => {
  return task.status === 'pending' || task.status === 'running'
}

// openCancelConfirm selects the task and opens its cancellation confirmation dialog.
const openCancelConfirm = (task: UsageCleanupTask) => {
  cancelTarget.value = task
  cancelConfirmVisible.value = true
}

// buildPayload validates the cleanup date range and selects supported filters for
// submission. Missing dates produce a notification and null, which submitCleanup treats as
// a canceled confirmation. Positive entity identifiers and nonempty model values become
// optional predicates.
//
// A selected request type supplies its compatible legacy stream value when one exists;
// otherwise the existing stream predicate is used only when request type is absent.
// Nullable billing type keeps zero meaningful. The browser timezone accompanies calendar
// dates so the server interprets the selected range.
const buildPayload = (): CreateUsageCleanupTaskRequest | null => {
  if (!localStartDate.value || !localEndDate.value) {
    appStore.showError(t('admin.usage.cleanup.missingRange'))
    return null
  }

  const payload: CreateUsageCleanupTaskRequest = {
    start_date: localStartDate.value,
    end_date: localEndDate.value,
    timezone: getUserTimezone(),
  }

  if (localFilters.value.user_id && localFilters.value.user_id > 0) {
    payload.user_id = localFilters.value.user_id
  }
  if (localFilters.value.api_key_id && localFilters.value.api_key_id > 0) {
    payload.api_key_id = localFilters.value.api_key_id
  }
  if (localFilters.value.provider_id && localFilters.value.provider_id > 0) {
    payload.provider_id = localFilters.value.provider_id
  }
  if (localFilters.value.group_id && localFilters.value.group_id > 0) {
    payload.group_id = localFilters.value.group_id
  }
  if (localFilters.value.team_id && localFilters.value.team_id > 0) {
    payload.team_id = localFilters.value.team_id
  }
  if (localFilters.value.model) {
    payload.model = localFilters.value.model
  }
  // Canonical request type takes precedence over legacy stream; send both only when that
  // type has a compatible stream representation.
  if (localFilters.value.request_type) {
    payload.request_type = localFilters.value.request_type
    const legacyStream = requestTypeToLegacyStream(localFilters.value.request_type)
    if (legacyStream !== null && legacyStream !== undefined) {
      payload.stream = legacyStream
    }
  } else if (localFilters.value.stream !== null && localFilters.value.stream !== undefined) {
    payload.stream = localFilters.value.stream
  }
  if (localFilters.value.billing_type !== null && localFilters.value.billing_type !== undefined) {
    payload.billing_type = localFilters.value.billing_type
  }

  return payload
}

// submitCleanup creates a task from the validated local filter draft. Invalid drafts close
// confirmation without sending; successful submissions notify and refresh tasks, while
// failures notify and release the busy flag.
const submitCleanup = async () => {
  const payload = buildPayload()
  if (!payload) {
    confirmVisible.value = false
    return
  }
  submitting.value = true
  confirmVisible.value = false
  try {
    await adminUsageAPI.createCleanupTask(payload)
    appStore.showSuccess(t('admin.usage.cleanup.submitSuccess'))
    loadTasks()
  } catch (error) {
    // Creation failure leaves the draft intact for another attempt; report it before the
    // finally block releases the submission control.
    console.error('Failed to create cleanup task:', error)
    appStore.showError(t('admin.usage.cleanup.submitFailed'))
  } finally {
    submitting.value = false
  }
}

// cancelTask requests cancellation of the selected cleanup task and refreshes the task list
// after success. Missing selection closes confirmation; every attempted request clears the
// selection and busy flag afterward.
const cancelTask = async () => {
  const task = cancelTarget.value
  if (!task) {
    cancelConfirmVisible.value = false
    return
  }
  canceling.value = true
  cancelConfirmVisible.value = false
  try {
    await adminUsageAPI.cancelCleanupTask(task.id)
    appStore.showSuccess(t('admin.usage.cleanup.cancelSuccess'))
    loadTasks()
  } catch (error) {
    // Cancellation failure retains the listed task status until another refresh; the
    // selected cancellation target is cleared by the finally block.
    console.error('Failed to cancel cleanup task:', error)
    appStore.showError(t('admin.usage.cleanup.cancelFailed'))
  } finally {
    canceling.value = false
    cancelTarget.value = null
  }
}

watch(
  () => props.show,
  (show) => {
    if (show) {
      resetFilters()
      loadTasks()
      startPolling()
    } else {
      stopPolling()
    }
  },
)

// Unmount releases the interval owned by this dialog so task polling cannot continue after
// its controls leave the page.
onUnmounted(() => {
  stopPolling()
})
</script>
