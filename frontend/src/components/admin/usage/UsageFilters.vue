<template>
  <div :class="flat ? 'p-4' : 'card p-6'">
    <div class="space-y-4">
      <div class="flex items-center justify-between gap-2">
        <!-- Unselected predicates may be undefined; passing null matches the shared selectors' unfiltered option while retaining the query object values. -->
        <FilterDropdown
          :active-count="activeFilterCount"
          :columns="3"
          keep-mounted
          @reset="resetPanelFilters"
        >
          <!-- Request-ID filtering removes date bounds from queries; the user ranking uses its own supported filter dimensions. -->
          <RequestIdFilterField
            v-if="mode !== 'ranking'"
            v-model="filters.request_id"
            @search="emitChange"
          />
          <FilterField v-if="mode === 'usage'" :label="t('admin.usage.teamFilter')">
            <Select
              :model-value="filters.team_id ?? null"
              @update:model-value="filters.team_id = $event"
              :options="teamOptions"
              searchable
              @change="emitChange"
            />
          </FilterField>

          <!-- User search -->
          <FilterField
            :label="t('admin.usage.userFilter')"
            :value-text="filters.user_id ? userKeyword || `#${filters.user_id}` : ''"
            @clear="clearUser"
          >
            <div ref="userSearchRef" class="usage-filter-dropdown relative">
              <input
                v-model="userKeyword"
                type="text"
                class="input pr-8"
                :placeholder="t('admin.usage.searchUserPlaceholder')"
                @input="debounceUserSearch"
                @focus="showUserDropdown = true"
              />
              <button
                v-if="filters.user_id"
                type="button"
                @click="clearUser"
                class="absolute right-2 top-0 flex h-9 items-center text-gray-400 hover:text-gray-600 dark:text-dark-400 dark:hover:text-dark-200"
                aria-label="Clear user filter"
              >
                <Icon name="x" size="sm" />
              </button>
              <MotionTransition name="dropdown-fade">
                <div
                  v-if="showUserDropdown && (userResults.length > 0 || userKeyword)"
                  :inert="
                    !(showUserDropdown && (userResults.length > 0 || userKeyword)) || undefined
                  "
                  class="dropdown z-50 mt-1 max-h-menu-sm w-full overflow-auto py-0"
                >
                  <button
                    v-for="u in userResults"
                    :key="u.id"
                    type="button"
                    @click="selectUser(u)"
                    class="dropdown-item"
                  >
                    <span
                      >{{ u.email
                      }}<span v-if="u.deleted" class="ml-1 text-xs text-gray-400"
                        >（{{ t('admin.usage.userDeletedBadge') }}）</span
                      ></span
                    >
                    <span class="text-xs text-gray-400">#{{ u.id }}</span>
                  </button>
                </div>
              </MotionTransition>
            </div>
          </FilterField>

          <!-- API-key search -->
          <FilterField
            :label="t('usage.apiKeyFilter')"
            :value-text="filters.api_key_id ? apiKeyKeyword || `#${filters.api_key_id}` : ''"
            @clear="onClearApiKey"
          >
            <div ref="apiKeySearchRef" class="usage-filter-dropdown relative">
              <input
                v-model="apiKeyKeyword"
                type="text"
                class="input pr-8"
                :placeholder="t('admin.usage.searchApiKeyPlaceholder')"
                @input="debounceApiKeySearch"
                @focus="onApiKeyFocus"
              />
              <button
                v-if="filters.api_key_id"
                type="button"
                @click="onClearApiKey"
                class="absolute right-2 top-0 flex h-9 items-center text-gray-400 hover:text-gray-600 dark:text-dark-400 dark:hover:text-dark-200"
                aria-label="Clear API key filter"
              >
                <Icon name="x" size="sm" />
              </button>
              <MotionTransition name="dropdown-fade">
                <div
                  v-if="showApiKeyDropdown && apiKeyResults.length > 0"
                  :inert="!(showApiKeyDropdown && apiKeyResults.length > 0) || undefined"
                  class="dropdown z-50 mt-1 max-h-menu-sm w-full overflow-auto py-0"
                >
                  <button
                    v-for="k in apiKeyResults"
                    :key="k.id"
                    type="button"
                    @click="selectApiKey(k)"
                    class="dropdown-item"
                  >
                    <span class="truncate">{{ k.name || `#${k.id}` }}</span>
                    <span class="text-xs text-gray-400">#{{ k.id }}</span>
                  </button>
                </div>
              </MotionTransition>
            </div>
          </FilterField>

          <!-- Model filter -->
          <FilterField :label="t('usage.model')">
            <Select
              :model-value="filters.model ?? null"
              @update:model-value="filters.model = $event"
              :options="modelOptions"
              searchable
              @change="emitChange"
            />
          </FilterField>

          <!-- Provider search -->
          <FilterField
            :label="t('admin.usage.provider')"
            :value-text="filters.provider_id ? providerKeyword || `#${filters.provider_id}` : ''"
            @clear="clearProvider"
          >
            <div ref="providerSearchRef" class="usage-filter-dropdown relative">
              <input
                v-model="providerKeyword"
                type="text"
                class="input pr-8"
                :placeholder="t('admin.usage.searchProviderPlaceholder')"
                @input="debounceProviderSearch"
                @focus="showProviderDropdown = true"
              />
              <button
                v-if="filters.provider_id"
                type="button"
                @click="clearProvider"
                class="absolute right-2 top-0 flex h-9 items-center text-gray-400 hover:text-gray-600 dark:text-dark-400 dark:hover:text-dark-200"
                aria-label="Clear provider filter"
              >
                <Icon name="x" size="sm" />
              </button>
              <MotionTransition name="dropdown-fade">
                <div
                  v-if="showProviderDropdown && (providerResults.length > 0 || providerKeyword)"
                  :inert="
                    !(showProviderDropdown && (providerResults.length > 0 || providerKeyword)) ||
                    undefined
                  "
                  class="dropdown z-50 mt-1 max-h-menu-sm w-full overflow-auto py-0"
                >
                  <button
                    v-for="a in providerResults"
                    :key="a.id"
                    type="button"
                    @click="selectProvider(a)"
                    class="dropdown-item"
                  >
                    <span class="truncate">{{ a.name }}</span>
                    <span class="text-xs text-gray-400">#{{ a.id }}</span>
                  </button>
                </div>
              </MotionTransition>
            </div>
          </FilterField>

          <!-- Request mode for usage and ranking filters. -->
          <FilterField v-if="mode !== 'errors'" :label="t('usage.type')">
            <Select
              :model-value="filters.request_type ?? null"
              @update:model-value="filters.request_type = $event"
              :options="requestTypeOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Billing source for usage and ranking filters. -->
          <FilterField v-if="mode !== 'errors'" :label="t('admin.usage.billingType')">
            <Select
              :model-value="filters.billing_type ?? null"
              @update:model-value="filters.billing_type = $event"
              :options="billingTypeOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Billing-mode selection belongs to usage records; ranking accepts the dimensions supported by its own API. -->
          <FilterField v-if="mode === 'usage'" :label="t('admin.usage.billingMode')">
            <Select
              :model-value="filters.billing_mode ?? null"
              @update:model-value="filters.billing_mode = $event"
              :options="billingModeOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Native compaction selection applies to usage records and the ranking query. -->
          <FilterField v-if="mode !== 'errors'" :label="t('usage.compactionFilter')">
            <Select
              :model-value="filters.native_compaction_v2 ?? null"
              @update:model-value="filters.native_compaction_v2 = $event"
              :options="compactionOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Error phase selector. -->
          <FilterField v-if="mode === 'errors'" :label="t('admin.ops.errorLog.type')">
            <Select
              :model-value="filters.error_phase ?? null"
              @update:model-value="filters.error_phase = $event"
              :options="errorPhaseOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Error category selector. -->
          <FilterField v-if="mode === 'errors'" :label="t('usage.errors.category')">
            <Select
              :model-value="filters.error_category ?? null"
              @update:model-value="filters.error_category = $event"
              :options="errorCategoryOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Error HTTP status selector. -->
          <FilterField v-if="mode === 'errors'" :label="t('admin.ops.errorLog.status')">
            <Select
              :model-value="filters.status_code ?? null"
              @update:model-value="filters.status_code = $event"
              :options="statusCodeOptions"
              @change="emitChange"
            />
          </FilterField>

          <!-- Group filter -->
          <FilterField :label="t('admin.usage.group')">
            <Select
              :model-value="filters.group_id ?? null"
              @update:model-value="filters.group_id = $event"
              :options="groupOptions"
              searchable
              @change="emitChange"
            />
          </FilterField>
        </FilterDropdown>

        <div v-if="showActions" class="flex flex-wrap items-center justify-end gap-2">
          <button
            type="button"
            @click="$emit('refresh')"
            class="btn btn-secondary btn-icon"
            :title="t('common.refresh')"
          >
            <Icon name="refresh" size="sm" />
          </button>
          <slot name="after-reset" />
          <template v-if="mode === 'usage'">
            <button
              type="button"
              @click="$emit('cleanup')"
              class="btn btn-danger whitespace-nowrap px-3 sm:px-4"
            >
              {{ t('admin.usage.cleanup.button') }}
            </button>
            <button
              type="button"
              @click="$emit('export')"
              :disabled="exporting"
              class="btn btn-primary whitespace-nowrap px-3 sm:px-4"
            >
              {{ t('usage.exportExcel') }}
            </button>
          </template>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import RequestIdFilterField from '@/components/common/RequestIdFilterField.vue'
