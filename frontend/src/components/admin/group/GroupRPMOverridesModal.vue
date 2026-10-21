<template>
  <BaseDialog
    :show="show"
    :title="t('admin.groups.rpmOverridesTitle')"
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
          {{ t('admin.groups.groupRpmDefault') }}: {{ group.rpm_limit || 0 }}
        </span>
      </div>

      <!-- User addition controls. -->
      <div class="rounded-control border border-gray-200 p-3 dark:border-dark-600">
        <h4 class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.addUserRpm') }}
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
              v-model.number="newRpm"
              type="number"
              step="1"
              min="0"
              autocomplete="off"
              class="hide-spinner input w-full"
              placeholder="100"
            />
          </div>
          <button
            type="button"
            class="btn btn-primary shrink-0"
            :disabled="!selectedUser || newRpm == null || newRpm < 0"
            @click="handleAddLocal"
          >
            {{ t('common.add') }}
          </button>
        </div>

        <div
          v-if="localEntries.length > 0"
          class="mt-3 flex items-center justify-end border-t border-gray-100 pt-3 dark:border-dark-600"
        >
          <button
            type="button"
            :disabled="clearing"
            class="rounded-control border border-red-200 bg-red-50 px-3 py-1.5 text-sm font-medium text-red-600 transition-colors hover:bg-red-100 disabled:opacity-50 dark:border-red-800 dark:bg-red-900/20 dark:text-red-400 dark:hover:bg-red-900/40"
            @click="clearAllLocal"
          >
            <Icon
              v-if="clearing"
              name="refresh"
              size="sm"
              class="mr-1 inline animate-spin"
              :animate-on-hover="false"
            />
            {{ t('admin.groups.clearAll') }}
          </button>
        </div>
      </div>

      <!-- Override list. -->
      <div>
        <h4 class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('admin.groups.rpmOverrides') }}
          <span v-if="!loading">({{ localEntries.length }})</span>
        </h4>

        <div
          v-if="!loading && localEntries.length === 0"
          class="py-6 text-center text-sm text-gray-400 dark:text-gray-500"
        >
          {{ t('admin.groups.noRpmOverrides') }}
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
                    :title="t('admin.groups.columns.rpmOverrideHint')"
                    >{{ t('admin.groups.columns.rpmOverride') }}</th
                  >
                  <th class="w-10 px-2 py-2"></th>
                </tr>
              </thead>
              <TableSkeletonBody v-if="loading" :columns="7" cell-class="px-3 py-2" />
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
                      step="1"
                      min="0"
                      autocomplete="off"
                      :value="entry.rpm_override"
                      class="input hide-spinner min-h-0 w-20 rounded-compact px-2 py-1 text-center font-medium"
                      @change="
                        updateLocalRpm(entry.user_id, ($event.target as HTMLInputElement).value)
                      "
                    />
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

      <!-- Dialog footer. -->
      <div class="flex items-center gap-3 border-t border-gray-200 pt-4 dark:border-dark-600">
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
import type { GroupRPMOverrideEntry } from '@/api/admin/groups'
import type { AdminGroup } from '@/api/groups.types'
import type { AdminUser } from '@/api/admin/users.types'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Pagination from '@/components/common/Pagination.vue'
import Icon from '@/components/icons/Icon.vue'

// GroupRPMOverridesModal.vue edits per-user group RPM overrides in a local list. Server entries
// provide the comparison and cancellation snapshot, while searches select users for additions.
// Pagination applies to the draft so unsaved rows appear immediately alongside loaded records.

// LocalEntry represents an editable user row for group RPM overrides.
interface LocalEntry extends GroupRPMOverrideEntry {}

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
const serverEntries = ref<GroupRPMOverrideEntry[]>([])
const localEntries = ref<LocalEntry[]>([])
// searchQuery, searchResults, showDropdown, and selectedUser own the user lookup and selection
// used to append override rows.
const searchQuery = ref('')
const searchResults = ref<AdminUser[]>([])
const showDropdown = ref(false)
const selectedUser = ref<AdminUser | null>(null)
// newRpm holds the selected user limit before insertion; null means no entered limit and zero
// remains valid.
const newRpm = ref<number | null>(null)
// currentPage and pageSize describe the local draft window displayed by the override table and
// pagination controls.
const currentPage = ref(1)
// pageSize sets the number of visible rows and participates in the current page calculation for
// pagination.
const pageSize = ref(10)

// searchTimeout owns the pending search debounce timer, which a subsequent input clears before
// scheduling another query.
let searchTimeout: ReturnType<typeof setTimeout>

