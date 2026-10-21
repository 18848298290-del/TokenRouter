import { ref, computed, toRaw, type Ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { useTableSelection } from '@/composables/useTableSelection'
import { fetchAllProviderIds } from '@/utils/providerSelection'
import type { Provider, ProviderPlatform, ProviderType } from '@/api/admin/providers.types'

import type { ProviderListParams, ProviderSortOrder } from './useProviderList'

// useProviderSelection.ts tracks checked provider IDs and snapshots the filters
// used for cross-page actions. Visible-row selection delegates to the shared
// table-selection composable. Selecting every matching provider queries IDs
// across all pages and retains the completed ID set so the page can distinguish
// a complete result selection from individually checked rows.
//
// Clearing selection advances a request generation before clearing IDs. A
// pending all-results request compares that generation before publishing or
// reporting failure, so changing filters cannot restore a discarded selection.
// Bulk-edit metadata uses its captured filters to collect platform and
// credential-type choices for the target preview.
// ProviderBulkEditTarget captures either checked provider IDs or a filtered
// result set for the bulk editor. Each branch carries its own platform and
// credential-type metadata, with filters and selected IDs copied before the
// dialog opens.
export type ProviderBulkEditTarget =
  | {
      mode: 'selected' // Target selection mode.
      providerIds: number[] // Copied provider IDs.
      selectedPlatforms: ProviderPlatform[] // Target provider platforms.
      selectedTypes: ProviderType[] // Target credential types.
    }
  | {
      mode: 'filtered' // Target selection mode.
      // filters holds the copied request fields so later table edits cannot change this bulk-edit target.
      filters: {
        platform?: string // Selected platform filter.
        type?: string // Selected credential filter.
        status?: string // Selected status filter.
        group?: string // Selected group filter.
        search?: string // Captured name search.
        privacy_mode?: string // Selected privacy filter.
        sort_by?: string // Captured sorting column.
        sort_order?: ProviderSortOrder // Captured sorting direction.
      }
      previewCount: number // Reported matching count.
      selectedPlatforms: ProviderPlatform[] // Matching provider platforms.
      selectedTypes: ProviderType[] // Matching credential types.
    }

// ProviderBulkEditFilterSnapshot contains the copied filters used for filtered
// bulk editing and all-results selection.
export type ProviderBulkEditFilterSnapshot = Extract<
  ProviderBulkEditTarget,
  { mode: 'filtered' }
>['filters']
// useProviderSelection binds checked IDs to the live provider rows and filter
// state. It exposes ordinary row operations, all-results selection, and
// bulk-edit metadata lookup for the page. The pagination total participates in
// deciding whether an all-results snapshot still describes the current result
// set.
//
// Clearing selection advances a generation before clearing the IDs. All-results
// requests retain their starting generation and publish only while it matches,
// preventing a delayed response from restoring a selection that the
// administrator already discarded.
export function useProviderSelection(
  providers: Ref<Provider[]>,
  pagination: Pick<
    ReturnType<typeof import('./useProviderList').useProviderList>['pagination'],
    'total'
  >,
  params: ProviderListParams,
) {
  // The translator and application store supply messages for this controller
  // while its request and selection state belongs to this mounted page.
  const { t } = useI18n()
  const appStore = useAppStore()
  // BULK_EDIT_FILTER_METADATA_PAGE_SIZE bounds each lightweight metadata request
  // while collecting every platform and credential type in a filtered target.
  const BULK_EDIT_FILTER_METADATA_PAGE_SIZE = 500

  // selPlatforms and selTypes describe selected providers visible on the current
  // page and supply defaults for selected-ID bulk edits.
  const selPlatforms = computed<ProviderPlatform[]>(() => {
    const platforms = new Set(
      providers.value.filter((a) => isSelected(a.id)).map((a) => a.platform),
    )
    return [...platforms]
  })
  const selTypes = computed<ProviderType[]>(() => {
    const types = new Set(providers.value.filter((a) => isSelected(a.id)).map((a) => a.type))
    return [...types]
  })

  // The shared selection controller owns checked IDs and row operations; the
  // all-results snapshot below tracks whether those IDs cover the entire filtered
  // result.
  const {
    selectedSet,
    selectedIds: selIds,
    allVisibleSelected,
    isSelected,
    setSelectedIds,
    select,
    deselect,
    toggle: toggleSel,
    clear: clearSelectedIds,
    removeMany: removeSelectedProviders,
    toggleVisible,
    selectVisible: selectCurrentPage,
    batchUpdate,
  } = useTableSelection<Provider>({
    rows: providers,
    getId: (provider) => provider.id,
  })

  // selectingAllResults, selectedAllResultIDs, and selectionRequestVersion
  // distinguish a pending request, its completed ID snapshot, and the generation
  // permitted to publish that snapshot.
  const selectingAllResults = ref(false)
  const selectedAllResultIDs = ref<Set<number> | null>(null)
  const selectionRequestVersion = ref(0)

  // allResultsSelected compares snapshot IDs with both the live total and checked
  // IDs, so manual changes clear the complete-selection indicator.
  const allResultsSelected = computed(() => {
    const snapshot = selectedAllResultIDs.value
    if (
      !snapshot ||
      snapshot.size === 0 ||
      snapshot.size !== pagination.total ||
      snapshot.size !== selectedSet.value.size
    ) {
      return false
    }
    return Array.from(snapshot).every((id) => selectedSet.value.has(id))
  })

  // clearSelection discards checked IDs and the completed all-results snapshot
  // when the page clears selection or changes its filters. It advances the
  // request generation so pending all-results success and failure handlers lose
  // permission to publish, including their final loading-state cleanup.
  //
  // The loading flag is released immediately so a subsequent selection can start
  // while the earlier request is still running. That request continues fetching,
  // but its completion cannot restore the discarded IDs or hide the newer request.
  const clearSelection = () => {
    // Revoke pending request ownership before clearing IDs so delayed completion
    // cannot repopulate the selection the administrator just discarded.
    selectionRequestVersion.value++
    selectingAllResults.value = false
    selectedAllResultIDs.value = null
    clearSelectedIds()
  }

  // selectPage adds every currently visible provider to the checked ID set.
  const selectPage = () => {
    selectCurrentPage()
  }

  // buildBulkEditFilterSnapshot copies the current table filters into a stable
  // bulk-action target. Non-string fields become empty strings and sort order
  // defaults to ascending, so later filter edits cannot mutate the captured
  // request.
  const buildBulkEditFilterSnapshot = (): ProviderBulkEditFilterSnapshot => {
    const rawParams = toRaw(params) as Record<string, unknown>
    const sortOrder: ProviderSortOrder = rawParams.sort_order === 'desc' ? 'desc' : 'asc'
    return {
      platform: typeof rawParams.platform === 'string' ? rawParams.platform : '',
      type: typeof rawParams.type === 'string' ? rawParams.type : '',
      status: typeof rawParams.status === 'string' ? rawParams.status : '',
      group: typeof rawParams.group === 'string' ? rawParams.group : '',
      search: typeof rawParams.search === 'string' ? rawParams.search : '',
      privacy_mode: typeof rawParams.privacy_mode === 'string' ? rawParams.privacy_mode : '',
      sort_by: typeof rawParams.sort_by === 'string' ? rawParams.sort_by : '',
      sort_order: sortOrder,
    }
  }

  // handleSelectAllResults selects provider IDs across every page matching the
  // captured filters. It rejects duplicate starts and empty result sets, then
  // records a request generation before fetching all IDs. Success publishes the
  // checked IDs and completed snapshot only while that generation remains
  // current.
  //
  // Clearing selection advances the generation, which also suppresses stale error
  // messages and loading cleanup. The final snapshot is compared with live
  // pagination and checked IDs so removing one selection immediately disables the
  // all-results indicator.
  const handleSelectAllResults = async () => {
    if (selectingAllResults.value || pagination.total === 0) return

    const requestVersion = ++selectionRequestVersion.value
    const filters = buildBulkEditFilterSnapshot()
    selectingAllResults.value = true
    try {
      const ids = await fetchAllProviderIds(
        (page, pageSize, requestFilters) => adminAPI.providers.list(page, pageSize, requestFilters),
        filters,
      )
      if (requestVersion !== selectionRequestVersion.value) return

      setSelectedIds(ids)
      selectedAllResultIDs.value = new Set(ids)
    } catch (error) {
      if (requestVersion !== selectionRequestVersion.value) return
      console.error('Failed to select all provider results:', error)
      appStore.showError(t('admin.providers.bulkActions.selectAllFailed'))
    } finally {
      if (requestVersion === selectionRequestVersion.value) {
        selectingAllResults.value = false
      }
    }
  }

  // collectSelectionMetadata returns distinct platform and credential types from
  // provider rows.
  const collectSelectionMetadata = (rows: Provider[]) => {
    const selectedPlatforms = Array.from(new Set(rows.map((provider) => provider.platform)))
    const selectedTypes = Array.from(new Set(rows.map((provider) => provider.type)))
    return { selectedPlatforms, selectedTypes }
  }

  // loadBulkEditFilterMetadata collects the platform and credential types for a
  // filtered bulk-edit target. It reads lightweight pages until the reported
  // total is reached or an empty page ends traversal, returning the latest
  // reported preview count.
  const loadBulkEditFilterMetadata = async (filters: ProviderBulkEditFilterSnapshot) => {
    const rows: Provider[] = []
    let page = 1
    while (true) {
      const response = await adminAPI.providers.list(page, BULK_EDIT_FILTER_METADATA_PAGE_SIZE, {
        ...filters,
        lite: '1',
      })
      const total = response.total
      rows.push(...response.items)

      if (rows.length >= total || response.items.length === 0) {
        return {
          previewCount: total, // Reported matching count.
          ...collectSelectionMetadata(rows),
        }
      }
      page += 1
    }
  }
  return {
    selIds,
    allVisibleSelected,
    isSelected,
    setSelectedIds,
    select,
    deselect,
    toggleSel,
    removeSelectedProviders,
    toggleVisible,
    batchUpdate,
    selectingAllResults,
    allResultsSelected,
    clearSelection,
    selectPage,
    selPlatforms,
    selTypes,
    buildBulkEditFilterSnapshot,
    handleSelectAllResults,
    loadBulkEditFilterMetadata,
  }
}