import MotionTransition from '@/components/common/MotionTransition.vue'
import { ref, onMounted, onUnmounted, toRef, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import Select, { type SelectOption } from '@/components/common/Select.vue'
import FilterDropdown from '@/components/common/FilterDropdown.vue'
import FilterField from '@/components/common/FilterField.vue'
import Icon from '@/components/icons/Icon.vue'
import { COMMON_ERROR_STATUS_CODES } from '@/utils/errorBadges'
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'
import type { SimpleApiKey, SimpleUser } from '@/api/admin/usage'

// UsageFilters.vue edits shared administrator usage filters and provides user, API-key, and
// provider searches. The modelValue object remains parent-owned and is mutated through its
// ref; change events ask the page to reload. Usage, error, and ranking modes expose
// different filter subsets.
//
// User search maintains a revision shared with route-label hydration. Clearing, selecting,
// or typing advances that revision so delayed user results cannot replace a newer
// selection. API-key and provider search use their own debounce timers but do not share
// that response guard. Date changes update the shared object, while panel reset clears only
// non-date fields.

// ModelValue holds mutable query predicates shared with the parent usage page.
type ModelValue = Record<string, any>

// Props controls filter visibility, shared query state, and caller-provided model choices.
interface Props {
  // modelValue is the parent-owned query object mutated through filters; callers handle
  // change events to reload its current values.
  modelValue: ModelValue
  exporting: boolean // export operation busy flag
  startDate: string // beginning calendar date
  endDate: string // ending calendar date
  showActions?: boolean // action button visibility flag
  modelOptions?: string[] // parent-provided model name choices
  // mode selects usage filters by default; errors substitutes error predicates and ranking
  // hides billing mode and destructive/export actions.
  mode?: 'usage' | 'errors' | 'ranking'
  // flat removes the component card surface when the parent already supplies the shared
  // filter section container.
  flat?: boolean
}

// props supplies usage-mode defaults and parent-owned values used to render and edit the
// filter panel.
const props = withDefaults(defineProps<Props>(), {
  showActions: true,
  mode: 'usage',
  flat: false,
})
// emit publishes model changes and reload, reset, export, or cleanup actions for the owning
// page.
const emit = defineEmits(['update:modelValue', 'change', 'refresh', 'reset', 'export', 'cleanup'])

// Translation supplies option labels while filters keeps a ref to the parent-owned mutable
// query object.
const { t } = useI18n()
const filters = toRef(props, 'modelValue')

// Search container refs identify clicks inside each autocomplete so the document listener
// can dismiss the other dropdowns.
const userSearchRef = ref<HTMLElement | null>(null)
const apiKeySearchRef = ref<HTMLElement | null>(null)
const providerSearchRef = ref<HTMLElement | null>(null)

// User search state tracks typed text, returned candidates, visibility, debounce ownership,
// and the current response revision.
const userKeyword = ref('')
const userResults = ref<SimpleUser[]>([])
const showUserDropdown = ref(false)
let userSearchTimeout: ReturnType<typeof setTimeout> | null = null
let userSearchSequence = 0

// API-key search state tracks typed text and candidates scoped to the selected user when a
// user filter exists.
const apiKeyKeyword = ref('')
const apiKeyResults = ref<SimpleApiKey[]>([])
const showApiKeyDropdown = ref(false)
let apiKeySearchTimeout: ReturnType<typeof setTimeout> | null = null

// SimpleProvider contains provider identity and name for autocomplete selection.
interface SimpleProvider {
  id: number // provider record identifier
  name: string // provider display name
}
// Provider search state tracks the text and compact identities displayed by the provider
// autocomplete dropdown.
const providerKeyword = ref('')
const providerResults = ref<SimpleProvider[]>([])
const showProviderDropdown = ref(false)
let providerSearchTimeout: ReturnType<typeof setTimeout> | null = null

// modelOptions prepends the unfiltered choice to the model names supplied by the parent
// usage page.
const modelOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allModels') },
  ...(props.modelOptions ?? []).map((m) => ({ value: m, label: m })),
])
// Group and team choices begin with an unfiltered option and receive independent additions
// after mounting.
const groupOptions = ref<SelectOption[]>([{ value: null, label: t('admin.usage.allGroups') }])
const teamOptions = ref<SelectOption[]>([{ value: null, label: t('admin.usage.allTeams') }])
// activeFilterCount counts nonempty predicates outside dates, excluding request ID when the
// ranking mode hides that field.
const activeFilterCount = computed(
  () =>
    Object.entries(filters.value).filter(
      ([key, value]) =>
        !['start_date', 'end_date'].includes(key) &&
        !(key === 'request_id' && props.mode === 'ranking') &&
        value !== null &&
        value !== undefined &&
        String(value) !== '',
    ).length,
)

