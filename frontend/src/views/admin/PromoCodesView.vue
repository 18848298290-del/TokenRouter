<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <div class="flex flex-wrap items-center gap-2">
          <div class="min-w-0 flex-1 sm:max-w-64">
            <input
              v-model="searchQuery"
              type="text"
              :placeholder="t('admin.promo.searchCodes')"
              class="input"
              @input="handleSearch"
            />
          </div>
          <Select
            v-model="filters.status"
            :options="filterStatusOptions"
            class="w-36 shrink-0"
            @change="loadCodes"
          />

          <div
            class="flex w-full flex-wrap items-center justify-end gap-2 sm:ml-auto sm:w-auto sm:shrink-0"
          >
            <button
              @click="loadCodes"
              :disabled="loading"
              class="btn btn-secondary shrink-0 btn-icon"
              :title="t('common.refresh')"
            >
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            </button>
            <button
              @click="showCreateDialog = true"
              class="btn btn-primary whitespace-nowrap px-3 sm:px-4"
            >
              <Icon name="plus" size="sm" class="mr-1" />
              {{ t('admin.promo.createCode') }}
            </button>
          </div>
        </div>
      </template>

      <template #table>
        <DataTable
          column-order-storage-key="admin-promo-codes-column-order"
          :columns="columns"
          :data="codes"
          :loading="loading"
          :server-side-sort="true"
          default-sort-key="created_at"
          default-sort-order="desc"
          @sort="handleSort"
        >
          <template #cell-code="{ value }">
            <div class="flex items-center space-x-2">
              <code class="font-mono text-sm text-gray-900 dark:text-gray-100">{{ value }}</code>
              <button
                @click="copyToClipboard(value)"
                :class="[
                  'flex items-center transition-colors',
                  copiedCode === value
                    ? 'text-green-500'
                    : 'text-gray-400 hover:text-gray-600 dark:hover:text-gray-300',
                ]"
                :title="copiedCode === value ? t('admin.promo.copied') : t('keys.copyToClipboard')"
              >
                <Icon v-if="copiedCode !== value" name="copy" size="sm" :stroke-width="2" />
                <Icon name="check" size="sm" :animate-on-hover="false" v-else class="h-4 w-4" />
              </button>
            </div>
          </template>

          <template #cell-bonus_amount="{ value }">
            <span class="text-sm font-medium text-gray-900 dark:text-white">
              {{ formatBalanceAmount(value, { fractionDigits: 2 }) }}
            </span>
          </template>

          <template #cell-usage="{ row }">
            <span class="text-sm text-gray-600 dark:text-gray-300">
              {{ row.used_count }} / {{ row.max_uses === 0 ? '∞' : row.max_uses }}
            </span>
          </template>

          <template #cell-status="{ value, row }">
            <span :class="['badge', getStatusClass(value, row)]">
              {{ getStatusLabel(value, row) }}
            </span>
          </template>

          <template #cell-expires_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">
              {{ value ? formatDateTime(value) : t('admin.promo.neverExpires') }}
            </span>
          </template>

          <template #cell-created_at="{ value }">
            <span class="text-sm text-gray-500 dark:text-dark-400">
              {{ formatDateTime(value) }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center space-x-1">
              <button
                @click="copyRegisterLink(row)"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-green-50 hover:text-green-600 dark:hover:bg-green-900/20 dark:hover:text-green-400"
                :title="t('admin.promo.copyRegisterLink')"
              >
                <Icon name="link" size="sm" />
              </button>
              <button
                @click="handleViewUsages(row)"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-primary-50 hover:text-primary-600 dark:hover:bg-primary-500/8 dark:hover:text-primary-500"
                :title="t('admin.promo.viewUsages')"
              >
                <Icon name="eye" size="sm" />
              </button>
              <button
                @click="handleEdit(row)"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:hover:bg-dark-700 dark:hover:text-gray-300"
                :title="t('common.edit')"
              >
                <Icon name="edit" size="sm" />
              </button>
              <button
                @click="handleDelete(row)"
                class="flex flex-col items-center gap-0.5 rounded-control p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                :title="t('common.delete')"
              >
                <Icon name="trash" size="sm" />
              </button>
            </div>
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
      :show="showCreateDialog"
      :title="t('admin.promo.createCode')"
      width="normal"
      @close="showCreateDialog = false"
    >
      <form id="create-promo-form" @submit.prevent="handleCreate" class="space-y-4">
        <div>
          <label class="input-label">
            {{ t('admin.promo.code') }}
            <span class="ml-1 text-xs font-normal text-gray-400"
              >({{ t('admin.promo.autoGenerate') }})</span
            >
          </label>
          <input
            v-model="createForm.code"
            type="text"
            class="input font-mono uppercase"
            :placeholder="t('admin.promo.codePlaceholder')"
          />
        </div>
        <div>
          <label class="input-label">{{ t('admin.promo.bonusAmount') }}</label>
          <input
            v-model.number="createForm.bonus_amount"
            type="number"
            step="0.01"
            min="0"
            required
            class="input"
          />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.maxUses') }}
            <span class="ml-1 text-xs font-normal text-gray-400"
              >({{ t('admin.promo.zeroUnlimited') }})</span
            >
          </label>
          <input v-model.number="createForm.max_uses" type="number" min="0" class="input" />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.expiresAt') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <input v-model="createForm.expires_at_str" type="datetime-local" class="input" />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.notes') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <textarea
            v-model="createForm.notes"
            rows="2"
            class="input"
            :placeholder="t('admin.promo.notesPlaceholder')"
          ></textarea>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="showCreateDialog = false" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button
            type="submit"
            form="create-promo-form"
            :disabled="creating"
            class="btn btn-primary"
          >
            {{ creating ? t('common.creating') : t('common.create') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="showEditDialog"
      :title="t('admin.promo.editCode')"
      width="normal"
      @close="closeEditDialog"
    >
      <form id="edit-promo-form" @submit.prevent="handleUpdate" class="space-y-4">
        <div>
          <label class="input-label">{{ t('admin.promo.code') }}</label>
          <input v-model="editForm.code" type="text" class="input font-mono uppercase" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.promo.bonusAmount') }}</label>
          <input
            v-model.number="editForm.bonus_amount"
            type="number"
            step="0.01"
            min="0"
            required
            class="input"
          />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.maxUses') }}
            <span class="ml-1 text-xs font-normal text-gray-400"
              >({{ t('admin.promo.zeroUnlimited') }})</span
            >
          </label>
          <input v-model.number="editForm.max_uses" type="number" min="0" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.promo.status') }}</label>
          <Select v-model="editForm.status" :options="statusOptions" />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.expiresAt') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <input v-model="editForm.expires_at_str" type="datetime-local" class="input" />
        </div>
        <div>
          <label class="input-label">
            {{ t('admin.promo.notes') }}
            <span class="ml-1 text-xs font-normal text-gray-400">({{ t('common.optional') }})</span>
          </label>
          <textarea v-model="editForm.notes" rows="2" class="input"></textarea>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" @click="closeEditDialog" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button type="submit" form="edit-promo-form" :disabled="updating" class="btn btn-primary">
            {{ updating ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <BaseDialog
      :show="showUsagesDialog"
      :title="t('admin.promo.usageRecords')"
      width="wide"
      @close="showUsagesDialog = false"
    >
      <ContentSkeleton v-if="usagesLoading" :rows="4" class="py-4" />
      <div
        v-else-if="usages.length === 0"
        class="py-8 text-center text-gray-500 dark:text-gray-400"
      >
        {{ t('admin.promo.noUsages') }}
      </div>
      <div v-else class="space-y-3">
        <div
          v-for="usage in usages"
          :key="usage.id"
          class="flex items-center justify-between rounded-control border border-gray-200 p-3 dark:border-dark-600"
        >
          <div class="flex items-center gap-3">
            <div
              class="flex h-8 w-8 items-center justify-center rounded-full bg-green-100 dark:bg-green-900/30"
            >
              <Icon name="user" size="sm" class="text-green-600 dark:text-green-400" />
            </div>
            <div>
              <p class="text-sm font-medium text-gray-900 dark:text-white">
                {{ usage.user?.email || t('admin.promo.userPrefix', { id: usage.user_id }) }}
              </p>
              <p class="text-xs text-gray-500 dark:text-gray-400">
                {{ formatDateTime(usage.used_at) }}
              </p>
            </div>
          </div>
          <div class="text-right">
            <span class="text-sm font-medium text-green-600 dark:text-green-400">
              +{{ formatBalanceAmount(usage.bonus_amount, { fractionDigits: 2 }) }}
            </span>
          </div>
        </div>

        <div v-if="usagesTotal > usagesPageSize" class="mt-4">
          <Pagination
            :page="usagesPage"
            :total="usagesTotal"
            :page-size="usagesPageSize"
            @update:page="handleUsagesPageChange"
            @update:page-size="
              (size: number) => {
                usagesPageSize = size
                usagesPage = 1
                loadUsages()
              }
            "
          />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end">
          <button type="button" @click="showUsagesDialog = false" class="btn btn-secondary">
            {{ t('common.close') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('admin.promo.deleteCode')"
      :message="t('admin.promo.deleteCodeConfirm')"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      danger
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />
  </AppLayout>
</template>

<script setup lang="ts">
import ContentSkeleton from '@/components/common/ContentSkeleton.vue'
import { ref, reactive, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useClipboard } from '@/composables/useClipboard'
import { COPY_FEEDBACK_MS } from '@/constants/ui'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import { adminAPI } from '@/api/admin'
import { formatDateTime, formatDateTimeLocalInput } from '@/utils/format'
import type { PromoCode, PromoCodeUsage } from '@/api/admin/promo.types'
import type { Column } from '@/components/common/table/types'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/table/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'

// PromoCodesView.vue manages registration promotion codes, their editable limits, and
// paginated usage history. The main list has cancellable reloads that publish results
// only while their controller remains current. Usage history has its own dialog and
// pagination, allowing it to load independently of the main list.
//
// Displayed status combines configured status, expiry, and usage limits, with expiry
// taking precedence over exhaustion. Create requests omit empty optional values, while
// updates use zero to clear expiry. Clipboard feedback identifies the copied code
// temporarily, and registration links encode the code into a query parameter before
// copying through browser APIs.

// Translation, toast notifications, clipboard access, and balance formatting supply
// shared services for promotion-code actions.
const { t } = useI18n()
const appStore = useAppStore()
const { copyToClipboard: clipboardCopy } = useClipboard()
const { formatBalanceAmount } = useBalanceDisplay()

// List rows, operation flags, search text, and copied-code feedback represent the main
// promotion-code table state.
const codes = ref<PromoCode[]>([])
const loading = ref(false)
const creating = ref(false)
const updating = ref(false)
const searchQuery = ref('')
const copiedCode = ref<string | null>(null)

// The optional status condition and pagination/sort drafts determine the promotion-code
// list request sent by loadCodes.
const filters = reactive({
  status: '', // configured lifecycle status
})

const pagination = reactive({
  page: 1, // requested page number
  page_size: getPersistedPageSize(), // requested row count
  total: 0, // matching row count
})
const sortState = reactive({
  sort_by: 'created_at', // server sorting field
  sort_order: 'desc' as 'asc' | 'desc', // server sorting direction
})

// Dialog visibility and selected edit/delete records separate creation, editing, usage
// inspection, and deletion confirmation flows.
const showCreateDialog = ref(false)
const showEditDialog = ref(false)
const showDeleteDialog = ref(false)
const showUsagesDialog = ref(false)

const editingCode = ref<PromoCode | null>(null)
const deletingCode = ref<PromoCode | null>(null)

// Usage rows, loading state, selected code, and pagination belong to the independently
// loaded usage-history dialog.
const usages = ref<PromoCodeUsage[]>([])
const usagesLoading = ref(false)
const currentViewingCode = ref<PromoCode | null>(null)
const usagesPage = ref(1)
const usagesPageSize = ref(20)
const usagesTotal = ref(0)

// Creation values use empty optional strings and zero maximum uses, while the edit
// draft holds the selected code settings.
const createForm = reactive({
  code: '', // custom code text
  bonus_amount: 1, // registration balance bonus
  // A zero maximum leaves this promotion unlimited; the status display compares
  // used_count only when a positive maximum is configured.
  max_uses: 0, // maximum redemption count
  // Empty expiry remains absent during creation and becomes zero when updating,
  // allowing an existing deadline to be cleared.
  expires_at_str: '', // local expiry input
  notes: '', // administrator note text
})

const editForm = reactive({
  code: '', // custom code text
  bonus_amount: 0, // registration balance bonus
  // A zero maximum leaves this promotion unlimited; the status display compares
  // used_count only when a positive maximum is configured.
  max_uses: 0, // maximum redemption count
  status: 'active' as 'active' | 'disabled', // configured lifecycle status
  // Empty expiry remains absent during creation and becomes zero when updating,
  // allowing an existing deadline to be cleared.
  expires_at_str: '', // local expiry input
  notes: '', // administrator note text
})

// Translated status choices and table columns update with the locale and expose the
// supported server sorting keys.
const filterStatusOptions = computed(() => [
  { value: '', label: t('admin.promo.allStatus') },
  { value: 'active', label: t('admin.promo.statusActive') },
  { value: 'disabled', label: t('admin.promo.statusDisabled') },
])

const statusOptions = computed(() => [
  { value: 'active', label: t('admin.promo.statusActive') },
  { value: 'disabled', label: t('admin.promo.statusDisabled') },
])

const columns = computed<Column[]>(() => [
  { key: 'code', label: t('admin.promo.columns.code') },
  { key: 'bonus_amount', label: t('admin.promo.columns.bonusAmount'), sortable: true },
  { key: 'usage', label: t('admin.promo.columns.usage') },
  { key: 'status', label: t('admin.promo.columns.status'), sortable: true },
  { key: 'expires_at', label: t('admin.promo.columns.expiresAt'), sortable: true },
  { key: 'created_at', label: t('admin.promo.columns.createdAt'), sortable: true },
  { key: 'actions', label: t('admin.promo.columns.actions') },
])

// getStatusClass selects the badge from expiry, exhausted usage, and configured status.
// Expiry takes precedence when multiple conditions apply. A positive maximum can mark a
// code exhausted, while zero leaves usage unlimited for this display decision. Codes
// that remain eligible use their active or disabled status, keeping the badge
// consistent with the textual status selected by getStatusLabel for the same row.
const getStatusClass = (status: string, row: PromoCode) => {
  // Expiry overrides configured status and usage exhaustion because an elapsed
  // timestamp makes this code unavailable regardless of those other fields.
  if (row.expires_at && new Date(row.expires_at) < new Date()) {
    return 'badge-danger'
  }
  if (row.max_uses > 0 && row.used_count >= row.max_uses) {
    return 'badge-gray'
  }
  return status === 'active' ? 'badge-success' : 'badge-gray'
}

// getStatusLabel selects the displayed status from expiry, usage limits, and configured
// status. An expired timestamp wins over exhaustion and disabled status. Exhaustion
// applies only to positive maximum-use values, so zero remains unlimited in this
// display. Otherwise the configured active flag chooses the active or disabled
// translation, matching getStatusClass so the label and badge describe the same
// effective promotion-code state in each rendered table row.
const getStatusLabel = (status: string, row: PromoCode) => {
  // Expiry overrides configured status and usage exhaustion because an elapsed
  // timestamp makes this code unavailable regardless of those other fields.
  if (row.expires_at && new Date(row.expires_at) < new Date()) {
    return t('admin.promo.statusExpired')
  }
  if (row.max_uses > 0 && row.used_count >= row.max_uses) {
    return t('admin.promo.statusMaxUsed')
  }
  return status === 'active' ? t('admin.promo.statusActive') : t('admin.promo.statusDisabled')
}

// abortController identifies the active main-list request so replacements and unmount
// can abort its pending work.
let abortController: AbortController | null = null

// loadCodes requests the selected promotion-code page after aborting the previous load.
// Each completion checks its controller and signal before publishing rows or reporting
// errors, so overlapping searches cannot replace newer data. The current successful
// request updates rows and total together. Finally, only the current controller
// releases the loading flag and clears itself, leaving a newer request's loading state
// intact while it remains pending.
const loadCodes = async () => {
  // Cancel the previous main-list request before replacement so rapid filter changes
  // cannot leave obsolete results owning the table.
  if (abortController) {
    abortController.abort()
  }
  const currentController = new AbortController()
  abortController = currentController
  loading.value = true

  try {
    const response = await adminAPI.promo.list(
      pagination.page,
      pagination.page_size,
      {
        status: filters.status || undefined,
        search: searchQuery.value || undefined,
        sort_by: sortState.sort_by,
        sort_order: sortState.sort_order,
      },
      { signal: currentController.signal },
    )
    // Apply this page only while its controller remains active; aborted or replaced
    // requests no longer describe the latest table selection.
    if (currentController.signal.aborted || abortController !== currentController) return

    codes.value = response.items
    pagination.total = response.total
  } catch (error: any) {
    // Suppress cancellation and superseded failures because replacing a list request
    // is an expected consequence of changing search or pagination.
    if (
      currentController.signal.aborted ||
      abortController !== currentController ||
      error?.name === 'AbortError' ||
      error?.code === 'ERR_CANCELED'
    ) {
      return
    }
    appStore.showError(t('admin.promo.failedToLoad'))
    console.error('Error loading promo codes:', error)
  } finally {
    // Only the currently installed controller may clear loading, because another
    // request can start before this finalizer runs.
    if (abortController === currentController) {
      loading.value = false
      abortController = null
    }
  }
}

// searchTimeout owns the delayed text search and is cancelled by a replacement search
// or component unmount.
let searchTimeout: ReturnType<typeof setTimeout>
// handleSearch replaces the pending timer with a delayed first-page search. The
// component clears this timer on unmount, so an unexecuted search cannot start a new
// list request afterward.
const handleSearch = () => {
  clearTimeout(searchTimeout)
  searchTimeout = setTimeout(() => {
    pagination.page = 1
    loadCodes()
  }, 300)
}

// handlePageChange selects a promotion-code page and starts its server reload.
const handlePageChange = (page: number) => {
  pagination.page = page
  loadCodes()
}

// handlePageSizeChange stores the selected page size and reloads from page one.
// Resetting the current page before requesting rows presents the beginning of the
// newly sized result set.
const handlePageSizeChange = (pageSize: number) => {
  pagination.page_size = pageSize
  pagination.page = 1
  loadCodes()
}

// handleSort records the selected server field and direction, then reloads from the
// first page. The page reset presents the beginning of the reordered records under
// the current filters.
const handleSort = (key: string, order: 'asc' | 'desc') => {
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  loadCodes()
}

// copyToClipboard copies a promotion code and briefly marks that text as copied. The
// feedback timer resets copiedCode after the shared duration; unsuccessful clipboard
// copies leave the current feedback unchanged.
const copyToClipboard = async (text: string) => {
  const success = await clipboardCopy(text, t('admin.promo.copied'))
  if (success) {
    copiedCode.value = text
    // Reset copied-code feedback after the shared display interval so a successful
    // copy does not leave a permanent copied marker.
    setTimeout(() => {
      copiedCode.value = null
    }, COPY_FEEDBACK_MS)
  }
}

// handleCreate submits the draft and closes the dialog after persistence succeeds.
// Empty optional fields are omitted, and expiry is converted to epoch seconds; failure
// retains the draft for correction or retry.
const handleCreate = async () => {
  creating.value = true
  try {
    await adminAPI.promo.create({
      code: createForm.code || undefined,
      bonus_amount: createForm.bonus_amount,
      max_uses: createForm.max_uses,
      // Creation omits blank expiry, while populated local inputs become epoch
      // seconds for the promotion endpoint to store the deadline.
      expires_at: createForm.expires_at_str
        ? Math.floor(new Date(createForm.expires_at_str).getTime() / 1000)
        : undefined,
      notes: createForm.notes || undefined,
    })
    appStore.showSuccess(t('admin.promo.codeCreated'))
    showCreateDialog.value = false
    resetCreateForm()
    loadCodes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToCreate'))
  } finally {
    creating.value = false
  }
}

// resetCreateForm restores the default registration bonus and clears the optional
// code, expiry, and notes. Maximum uses returns to zero, which the promotion status
// display treats as an unlimited code.
const resetCreateForm = () => {
  createForm.code = ''
  createForm.bonus_amount = 1
  createForm.max_uses = 0
  createForm.expires_at_str = ''
  createForm.notes = ''
}

// handleEdit copies a promotion code into the editable form and opens it. A missing
// expiry becomes an empty local input, while a stored timestamp is converted through
// epoch seconds for display.
const handleEdit = (code: PromoCode) => {
  editingCode.value = code
  editForm.code = code.code
  editForm.bonus_amount = code.bonus_amount
  editForm.max_uses = code.max_uses
  editForm.status = code.status
  // Convert the server timestamp through epoch seconds before formatting the local
  // editor input; absent expiry keeps the input empty.
  editForm.expires_at_str = code.expires_at
    ? formatDateTimeLocalInput(Math.floor(new Date(code.expires_at).getTime() / 1000))
    : ''
  editForm.notes = code.notes || ''
  showEditDialog.value = true
}

// closeEditDialog hides the promotion editor and releases its selected record.
const closeEditDialog = () => {
  showEditDialog.value = false
  editingCode.value = null
}

// handleUpdate persists the selected promotion code and reloads after success. An empty
// expiry sends zero to clear it, while failed requests keep the editor and its entered
// values available.
const handleUpdate = async () => {
  if (!editingCode.value) return

  updating.value = true
  try {
    await adminAPI.promo.update(editingCode.value.id, {
      code: editForm.code,
      bonus_amount: editForm.bonus_amount,
      max_uses: editForm.max_uses,
      status: editForm.status,
      // Updates send zero for blank expiry so administrators can clear a stored
      // deadline instead of leaving the previous expiry untouched.
      expires_at: editForm.expires_at_str
        ? Math.floor(new Date(editForm.expires_at_str).getTime() / 1000)
        : 0,
      notes: editForm.notes,
    })
    appStore.showSuccess(t('admin.promo.codeUpdated'))
    closeEditDialog()
    loadCodes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToUpdate'))
  } finally {
    updating.value = false
  }
}

// copyRegisterLink copies a registration URL containing the encoded promotion code. If
// navigator.clipboard rejects, a temporary textarea supplies the legacy copy operation
// and is removed after that operation completes.
const copyRegisterLink = async (code: PromoCode) => {
  const baseUrl = window.location.origin
  const registerLink = `${baseUrl}/register?promo=${encodeURIComponent(code.code)}`

  try {
    await navigator.clipboard.writeText(registerLink)
    appStore.showSuccess(t('admin.promo.registerLinkCopied'))
  } catch {
    // If the clipboard API fails, select a temporary textarea for the legacy copy
    // command and remove it after dispatching that command.
    const textArea = document.createElement('textarea')
    textArea.value = registerLink
    document.body.appendChild(textArea)
    textArea.select()
    document.execCommand('copy')
    document.body.removeChild(textArea)
    appStore.showSuccess(t('admin.promo.registerLinkCopied'))
  }
}

// handleDelete selects a promotion code and opens its confirmation dialog.
const handleDelete = (code: PromoCode) => {
  deletingCode.value = code
  showDeleteDialog.value = true
}

// confirmDelete removes the selected promotion code and refreshes after success.
// Missing selection returns immediately, and request failure retains the dialog state
// while displaying the server detail or translated error.
const confirmDelete = async () => {
  if (!deletingCode.value) return

  try {
    await adminAPI.promo.delete(deletingCode.value.id)
    appStore.showSuccess(t('admin.promo.codeDeleted'))
    showDeleteDialog.value = false
    deletingCode.value = null
    loadCodes()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToDelete'))
  }
}

// handleViewUsages selects a promotion code and loads its first usage page. Opening
// another code resets usage pagination so its history begins at the start rather than
// the previous code's offset.
const handleViewUsages = async (code: PromoCode) => {
  currentViewingCode.value = code
  showUsagesDialog.value = true
  usagesPage.value = 1
  await loadUsages()
}

// loadUsages clears displayed usage rows before requesting the selected code's page.
// With no selected code it returns; failures report an error and leave the cleared list
// visible after loading finishes.
const loadUsages = async () => {
  if (!currentViewingCode.value) return
  usagesLoading.value = true
  usages.value = []

  try {
    const response = await adminAPI.promo.getUsages(
      currentViewingCode.value.id,
      usagesPage.value,
      usagesPageSize.value,
    )
    usages.value = response.items
    usagesTotal.value = response.total
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.promo.failedToLoadUsages'))
  } finally {
    usagesLoading.value = false
  }
}

// handleUsagesPageChange selects a usage-history page and loads its records.
const handleUsagesPageChange = (page: number) => {
  usagesPage.value = page
  loadUsages()
}

onMounted(() => {
  loadCodes()
})

// Cancel queued searches and abort the owned list request during teardown so it
// cannot publish rows after the page closes.
onUnmounted(() => {
  clearTimeout(searchTimeout)
  abortController?.abort()
})
</script>
