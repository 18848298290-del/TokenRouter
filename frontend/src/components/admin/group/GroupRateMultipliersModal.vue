<template>
  <BaseDialog
    :show="show"
    :title="t('admin.groups.rateMultipliersTitle')"
    width="wide"
    @close="handleClose"
  >
    <div v-if="group" class="space-y-4">
      <!-- Group information. -->
      <div
        class="flex flex-wrap items-center gap-3 rounded-control bg-gray-50 px-4 py-2.5 text-sm dark:bg-dark-700"
      >
        <span class="font-medium text-gray-900 dark:text-white">{{ group.name }}</span>
        <span class="text-gray-400">|</span>
        <span class="text-gray-600 dark:text-gray-400">
          {{ t('admin.groups.columns.rateMultiplier') }}: {{ group.rate_multiplier }}x
        </span>
      </div>

      <!-- Override actions. -->
      <div class="rounded-control border border-gray-200 p-3 dark:border-dark-600">
        <!-- User addition controls. -->
        <h4 class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.addUserRate') }}
        </h4>
        <div class="flex items-end gap-2">
          <div class="relative flex-1">
            <input
              v-model="searchQuery"
              type="text"
              autocomplete="off"
              class="input w-full"
              :placeholder="t('admin.groups.searchUserPlaceholder')"
              @input="handleSearchUsers"
              @focus="showDropdown = true"
            />
            <MotionTransition name="dropdown-fade">
              <div
                v-if="showDropdown && searchResults.length > 0"
                :inert="!(showDropdown && searchResults.length > 0) || undefined"
                class="dropdown left-0 right-0 top-full z-10 mt-1 max-h-48 overflow-y-auto py-0"
              >
                <button
                  v-for="user in searchResults"
                  :key="user.id"
                  type="button"
                  class="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-800"
                  @click="selectUser(user)"
                >
                  <span class="text-gray-400">#{{ user.id }}</span>
                  <span class="text-gray-900 dark:text-white">{{
                    user.username || user.email
                  }}</span>
                  <span v-if="user.username" class="text-xs text-gray-400">{{ user.email }}</span>
                </button>
              </div>
            </MotionTransition>
          </div>
          <div class="w-24">
            <input
              v-model.number="newRate"
              type="number"
              step="0.001"
              min="0"
              autocomplete="off"
              class="hide-spinner input w-full"
              placeholder="1.0"
            />
          </div>
          <button
            type="button"
            class="btn btn-primary shrink-0"
            :disabled="!selectedUser || !newRate"
            @click="handleAddLocal"
          >
            {{ t('common.add') }}
          </button>
        </div>

        <!-- Batch multiplier and clear actions. -->
        <div
          v-if="localEntries.length > 0"
          class="mt-3 flex items-center gap-2 border-t border-gray-100 pt-3 dark:border-dark-600"
        >
          <span class="text-xs font-medium text-gray-500 dark:text-gray-400">{{
            t('admin.groups.batchAdjust')
          }}</span>
          <div class="flex items-center gap-1.5">
            <span class="text-xs text-gray-400">×</span>
            <input
              v-model.number="batchFactor"
              type="number"
              step="0.1"
              min="0"
              autocomplete="off"
              class="input hide-spinner min-h-0 w-20 rounded-compact px-2 py-1 text-center"
              placeholder="0.5"
            />
            <button
              type="button"
              class="btn btn-primary btn-sm shrink-0 px-2.5 py-1 text-xs"
              :disabled="!batchFactor || batchFactor <= 0"
              @click="applyBatchFactor"
            >
              {{ t('admin.groups.applyMultiplier') }}
            </button>
          </div>
          <div class="ml-auto">
            <button
              type="button"
              class="rounded-control border border-red-200 bg-red-50 px-3 py-1.5 text-sm font-medium text-red-600 transition-colors hover:bg-red-100 dark:border-red-800 dark:bg-red-900/20 dark:text-red-400 dark:hover:bg-red-900/40"
              @click="clearAllLocal"
            >
              {{ t('admin.groups.clearAll') }}
            </button>
          </div>
        </div>
      </div>

      <!-- Configured user overrides. -->
      <div>
        <h4 class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.rateMultipliers') }}
          <span v-if="!loading">({{ localEntries.length }})</span>
        </h4>

        <div
          v-if="!loading && localEntries.length === 0"
          class="py-6 text-center text-sm text-gray-400 dark:text-gray-500"
        >
          {{ t('admin.groups.noRateMultipliers') }}
        </div>

        <!-- The scrollable table body and pagination share a frame so navigation remains visible while administrators inspect longer override lists. -->
        <div
          v-else
          class="overflow-hidden rounded-surface border border-gray-200 dark:border-dark-600"
        >
          <div class="max-h-panel overflow-auto">
            <table :aria-busy="loading" class="w-full min-w-max text-sm">
              <thead class="sticky top-0 z-[1]">
                <!-- check-ui-allow: local sticky header stacking inside the modal -->
                <tr
                  class="border-b border-gray-200 bg-gray-50 dark:border-dark-600 dark:bg-dark-700"
                >
                  <th
                    class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                    >{{ t('admin.groups.columns.userEmail') }}</th
                  >
                  <th
                    class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                    >ID</th
                  >
                  <th
                    class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                    >{{ t('admin.groups.columns.userName') }}</th
                  >
                  <th
                    class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                    >{{ t('admin.groups.columns.userNotes') }}</th
                  >
                  <th
                    class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                    >{{ t('admin.groups.columns.userStatus') }}</th
                  >
                  <th
                    class="px-3 py-2 text-left text-xs font-medium text-gray-500 dark:text-gray-400"
                    >{{ t('admin.groups.columns.rateMultiplier') }}</th
                  >
                  <th
                    v-if="showFinalRate"
                    class="px-3 py-2 text-left text-xs font-medium text-primary-600 dark:text-primary-400"
                    >{{ t('admin.groups.finalRate') }}</th
                  >
                  <th class="w-10 px-2 py-2"></th>
                </tr>
              </thead>
              <TableSkeletonBody
                v-if="loading"
                :columns="showFinalRate ? 8 : 7"
                cell-class="px-3 py-2"
              />
              <tbody v-else class="divide-y divide-gray-100 dark:divide-dark-600">
                <tr
                  v-for="entry in paginatedLocalEntries"
                  :key="entry.user_id"
                  class="hover:bg-gray-50 dark:hover:bg-dark-700/50"
                >
                  <td class="px-3 py-2 text-gray-600 dark:text-gray-400">{{ entry.user_email }}</td>
                  <td class="whitespace-nowrap px-3 py-2 text-gray-400 dark:text-gray-500">{{
                    entry.user_id
                  }}</td>
                  <td class="whitespace-nowrap px-3 py-2 text-gray-900 dark:text-white">{{
                    entry.user_name || '-'
                  }}</td>
                  <td
                    class="max-w-[160px] truncate px-3 py-2 text-gray-500 dark:text-gray-400"
                    :title="entry.user_notes"
                    >{{ entry.user_notes || '-' }}</td
                  >
                  <td class="whitespace-nowrap px-3 py-2">
                    <span
                      :class="[
                        'inline-flex rounded-full px-2 py-0.5 text-xs font-medium',
                        entry.user_status === 'active'
                          ? 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400'
                          : 'bg-gray-100 text-gray-600 dark:bg-dark-600 dark:text-gray-400',
                      ]"
                    >
                      {{ entry.user_status }}
                    </span>
                  </td>
                  <td class="whitespace-nowrap px-3 py-2">
                    <input
                      type="number"
                      step="0.001"
                      min="0.001"
                      autocomplete="off"
                      :value="entry.rate_multiplier ?? ''"
                      :placeholder="String(props.group?.rate_multiplier ?? 1)"
                      class="input hide-spinner min-h-0 w-20 rounded-compact px-2 py-1 text-center font-medium"
                      @change="
                        updateLocalRate(entry.user_id, ($event.target as HTMLInputElement).value)
                      "
                    />
                  </td>
                  <td
                    v-if="showFinalRate"
                    class="whitespace-nowrap px-3 py-2 font-medium text-primary-600 dark:text-primary-400"
                  >
                    {{ computeFinalRate(entry.rate_multiplier) }}
                  </td>
                  <td class="px-2 py-2">
                    <button
                      type="button"
                      class="rounded-compact p-1 text-gray-400 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
                      @click="removeLocal(entry.user_id)"
                    >
                      <Icon name="trash" size="sm" />
                    </button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Draft pagination. -->
          <Pagination
            v-if="!loading"
            :total="localEntries.length"
            :page="currentPage"
            :page-size="pageSize"
            @update:page="currentPage = $event"
            @update:pageSize="handlePageSizeChange"
          />
        </div>
      </div>

      <!-- Footer actions. -->
      <div class="flex items-center gap-3 border-t border-gray-200 pt-4 dark:border-dark-600">
        <!-- Unsaved changes and cancellation. -->
        <template v-if="isDirty">
          <span class="text-xs text-amber-600 dark:text-amber-400">{{
            t('admin.groups.unsavedChanges')
          }}</span>
          <button
            type="button"
            class="text-xs font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
            @click="handleCancel"
          >
            {{ t('admin.groups.revertChanges') }}
          </button>
        </template>
        <!-- Close and save actions. -->
        <div class="ml-auto flex items-center gap-3">
          <button type="button" class="btn btn-sm px-4 py-1.5" @click="handleClose">
            {{ t('common.close') }}
          </button>
          <button
            v-if="isDirty"
            type="button"
            class="btn btn-primary btn-sm px-4 py-1.5"
            :disabled="saving"
            @click="handleSave"
          >
            <Icon
              v-if="saving"
              name="refresh"
              size="sm"
              class="mr-1 animate-spin"
              :animate-on-hover="false"
            />
            {{ t('common.save') }}
          </button>
        </div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import TableSkeletonBody from '@/components/common/TableSkeletonBody.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { GroupRateMultiplierEntry } from '@/api/admin/groups'