// requestTypeOptions supplies the canonical request modes accepted by usage filtering and
// their translated display labels.
const requestTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'ws_v2', label: t('usage.ws') },
  { value: 'live', label: t('usage.live') },
  { value: 'stream', label: t('usage.stream') },
  { value: 'sync', label: t('usage.sync') },
  { value: 'cyber', label: t('usage.cyber') },
])

// billingTypeOptions distinguishes balance and subscription billing while null clears the
// billing-source predicate for requests.
const billingTypeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingTypes') },
  { value: 0, label: t('admin.usage.billingTypeBalance') },
  { value: 1, label: t('admin.usage.billingTypeSubscription') },
])

// errorPhaseOptions maps backend phase identifiers to the same translated categories used
// by error type badges.
const errorPhaseOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('admin.usage.allTypes') },
  { value: 'upstream', label: t('admin.ops.errorLog.typeUpstream') },
  { value: 'provider_auth', label: t('admin.ops.errorLog.typeProviderAuth') },
  { value: 'request', label: t('admin.ops.errorLog.typeRequest') },
  { value: 'auth', label: t('admin.ops.errorLog.typeAuth') },
  { value: 'routing', label: t('admin.ops.errorLog.typeRouting') },
  { value: 'internal', label: t('admin.ops.errorLog.typeInternal') },
])

