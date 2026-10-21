<template>
  <div ref="containerRef" class="relative">
    <div v-if="selectedUserIds.length > 0" class="mb-2 flex flex-wrap gap-2">
      <span
        v-for="userId in selectedUserIds"
        :key="userId"
        class="inline-flex max-w-full items-center gap-1.5 rounded-compact bg-gray-100 px-2.5 py-1.5 text-xs text-gray-700 dark:bg-dark-600 dark:text-gray-200"
      >
        <span class="max-w-64 truncate font-medium" :title="selectedUserLabel(userId)">
          {{ selectedUserLabel(userId) }}
        </span>
        <span class="shrink-0 text-gray-400">#{{ userId }}</span>
        <span v-if="selectedUsers[userId]?.deleted" class="shrink-0 text-gray-400">
          {{ t('admin.settings.openaiFastPolicy.userDeleted') }}
        </span>
        <button
          type="button"
          class="shrink-0 rounded-compact text-gray-400 hover:text-red-600 dark:hover:text-red-400"
          :aria-label="t('admin.settings.openaiFastPolicy.removeUser')"
          :title="t('admin.settings.openaiFastPolicy.removeUser')"
          @click="removeUser(userId)"
        >
          <Icon name="x" size="xs" :stroke-width="2" />
        </button>
      </span>
    </div>

    <div class="relative">
      <Icon name="search" size="sm" class="input-icon text-gray-400" />
      <input
        v-model="searchQuery"
        type="text"
        autocomplete="off"
        class="input w-full input-has-icon [--input-icon-slot:1.5rem]"
        :placeholder="t('admin.settings.openaiFastPolicy.userSearchPlaceholder')"
        @input="debounceSearch"
        @focus="showDropdown = true"
      />
    </div>

    <MotionTransition name="dropdown-fade">
      <div
        v-if="showDropdown && searchQuery.trim()"
        :inert="!(showDropdown && searchQuery.trim()) || undefined"
        class="dropdown z-50 mt-1 max-h-menu-sm w-full overflow-auto py-0"
      >
        <div v-if="searchLoading" class="px-4 py-3 text-sm text-gray-500 dark:text-gray-400">
          {{ t('common.loading') }}
        </div>
        <div
          v-else-if="availableResults.length === 0"
          class="px-4 py-3 text-sm text-gray-500 dark:text-gray-400"
        >
          {{ t('admin.settings.openaiFastPolicy.userSearchEmpty') }}
        </div>
        <template v-else>
          <button
            v-for="user in availableResults"
            :key="user.id"
            type="button"
            class="dropdown-item justify-between gap-3"
            @click="selectUser(user)"
          >
            <span class="min-w-0 truncate font-medium text-gray-900 dark:text-white">
              {{ user.email }}
              <span v-if="user.deleted" class="ml-1 text-xs font-normal text-gray-400">
                {{ t('admin.settings.openaiFastPolicy.userDeleted') }}
              </span>
            </span>
            <span class="shrink-0 text-xs text-gray-400">#{{ user.id }}</span>
          </button>
        </template>
      </div>
    </MotionTransition>
  </div>
</template>

<script setup lang="ts">
import MotionTransition from '@/components/common/MotionTransition.vue'
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { SimpleUser } from '@/api/admin/usage'
import Icon from '@/components/icons/Icon.vue'

/**
 * OpenAIFastPolicyUserSelector.vue edits the user IDs targeted by an OpenAI Fast
 * policy rule. Selected IDs remain the saved identity even when user lookup fails;
 * separately cached email labels improve readability and include deleted accounts.
 * Search results exclude selected users and list active users before deleted users.
 *
 * A generation counter invalidates earlier searches when input changes, a user is
 * selected, or the component unmounts. The debounce timer delays the next request,
 * while generation checks prevent a completed older request from replacing the current
 * results or loading state. Label hydration checks current selected IDs before merging
 * returned users.
 */

// modelValue belongs to the parent rule editor; this control emits replacement arrays
// after user actions and uses a normalized copy for rendering selected identifiers.
const props = defineProps<{
  modelValue: number[] // Selected rule user identifiers.
}>()

// Selection updates contain positive integer IDs with duplicates removed by the displayed selection list.
const emit = defineEmits<{
  'update:modelValue': [value: number[]] // Replacement selected user identifiers.
}>()

// Component references and search state drive the dropdown and retain cached labels across selection edits.
const { t } = useI18n()
const containerRef = ref<HTMLElement | null>(null)
const searchQuery = ref('')
const searchResults = ref<SimpleUser[]>([])
const searchLoading = ref(false)
const showDropdown = ref(false)
// Label hydration caches resolved users by ID; missing lookups keep the numeric selection visible
// through selectedUserLabel, and removing a selection does not purge this component-local cache.
const selectedUsers = ref<Record<number, SimpleUser>>({})
// searchTimer belongs to this component and is cleared before scheduling another search or unmounting.
let searchTimer: ReturnType<typeof setTimeout> | null = null
// searchSequence invalidates outstanding responses as well as queued work, because clearing the
// debounce timer alone cannot stop a request that has already reached the API.
let searchSequence = 0

// Displayed selections remove invalid and duplicate IDs without emitting an update until a user edits.
const selectedUserIds = computed(() =>
  Array.from(new Set(props.modelValue.filter((id) => Number.isInteger(id) && id > 0))),
)