import type { AdminGroup } from '@/api/groups.types'
import type { AdminUser } from '@/api/admin/users.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'

// GroupRateMultipliersModal.vue edits per-user group rate multipliers in a local list. Server
// entries provide the comparison and cancellation snapshot, while searches select users for
// additions. Pagination applies to the draft so unsaved rows appear immediately alongside
// loaded records.

// LocalEntry represents an editable user row for group rate multipliers.
interface LocalEntry extends GroupRateMultiplierEntry {}

// props supplies parent-owned editor values and display controls consumed by the fields below.
const props = defineProps<{
  show: boolean // dialog visibility flag
  group: AdminGroup | null // selected administrator group
}>()

// emit forwards typed edits and actions to the parent that owns the displayed draft.
const emit = defineEmits<{
  close: [] // request dialog closure
  success: [] // successful save notification
}>()

// t resolves the current locale messages used by labels, validation, and action notifications.
const { t } = useI18n()
// appStore displays operation notifications after requests complete or validation prevents the
// requested action.
const appStore = useAppStore()

// loading tracks the current list request so the table can present pending data to
// administrators.
const loading = ref(false)
// saving records the batch persistence attempt so the modal can disable its save control while
// awaiting the API.
const saving = ref(false)
// serverEntries is the loaded comparison snapshot; localEntries contains independently copied
// rows for editing and cancellation.
const serverEntries = ref<GroupRateMultiplierEntry[]>([])
const localEntries = ref<LocalEntry[]>([])
// searchQuery, searchResults, showDropdown, and selectedUser own the user lookup and selection
// used to append override rows.
const searchQuery = ref('')
const searchResults = ref<AdminUser[]>([])
const showDropdown = ref(false)
const selectedUser = ref<AdminUser | null>(null)
// newRate holds the selected user multiplier before insertion, and the add handler rejects a
// falsy draft value.
const newRate = ref<number | null>(null)
// currentPage and pageSize describe the local draft window displayed by the override table and
// pagination controls.
const currentPage = ref(1)
// pageSize sets the number of visible rows and participates in the current page calculation for
// pagination.
const pageSize = ref(10)
// batchFactor holds a pending positive multiplier for preview and batch application to
// configured local user rates.
const batchFactor = ref<number | null>(null)