// errorCategoryCodes lists searchable backend categories; the display-only Other bucket
// cannot be translated into a query predicate.
const errorCategoryCodes = [
  'auth',
  'rate_limit',
  'quota',
  'invalid_request',
  'service_unavailable',
  'upstream',
  'internal',
  'cyber',
]

// errorCategoryOptions translates searchable category codes and adds the null choice that
// clears error category filtering.
const errorCategoryOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allCategories') },
  ...errorCategoryCodes.map((c) => ({ value: c, label: t('usage.errors.categories.' + c) })),
])

// statusCodeOptions uses common status codes independently of current rows so later-page
// errors remain selectable.
const statusCodeOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.errors.allStatuses') },
  ...COMMON_ERROR_STATUS_CODES.map((c) => ({ value: c, label: String(c) })),
])

// billingModeOptions maps token, request, image, and video billing modes to labels for
// usage filtering.
const billingModeOptions = ref<SelectOption[]>([
  { value: null, label: t('admin.usage.allBillingModes') },
  { value: 'token', label: t('admin.usage.billingModeToken') },
  { value: 'per_request', label: t('admin.usage.billingModePerRequest') },
  { value: 'image', label: t('admin.usage.billingModeImage') },
  { value: 'video', label: t('admin.usage.billingModeVideo') },
])