// Already selected users leave the result list, and active accounts precede deleted account matches.
const availableResults = computed(() => {
  const selected = new Set(selectedUserIds.value)
  return searchResults.value
    .filter((user) => !selected.has(user.id))
    .sort((a, b) => Number(a.deleted) - Number(b.deleted))
})

/**
 * selectedUserLabel returns a cached email or the translated numeric user label.
 * Failed hydration leaves the saved ID visible and removable, so lookup availability
 * does not determine which rule targets survive.
 */
function selectedUserLabel(userId: number): string {
  return (
    selectedUsers.value[userId]?.email ||
    t('admin.settings.openaiFastPolicy.userIdFallback', { id: userId })
  )
}

/**
 * clearPendingSearch cancels the pending debounce and invalidates outstanding search
 * responses. Call it before a replacement search, after selecting a result, and during
 * unmount so late completions cannot publish results.
 */
function clearPendingSearch(): void {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
  // Increment even when no timer exists: a previously dispatched request can still complete
  // and must fail its generation check after input changes or selection ends the search.
  searchSequence += 1
}

/**
 * debounceSearch schedules a trimmed query and publishes results for the latest search
 * generation. Empty input clears the results immediately. Each request captures the
 * generation after invalidation, and its success, error, and loading completion check
 * that generation before writing state. Consequently a slower earlier query cannot
 * replace a later result or clear its loading indicator, while a current failure
 * displays the empty result state.
 */
function debounceSearch(): void {
  clearPendingSearch()
  const query = searchQuery.value.trim()
  showDropdown.value = true
  if (!query) {
    searchResults.value = []
    searchLoading.value = false
    return
  }

  const sequence = searchSequence
  // Capture the current generation for every completion branch; the 300 ms delay
  // coalesces typing while later input invalidates even a request already in flight.
  searchTimer = setTimeout(async () => {
    searchLoading.value = true
    try {
      const results = await adminAPI.usage.searchUsers(query)
      if (sequence === searchSequence) {
        searchResults.value = results
      }
    } catch {
      // A failed current query presents the empty state; stale failures cannot erase
      // results produced by a newer query that has already finished successfully.
      if (sequence === searchSequence) {
        searchResults.value = []
      }
    } finally {
      if (sequence === searchSequence) {
        searchLoading.value = false
      }
    }
  }, 300)
}

/**
 * selectUser caches the selected label and emits the updated rule IDs. It invalidates
 * pending search work before clearing the dropdown, preventing a late response from
 * repopulating results after selection.
 */
function selectUser(user: SimpleUser): void {
  selectedUsers.value = { ...selectedUsers.value, [user.id]: user }
  emit('update:modelValue', [...selectedUserIds.value, user.id])
  clearPendingSearch()
  searchQuery.value = ''
  searchResults.value = []
  searchLoading.value = false
  showDropdown.value = false
}

/**
 * removeUser emits selected identifiers excluding the requested user identifier.
 */
function removeUser(userId: number): void {
  emit(
    'update:modelValue',
    selectedUserIds.value.filter((id) => id !== userId),
  )
}

/**
 * hydrateSelectedUsers resolves missing labels for the current numeric selection.
 * Lookup requests include deleted accounts because saved rules may still reference
 * them, and individual failures become null results so the remaining labels can load.
 * After every request completes, returned users are merged only when their IDs still
 * appear in the current prop. Unresolved IDs keep their numeric fallback, and removed
 * IDs cannot be reintroduced into the selection.
 */
async function hydrateSelectedUsers(userIds: number[]): Promise<void> {
  // Resolve labels independently of selection persistence so unavailable or deleted user
  // records do not remove historical IDs from the rule currently being edited.
  const missing = userIds.filter((id) => !selectedUsers.value[id])
  if (missing.length === 0) return

  const users = await Promise.all(
    missing.map(async (id) => {
      try {
        const user = await adminAPI.users.getById(id, true)
        return {
          id: user.id,
          email: user.email,
          deleted: Boolean(user.deleted_at),
        } satisfies SimpleUser
      } catch {
        // Keep unresolved IDs selected while allowing other labels to load; the numeric
        // label still gives administrators a way to recognize and remove each target.
        return null
      }
    }),
  )

  // Hydration may finish after a rule edit; check current membership before caching labels
  // so removed targets cannot contribute new label entries from this delayed response.
  const next = { ...selectedUsers.value }
  for (const user of users) {
    if (user && props.modelValue.includes(user.id)) {
      next[user.id] = user
    }
  }
  selectedUsers.value = next
}

/**
 * handleDocumentClick closes the dropdown when a click lands outside its container.
 * Mounting installs this document listener and unmounting removes it; a missing event
 * target leaves the dropdown state unchanged.
 */
function handleDocumentClick(event: MouseEvent): void {
  const target = event.target as Node | null
  if (target && !containerRef.value?.contains(target)) {
    showDropdown.value = false
  }
}

// Hydrate the initial IDs and later prop updates; asynchronous hydration checks membership
// again after requests finish because the parent can change selection while they run.
watch(
  selectedUserIds,
  (userIds) => {
    void hydrateSelectedUsers(userIds)
  },
  { immediate: true },
)

// The document listener closes menus for outside clicks and is paired with unmount cleanup
// because this settings field can leave the page while search work remains outstanding.
onMounted(() => {
  document.addEventListener('click', handleDocumentClick)
})

// Invalidate in-flight searches before releasing the click listener so delayed request callbacks
// cannot publish dropdown state after this control leaves the component tree.
onUnmounted(() => {
  clearPendingSearch()
  document.removeEventListener('click', handleDocumentClick)
})
</script>