// searchTimeout owns the pending search debounce timer, which a subsequent input clears before
// scheduling another query.
let searchTimeout: ReturnType<typeof setTimeout>

// showFinalRate exposes the preview column when the pending factor is positive and differs from
// the identity multiplier.
const showFinalRate = computed(() => {
  return batchFactor.value != null && batchFactor.value > 0 && batchFactor.value !== 1
})

// computeFinalRate previews the user's multiplier with the pending batch factor. A missing user
// multiplier uses the group multiplier or one; an absent factor returns that base, and products
// are rounded to six decimals.
const computeFinalRate = (rate: number | null | undefined) => {
  const base = rate ?? props.group?.rate_multiplier ?? 1
  if (!batchFactor.value) return base
  // Round the preview to the persisted draft precision.
  return parseFloat((base * batchFactor.value).toFixed(6))
}

// isDirty compares user IDs and override values against the loaded snapshot, controlling
// unsaved indicators and cancellation behavior.
const isDirty = computed(() => {
  if (localEntries.value.length !== serverEntries.value.length) return true
  const serverMap = new Map(serverEntries.value.map((e) => [e.user_id, e.rate_multiplier ?? null]))
  return localEntries.value.some((e) => serverMap.get(e.user_id) !== (e.rate_multiplier ?? null))
})