// isDirty compares user IDs and override values against the loaded snapshot, controlling
// unsaved indicators and cancellation behavior.
const isDirty = computed(() => {
  if (localEntries.value.length !== serverEntries.value.length) return true
  const serverMap = new Map(serverEntries.value.map((e) => [e.user_id, e.rpm_override]))
  return localEntries.value.some((e) => serverMap.get(e.user_id) !== e.rpm_override)
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
const cloneEntries = (entries: GroupRPMOverrideEntry[]): LocalEntry[] => {
  return entries.map((e) => ({ ...e }))
}

// loadEntries fetches the selected group's RPM overrides and replaces the local snapshot.
// Missing groups skip the request; failures display a notification and retain the previous
// rows.
const loadEntries = async () => {
  if (!props.group) return
  loading.value = true
  try {
    serverEntries.value = await adminAPI.groups.getGroupRPMOverrides(props.group.id)
    localEntries.value = cloneEntries(serverEntries.value)
    adjustPage()
  } catch (error) {
    appStore.showError(t('admin.groups.failedToLoad'))
    console.error('Error loading RPM overrides:', error)
  } finally {
    loading.value = false
  }
}

// adjustPage moves pagination back inside the remaining draft page range. Empty lists retain
// page one, and valid current pages remain selected after local additions or deletions.
const adjustPage = () => {
  const totalPages = Math.max(1, Math.ceil(localEntries.value.length / pageSize.value))
  if (currentPage.value > totalPages) currentPage.value = totalPages
}

// Opening the modal resets user selection and pagination before reloading overrides so the
// next edit starts from the selected group snapshot.
watch(
  () => props.show,
  (val) => {
    if (val && props.group) {
      currentPage.value = 1
      searchQuery.value = ''
      searchResults.value = []
      selectedUser.value = null
      newRpm.value = null
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

// handleAddLocal adds or replaces the selected user's RPM override. Missing users, null limits,
// and negative limits are ignored; zero remains an accepted override, and successful additions
// clear selection inputs.
const handleAddLocal = () => {
  if (!selectedUser.value || newRpm.value == null || newRpm.value < 0) return
  const user = selectedUser.value
  const idx = localEntries.value.findIndex((e) => e.user_id === user.id)
  const entry: LocalEntry = {
    user_id: user.id,
    user_name: user.username || '',
    user_email: user.email,
    user_notes: user.notes || '',
    user_status: user.status || 'active',
    rpm_override: newRpm.value,
  }
  if (idx >= 0) {
    localEntries.value[idx] = entry
  } else {
    localEntries.value.push(entry)
  }
  searchQuery.value = ''
  selectedUser.value = null
  newRpm.value = null
  adjustPage()
}

// updateLocalRpm parses a decimal integer and updates the matching local user. Invalid or
// negative values retain the previous limit, while zero remains available for the configured
// override.
const updateLocalRpm = (userId: number, value: string) => {
  const num = parseInt(value, 10)
  if (isNaN(num) || num < 0) return
  const entry = localEntries.value.find((e) => e.user_id === userId)
  if (entry) entry.rpm_override = num
}

// removeLocal deletes the matching user draft and adjusts its visible page.
const removeLocal = (userId: number) => {
  localEntries.value = localEntries.value.filter((e) => e.user_id !== userId)
  adjustPage()
}

// clearing prevents concurrent server-wide RPM clears and supplies pending state to the clear
// action in the modal.
const clearing = ref(false)
// clearAllLocal persists removal of every RPM override for the selected group. It suppresses
// concurrent clears and replaces both snapshots after success; failures retain current rows and
// display an error.
const clearAllLocal = async () => {
  if (!props.group || clearing.value) return
  clearing.value = true
  try {
    // Clear server overrides before replacing either local snapshot so a failed clear retains
    // the current rows and cancellation baseline.
    await adminAPI.groups.clearGroupRPMOverrides(props.group.id)
    localEntries.value = []
    serverEntries.value = []
    appStore.showSuccess(t('admin.groups.rpmSaved'))
  } catch (error) {
    appStore.showError(t('admin.groups.failedToSave'))
    console.error('Error clearing RPM overrides:', error)
  } finally {
    clearing.value = false
  }
}

// handleCancel replaces edited rows with copies of the loaded server snapshot. It keeps the
// dialog open and adjusts pagination so cancellation returns to an existing draft page.
const handleCancel = () => {
  localEntries.value = cloneEntries(serverEntries.value)
  adjustPage()
}

// handleSave submits the current group's local RPM overrides as one batch. Successful
// persistence emits success and close events; failures retain the dialog draft and display an
// error notification.
const handleSave = async () => {
  if (!props.group) return
  saving.value = true
  try {
    const entries = localEntries.value.map((e) => ({
      user_id: e.user_id,
      rpm_override: e.rpm_override,
    }))
    // Persist the complete edited list before reporting success and requesting closure so the
    // parent refresh observes the saved overrides.
    await adminAPI.groups.batchSetGroupRPMOverrides(props.group.id, entries)
    appStore.showSuccess(t('admin.groups.rpmSaved'))
    emit('success')
    emit('close')
  } catch (error) {
    appStore.showError(t('admin.groups.failedToSave'))
    console.error('Error saving RPM overrides:', error)
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