// compactionOptions distinguishes native and legacy compaction while null removes that
// predicate from usage selection.
const compactionOptions = computed<SelectOption[]>(() => [
  { value: null, label: t('usage.allCompactions') },
  { value: true, label: t('usage.nativeCompactionV2') },
  { value: false, label: t('usage.legacyCompaction') },
])

// emitChange notifies the parent that shared filter values need reloading.
const emitChange = () => emit('change')

// clearPendingUserSearch cancels the pending user debounce and advances its response
// revision. Already running requests continue, but their captured revisions become stale
// and prevent their results from being displayed.
const clearPendingUserSearch = () => {
  if (userSearchTimeout) {
    // Cancel scheduled work before advancing the revision so both pending and already
    // running user lookups lose publication authority together.
    clearTimeout(userSearchTimeout)
    userSearchTimeout = null
  }
  userSearchSequence += 1
}

// debounceUserSearch schedules a trimmed nonempty query after canceling the preceding user
// search. Empty text clears candidates immediately and still invalidates older responses
// through clearPendingUserSearch.
//
// The callback captures the new revision before waiting for the API. Success and failure
// both compare that revision before publishing candidates, so later typing or selection
// wins regardless of response order. Current successes sort active users before deleted
// users; current failures clear the suggestion list.
const debounceUserSearch = () => {
  clearPendingUserSearch()
  const query = userKeyword.value.trim()
  if (!query) {
    userResults.value = []
    return
  }

  const sequence = userSearchSequence
  userSearchTimeout = setTimeout(async () => {
    userSearchTimeout = null
    try {
      const results = await adminAPI.usage.searchUsers(query)
      // Publish only for the captured user-search revision; later typing, clearing, or
      // selection invalidates that response even when it succeeds.
      if (sequence === userSearchSequence) {
        userResults.value = results.sort((a, b) => Number(a.deleted) - Number(b.deleted))
      }
    } catch {
      // Clear suggestions only for the current search revision so an older rejected
      // request cannot erase results belonging to later input.
      if (sequence === userSearchSequence) {
        userResults.value = []
      }
    }
  }, SEARCH_DEBOUNCE_MS)
}