// paginatedLocalEntries slices the local draft using currentPage and pageSize so unsaved
// additions participate in pagination.
const paginatedLocalEntries = computed(() => {
  const start = (currentPage.value - 1) * pageSize.value
  return localEntries.value.slice(start, start + pageSize.value)
})

// cloneEntries copies each user entry into an independently editable row object. The returned
// list and row records can be changed by the draft while the loaded comparison snapshot retains
// its field values.
const cloneEntries = (entries: GroupRateMultiplierEntry[]): LocalEntry[] => {
  return entries.map((e) => ({ ...e }))
}

// loadEntries fetches the selected group's rate multipliers and replaces the local snapshot.
// Missing groups skip the request; failures display a notification and retain the previous
// rows.
const loadEntries = async () => {
  if (!props.group) return
  loading.value = true
  try {
    const raw = await adminAPI.groups.getGroupRateMultipliers(props.group.id)
    // Rows lacking a rate multiplier belong to the RPM editor, so filtering them keeps this
    // draft limited to multiplier overrides.
    serverEntries.value = raw.filter((e) => e.rate_multiplier != null)
    localEntries.value = cloneEntries(serverEntries.value)
    adjustPage()
  } catch (error) {
    appStore.showError(t('admin.groups.failedToLoad'))
    console.error('Error loading group rate multipliers:', error)
  } finally {
    loading.value = false
  }
}

// adjustPage moves pagination back inside the remaining draft page range. Empty lists retain
// page one, and valid current pages remain selected after local additions or deletions.
const adjustPage = () => {
  const totalPages = Math.max(1, Math.ceil(localEntries.value.length / pageSize.value))
  if (currentPage.value > totalPages) {
    currentPage.value = totalPages
  }
}

// Opening the modal resets user selection and pagination before reloading overrides so the
// next edit starts from the selected group snapshot.
watch(
  () => props.show,
  (val) => {
    if (val && props.group) {
      currentPage.value = 1
      batchFactor.value = null
      searchQuery.value = ''
      searchResults.value = []
      selectedUser.value = null
      newRate.value = null
      loadEntries()
    }
  },
)

// handlePageSizeChange updates the draft page size and returns to page one.
const handlePageSizeChange = (newSize: number) => {
  pageSize.value = newSize
  currentPage.value = 1
}

// handleSearchUsers debounces the current user query and clears the selected user. Blank input
// clears and hides suggestions; failed searches clear results. Requests already dispatched can
// still complete and publish suggestions.
const handleSearchUsers = () => {
  // Replace the pending search timer before scheduling a new query so rapid keystrokes wait
  // for the final paused input.
  clearTimeout(searchTimeout)
  selectedUser.value = null
  if (!searchQuery.value.trim()) {
    searchResults.value = []
    showDropdown.value = false
    return
  }
  searchTimeout = setTimeout(async () => {
    try {
      const res = await adminAPI.users.list(1, 10, { search: searchQuery.value.trim() })
      searchResults.value = res.items
      showDropdown.value = true
    } catch {
      // A failed user lookup clears suggestions so the list cannot present results from the
      // preceding query as matches for this input.
      searchResults.value = []
    }
  }, 300)
}

// selectUser stores the chosen user and replaces the search text with their email.
const selectUser = (user: AdminUser) => {
  selectedUser.value = user
  searchQuery.value = user.email
  showDropdown.value = false
  searchResults.value = []
}