// debounceApiKeySearch replaces the pending timer and searches keys using the selected user
// and current text. Failures clear suggestions; already running searches are not canceled
// or checked for staleness.
const debounceApiKeySearch = () => {
  // Replacing the debounce timer coalesces rapid input; an API-key request that has
  // already started still completes independently.
  if (apiKeySearchTimeout) clearTimeout(apiKeySearchTimeout)
  apiKeySearchTimeout = setTimeout(async () => {
    try {
      apiKeyResults.value = await adminAPI.usage.searchApiKeys(
        filters.value.user_id,
        apiKeyKeyword.value || '',
      )
    } catch {
      // Treat key lookup failure as no suggestions so the selected user predicate remains
      // usable even when key discovery fails.
      apiKeyResults.value = []
    }
  }, SEARCH_DEBOUNCE_MS)
}

// selectUser records the selected identity and email, invalidates user searches, and clears
// the key selection. It then loads that user's available keys before emitting the shared
// filter change.
const selectUser = async (u: SimpleUser) => {
  clearPendingUserSearch()
  userKeyword.value = u.email
  showUserDropdown.value = false
  filters.value.user_id = u.id
  clearApiKey()

  // Auto-load API keys for this user
  try {
    apiKeyResults.value = await adminAPI.usage.searchApiKeys(u.id, '')
  } catch {
    // Keep the newly selected user and publish its filter change even when loading that
    // user's key suggestions fails.
    apiKeyResults.value = []
  }

  emitChange()
}

// clearUser removes the selected user and dependent key while invalidating pending user
// results. It clears both autocomplete presentations before emitting change so the parent
// reload sees the cleared selection.
const clearUser = () => {
  clearPendingUserSearch()
  userKeyword.value = ''
  userResults.value = []
  showUserDropdown.value = false
  filters.value.user_id = undefined
  clearApiKey()
  emitChange()
}

// selectApiKey records a key identity and closes suggestions before emitting change. Keys
// without a name display their numeric identifier so the selected key remains identifiable
// in the input.
const selectApiKey = (k: SimpleApiKey) => {
  apiKeyKeyword.value = k.name || String(k.id)
  showApiKeyDropdown.value = false
  filters.value.api_key_id = k.id
  emitChange()
}

// clearApiKey clears key text, candidates, visibility, and the shared key predicate. It
// does not emit change itself because callers can combine key clearing with a user
// selection update.
const clearApiKey = () => {
  apiKeyKeyword.value = ''
  apiKeyResults.value = []
  showApiKeyDropdown.value = false
  filters.value.api_key_id = undefined
}

// onClearApiKey clears the selected key and notifies the query-owning parent.
const onClearApiKey = () => {
  clearApiKey()
  emitChange()
}

// resetPanelFilters clears every shared predicate except the beginning and ending dates. It
// invalidates user search work first and emits one change after the shared object has been
// updated.
const resetPanelFilters = () => {
  clearPendingUserSearch()
  for (const key of Object.keys(filters.value)) {
    if (key !== 'start_date' && key !== 'end_date') filters.value[key] = undefined
  }
  emitChange()
}

// debounceProviderSearch replaces the pending provider timer and retrieves up to twenty
// matching providers. Empty text or retrieval failure clears candidates; completed requests
// do not compare a response revision.
const debounceProviderSearch = () => {
  // Replacing the debounce timer coalesces provider input; already running provider
  // requests retain their existing completion behavior.
  if (providerSearchTimeout) clearTimeout(providerSearchTimeout)
  providerSearchTimeout = setTimeout(async () => {
    if (!providerKeyword.value) {
      providerResults.value = []
      return
    }
    try {
      const res = await adminAPI.providers.list(1, 20, { search: providerKeyword.value })
      providerResults.value = res.items.map((a) => ({ id: a.id, name: a.name }))
    } catch {
      // Treat provider lookup failure as an empty suggestion list while keeping the typed
      // provider text available for another search.
      providerResults.value = []
    }
  }, SEARCH_DEBOUNCE_MS)
}