// handleAddLocal adds or replaces the selected user's multiplier draft. Missing users and falsy
// rates are ignored; successful additions reset the user search and leave persistence to the
// save action.
const handleAddLocal = () => {
  if (!selectedUser.value || !newRate.value) return
  const user = selectedUser.value
  const idx = localEntries.value.findIndex((e) => e.user_id === user.id)
  const entry: LocalEntry = {
    user_id: user.id,
    user_name: user.username || '',
    user_email: user.email,
    user_notes: user.notes || '',
    user_status: user.status || 'active',
    rate_multiplier: newRate.value,
    rpm_override: null,
  }
  if (idx >= 0) {
    localEntries.value[idx] = entry
  } else {
    localEntries.value.push(entry)
  }
  searchQuery.value = ''
  selectedUser.value = null
  newRate.value = null
  adjustPage()
}

// updateLocalRate updates a matching user's draft from the numeric input. Blank text clears the
// override to null, invalid numeric text retains its previous value, and parsed numbers replace
// it.
const updateLocalRate = (userId: number, value: string) => {
  const entry = localEntries.value.find((e) => e.user_id === userId)
  if (!entry) return
  if (value.trim() === '') {
    entry.rate_multiplier = null
    return
  }
  const num = parseFloat(value)
  if (isNaN(num)) return
  entry.rate_multiplier = num
}

// removeLocal deletes the matching user draft and adjusts its visible page.
const removeLocal = (userId: number) => {
  localEntries.value = localEntries.value.filter((e) => e.user_id !== userId)
  adjustPage()
}

// applyBatchFactor multiplies every configured draft rate by a positive pending factor. Null
// overrides remain inherited, products are rounded to six decimals, and the factor is cleared
// after application.
const applyBatchFactor = () => {
  if (!batchFactor.value || batchFactor.value <= 0) return
  for (const entry of localEntries.value) {
    if (entry.rate_multiplier != null) {
      // Round batch products consistently with the six-decimal preview.
      entry.rate_multiplier = parseFloat((entry.rate_multiplier * batchFactor.value).toFixed(6))
    }
  }
  batchFactor.value = null
}

// clearAllLocal removes every local multiplier row until save or cancellation.
const clearAllLocal = () => {
  localEntries.value = []
}

// handleCancel replaces edited rows with copies of the loaded server snapshot. It keeps the
// dialog open and adjusts pagination so cancellation returns to an existing draft page.
const handleCancel = () => {
  localEntries.value = cloneEntries(serverEntries.value)
  batchFactor.value = null
  adjustPage()
}

// handleSave submits the current group's local rate multipliers as one batch. Successful
// persistence emits success and close events; failures retain the dialog draft and display an
// error notification.
const handleSave = async () => {
  if (!props.group) return
  saving.value = true
  try {
    const entries = localEntries.value
      .filter((e) => e.rate_multiplier != null)
      .map((e) => ({
        user_id: e.user_id,
        rate_multiplier: e.rate_multiplier as number,
      }))
    // Persist the complete edited list before reporting success and requesting closure so the
    // parent refresh observes the saved overrides.
    await adminAPI.groups.batchSetGroupRateMultipliers(props.group.id, entries)
    appStore.showSuccess(t('admin.groups.rateSaved'))
    emit('success')
    emit('close')
  } catch (error) {
    appStore.showError(t('admin.groups.failedToSave'))
    console.error('Error saving rate multipliers:', error)
  } finally {
    saving.value = false
  }
}

// handleClose restores server rows when the draft differs and emits close. The parent owns
// dialog visibility, and cancellation does not submit the pending local edits.
const handleClose = () => {
  if (isDirty.value) {
    localEntries.value = cloneEntries(serverEntries.value)
  }
  emit('close')
}

// handleClickOutside hides the user search suggestions when the document receives a click.
const handleClickOutside = () => {
  showDropdown.value = false
}

// The document click handler dismisses user suggestions from outside the input. This
// registration remains active because this component has no corresponding unmount removal.
if (typeof document !== 'undefined') {
  document.addEventListener('click', handleClickOutside)
}
</script>

<style scoped>
.hide-spinner::-webkit-outer-spin-button,
.hide-spinner::-webkit-inner-spin-button {
  -webkit-appearance: none;
  margin: 0;
}
.hide-spinner {
  -moz-appearance: textfield;
}
</style>