// selectProvider records provider identity and name before closing suggestions and
// notifying the parent.
const selectProvider = (a: SimpleProvider) => {
  providerKeyword.value = a.name
  showProviderDropdown.value = false
  filters.value.provider_id = a.id
  emitChange()
}

// clearProvider removes provider text, candidates, and the shared predicate before
// notifying the parent.
const clearProvider = () => {
  providerKeyword.value = ''
  providerResults.value = []
  showProviderDropdown.value = false
  filters.value.provider_id = undefined
  emitChange()
}

// onApiKeyFocus opens key suggestions and schedules a search when no candidates exist.
// Existing candidates remain available immediately, so focusing an already populated input
// does not issue another request.
const onApiKeyFocus = () => {
  showApiKeyDropdown.value = true
  // Trigger search if no results yet
  if (apiKeyResults.value.length === 0) {
    debounceApiKeySearch()
  }
}

// onDocumentClick closes search dropdowns whose containers do not contain the clicked node.
// Clicks inside a portaled Select dropdown are ignored so choosing a filter does not
// dismiss autocomplete state.
const onDocumentClick = (e: MouseEvent) => {
  const target = e.target as Node | null
  if (!target) return
  if (target instanceof Element && target.closest('.select-dropdown-portal')) return

  const clickedInsideUser = userSearchRef.value?.contains(target) ?? false
  const clickedInsideApiKey = apiKeySearchRef.value?.contains(target) ?? false
  const clickedInsideProvider = providerSearchRef.value?.contains(target) ?? false

  if (!clickedInsideUser) showUserDropdown.value = false
  if (!clickedInsideApiKey) showApiKeyDropdown.value = false
  if (!clickedInsideProvider) showProviderDropdown.value = false
}

watch(
  () => props.startDate,
  (value) => {
    filters.value.start_date = value
  },
  { immediate: true },
)

watch(
  () => props.endDate,
  (value) => {
    filters.value.end_date = value
  },
  { immediate: true },
)

watch(
  () => filters.value.user_id,
  (userId) => {
    if (!userId) {
      clearPendingUserSearch()
      userKeyword.value = ''
      userResults.value = []
    }
  },
)

watch(
  () => filters.value.api_key_id,
  (apiKeyId) => {
    if (!apiKeyId) {
      apiKeyKeyword.value = ''
      apiKeyResults.value = []
    }
  },
)

watch(
  () => filters.value.provider_id,
  (providerId) => {
    if (!providerId) {
      providerKeyword.value = ''
      providerResults.value = []
    }
  },
)

onMounted(async () => {
  // The document listener sees clicks beyond the component containers, including portaled
  // controls that onDocumentClick exempts from dismissal.
  document.addEventListener('click', onDocumentClick)
  // Team and group choices load independently so a failure in either source leaves the
  // other filters usable.
  await Promise.allSettled([
    adminAPI.groups.list(1, 1000).then((gs) => {
      groupOptions.value.push(...gs.items.map((g: any) => ({ value: g.id, label: g.name })))
    }),
    adminAPI.teams.list().then((teams) => {
      teamOptions.value.push(...teams.map((team) => ({ value: team.id, label: team.name })))
    }),
  ])
})

// Unmount invalidates user search results and removes the document listener; API-key and
// provider timer lifetimes remain independent here.
onUnmounted(() => {
  clearPendingUserSearch()
  document.removeEventListener('click', onDocumentClick)
})

// setUserKeyword fills a programmatically selected user label and closes its suggestions.
// It invalidates pending user searches first so route hydration or ranking selection cannot
// immediately receive stale candidates.
const setUserKeyword = (email: string) => {
  clearPendingUserSearch()
  userKeyword.value = email
  userResults.value = []
  showUserDropdown.value = false
}

// getUserSearchRevision returns the current user-search revision for route label
// validation.
const getUserSearchRevision = () => userSearchSequence

defineExpose({ getUserSearchRevision, setUserKeyword })
</script>
