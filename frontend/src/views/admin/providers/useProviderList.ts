import { ref, reactive, toRaw, watch } from 'vue'
import { useIntervalFn } from '@vueuse/core'
import { adminAPI } from '@/api/admin'
import { useTableLoader } from '@/composables/useTableLoader'
import { supportsUpstreamUsageQuery } from '@/utils/upstreamUsage'
import { buildGrokUsageRefreshKey, buildOpenAIUsageRefreshKey } from '@/utils/providerUsageRefresh'
import type { Provider } from '@/api/admin/providers.types'

// useProviderList.ts loads provider table rows and schedules incremental
// refreshes with ETags. Initial loading requests a lightweight response, and
// later requests use the current filters, pagination, sorting, and
// scheduler-column visibility. The page supplies callbacks for usage hydration,
// selection clearing, and references held by open dialogs.
//
// Incremental refresh compares persisted and runtime fields before replacing a
// row, retaining unchanged row objects so their usage cells keep their state.
// Missing or changed rows notify the page before the resulting array is
// published. Refreshes pause during interactions and after local edits, while
// startup remains controlled by the page after its auxiliary metadata has
// loaded.

// ProviderSortOrder identifies the ascending or descending order requested for
// the table.
export type ProviderSortOrder = 'asc' | 'desc'
// ProviderAPIFilters derives the accepted filter fields from the administrator
// list endpoint.
type ProviderAPIFilters = NonNullable<Parameters<typeof adminAPI.providers.list>[2]>
// ProviderListParams keeps initialized table filters required while the
// first-load lite flag remains optional.
export type ProviderListParams = Required<Omit<ProviderAPIFilters, 'lite'>> &
  Pick<ProviderAPIFilters, 'lite'>
// ProviderListOptions connects list refreshes to the page-owned selection,
// usage, and dialog state.
interface ProviderListOptions {
  // includeSchedulerScore reads current column visibility immediately before a
  // list request. Return true when the page needs scheduler details so all
  // loading paths request the same fields.
  includeSchedulerScore: () => boolean
  // clearSelection invalidates selected IDs before a debounced filter reload is
  // scheduled. The callback also supersedes pending cross-page selection work
  // owned by the selection controller.
  clearSelection: () => void
  // onLoaded restores visible usage observations and awaits daily statistics
  // after a completed load. It runs for both ordinary loads and successful
  // incremental responses, including not-modified results.
  onLoaded: () => Promise<void>
  // refreshTodayStats updates daily metrics after a loader-driven pagination or
  // filter request finishes. This callback does not hydrate upstream cache state
  // because those deferred paths retain their existing behavior.
  refreshTodayStats: () => Promise<void>
  // onRowMissing invalidates observations for an upstream-capable row missing
  // from the next server page. It runs before reconciliation publishes the
  // resulting rows and may clear provider cache state.
  onRowMissing: (provider: Provider) => void
  // onRowReplaced receives the current and replacement row before publication. It
  // invalidates changed cache identities and updates references held by page
  // dialogs while both versions remain available.
  onRowReplaced: (current: Provider, next: Provider) => void
  // isRefreshPaused reports whether dialogs or menus currently prevent interval
  // refresh. The controller checks this after loading admission and before
  // decrementing its countdown or requesting another page.
  isRefreshPaused: () => boolean
}

// useProviderList owns provider rows, loading parameters, pagination, and
// incremental refresh scheduling. The page supplies callbacks for selection and
// usage state plus references held by dialogs. Call resumeAutoRefresh after the
// page's auxiliary data has settled; the interval starts paused.
//
// Ordinary loads reset ETag state, while incremental refreshes retain unchanged
// row objects and notify callers before replacing changed rows. Callback
// execution follows the row-reconciliation order, and refresh admission checks
// page interaction state together with loading and the post-edit silent window.
export function useProviderList(options: ProviderListOptions) {
  // PROVIDER_SORT_STORAGE_KEY identifies the shared browser setting read here and
  // written by the table sorting control.
  const PROVIDER_SORT_STORAGE_KEY = 'provider-table-sort'

  // ProviderSortState pairs the active table column key with its requested
  // direction.
  type ProviderSortState = {
    sort_by: string // Active column key.
    sort_order: ProviderSortOrder // Requested sort direction.
  }

  // PROVIDER_SORTABLE_KEYS accepts persisted sorting choices that the server and
  // table currently expose to administrators.
  const PROVIDER_SORTABLE_KEYS = new Set([
    'id',
    'name',
    'status',
    'schedulable',
    'priority',
    'rate_multiplier',
    'last_used_at',
    'created_at',
    'expires_at',
  ])

  // loadInitialProviderSortState restores a supported sort key from browser
  // storage. Missing, malformed, or unsupported saved values use name ascending;
  // recognized keys retain descending order only when the stored order is desc.
  const loadInitialProviderSortState = (): ProviderSortState => {
    const fallback: ProviderSortState = { sort_by: 'name', sort_order: 'asc' }
    try {
      const raw = localStorage.getItem(PROVIDER_SORT_STORAGE_KEY)
      if (!raw) return fallback
      const parsed = JSON.parse(raw) as {
        key?: string // Persisted sorting column.
        order?: string // Persisted sorting direction.
      }
      const key = typeof parsed.key === 'string' ? parsed.key : ''
      if (!PROVIDER_SORTABLE_KEYS.has(key)) return fallback
      return {
        sort_by: key,
        sort_order: parsed.order === 'desc' ? 'desc' : 'asc',
      }
    } catch {
      // Unreadable browser preferences use the initialized default sorting choice
      // so table loading can proceed even when storage is unavailable.
      return fallback
    }
  }

  // sortState supplies both visible sort controls and list request parameters
  // from the restored browser preference.
  const sortState = reactive<ProviderSortState>(loadInitialProviderSortState())

  // AUTO_REFRESH_STORAGE_KEY and autoRefreshIntervals define the persisted
  // preference namespace and supported durations shown by the refresh control.
  const AUTO_REFRESH_STORAGE_KEY = 'provider-auto-refresh'
  const autoRefreshIntervals = [5, 10, 15, 30] as const

  // autoRefreshEnabled, autoRefreshIntervalSeconds, and autoRefreshCountdown
  // coordinate the saved preference with the active interval and its visible
  // countdown.
  const autoRefreshEnabled = ref(false)
  const autoRefreshIntervalSeconds = ref<(typeof autoRefreshIntervals)[number]>(30)
  const autoRefreshCountdown = ref(0)

  // autoRefreshETag belongs to the current list query, while autoRefreshFetching
  // prevents overlapping incremental requests during one refresh cycle.
  const autoRefreshETag = ref<string | null>(null)
  const autoRefreshFetching = ref(false)

  // AUTO_REFRESH_SILENT_WINDOW_MS and autoRefreshSilentUntil delay polling after
  // local edits so recently updated rows remain visible before the next
  // reconciliation.
  const AUTO_REFRESH_SILENT_WINDOW_MS = 15000
  const autoRefreshSilentUntil = ref(0)

  // hasPendingListSync marks local removals awaiting a full reload;
  // pendingTodayStatsRefresh schedules statistics after loader-driven pagination
  // or filter changes.
  const hasPendingListSync = ref(false)
  const pendingTodayStatsRefresh = ref(false)

  // loadSavedAutoRefresh restores the enabled flag and a supported interval from
  // browser storage. Invalid or unavailable storage leaves the initialized
  // defaults, while unsupported interval values leave the default interval in
  // place.
  const loadSavedAutoRefresh = () => {
    try {
      const saved = localStorage.getItem(AUTO_REFRESH_STORAGE_KEY)
      if (!saved) return
      const parsed = JSON.parse(saved) as {
        enabled?: boolean // Saved polling preference.
        interval_seconds?: number // Polling interval seconds.
      }
      autoRefreshEnabled.value = parsed.enabled === true
      const interval = Number(parsed.interval_seconds)
      if (autoRefreshIntervals.includes(interval as (typeof autoRefreshIntervals)[number])) {
        autoRefreshIntervalSeconds.value = interval as (typeof autoRefreshIntervals)[number]
      }
    } catch (e) {
      // Saved refresh preferences are optional; retain the initialized timer
      // settings when parsing or browser-storage access fails during setup.
      console.error('Failed to load saved auto refresh settings:', e)
    }
  }

  // saveAutoRefreshToStorage persists the current enabled flag and interval in
  // browser storage. Storage errors are logged while the active page keeps its
  // in-memory refresh settings and scheduling behavior.
  const saveAutoRefreshToStorage = () => {
    try {
      localStorage.setItem(
        AUTO_REFRESH_STORAGE_KEY,
        JSON.stringify({
          enabled: autoRefreshEnabled.value,
          interval_seconds: autoRefreshIntervalSeconds.value,
        }),
      )
    } catch (e) {
      // The active timer already follows the in-memory preference, so storage
      // failure affects persistence across visits rather than this interval.
      console.error('Failed to save auto refresh settings:', e)
    }
  }

  // setAutoRefreshEnabled stores the requested refresh preference and starts or
  // pauses the interval. Enabling resets the countdown to the selected duration;
  // disabling pauses the timer and clears the visible countdown.
  const setAutoRefreshEnabled = (enabled: boolean) => {
    autoRefreshEnabled.value = enabled
    saveAutoRefreshToStorage()
    if (enabled) {
      autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
      resumeAutoRefresh()
    } else {
      pauseAutoRefresh()
      autoRefreshCountdown.value = 0
    }
  }

  // setAutoRefreshInterval stores one supported refresh duration for subsequent
  // ticks. An enabled timer restarts its countdown from that duration; a disabled
  // timer retains its current countdown until enabled.
  const setAutoRefreshInterval = (seconds: (typeof autoRefreshIntervals)[number]) => {
    autoRefreshIntervalSeconds.value = seconds
    saveAutoRefreshToStorage()
    if (autoRefreshEnabled.value) {
      autoRefreshCountdown.value = seconds
    }
  }

  // syncProviderListDerivedParams updates scheduler-score inclusion from current
  // column visibility. Every list-loading path calls it before requesting rows so
  // the server computes scores only when the page needs that column.
  const syncProviderListDerivedParams = () => {
    // Scheduler scores depend on current visibility, including when filters and sorting reuse the same request-parameter object.
    const requestParams = params
    requestParams.include_scheduler_score = options.includeSchedulerScore() ? '1' : '0'
  }

  // The shared loader owns ordinary request cancellation and pagination while
  // these wrappers add provider-specific ETag and usage refresh sequencing.
  const {
    items: providers,
    loading,
    params,
    pagination,
    load: baseLoad,
    reload: baseReload,
    debouncedReload: baseDebouncedReload,
    handlePageChange: baseHandlePageChange,
    handlePageSizeChange: baseHandlePageSizeChange,
  } = useTableLoader<Provider, ProviderListParams>({
    fetchFn: adminAPI.providers.list,
    initialParams: {
      platform: '',
      type: '',
      status: '',
      privacy_mode: '',
      group: '',
      search: '',
      include_scheduler_score: options.includeSchedulerScore() ? '1' : '0',
      sort_by: sortState.sort_by,
      sort_order: sortState.sort_order,
    },
  })

  // resetAutoRefreshCache discards the validator retained by incremental list
  // refreshes. Call it when starting a full load or changing the page query so
  // the next incremental request sends no cached ETag.
  const resetAutoRefreshCache = () => {
    // Query changes and full reloads invalidate the previous validator; clearing
    // it prevents the next incremental request from validating against stale rows.
    autoRefreshETag.value = null
  }

  // isFirstLoad controls lightweight initial rows and removes that request flag
  // after the first load completes.
  const isFirstLoad = ref(true)

  // load fetches the current provider page and refreshes its usage observations.
  // It clears pending synchronization and ETag state before the request. The
  // first load sets lite on the shared parameters, then removes that parameter
  // after the initial request completes.
  //
  // Successful completion delegates usage hydration and daily statistics to the
  // page callback. The first-load flag is evaluated again after awaiting the
  // loader, retaining the existing interaction with overlapping requests. Loader
  // failures propagate before the completion callback runs.
  const load = async () => {
    const requestParams = params
    syncProviderListDerivedParams()
    hasPendingListSync.value = false
    resetAutoRefreshCache()
    pendingTodayStatsRefresh.value = false
    if (isFirstLoad.value) {
      requestParams.lite = '1'
    }
    await baseLoad()
    if (isFirstLoad.value) {
      isFirstLoad.value = false
      delete requestParams.lite
    }
    await options.onLoaded()
  }

  // reload resets pagination through the shared loader and refreshes visible
  // usage after completion. It clears pending list synchronization, cached ETag,
  // and deferred-statistics state before starting the request.
  const reload = async () => {
    syncProviderListDerivedParams()
    hasPendingListSync.value = false
    resetAutoRefreshCache()
    pendingTodayStatsRefresh.value = false
    await baseReload()
    await options.onLoaded()
  }

  // debouncedReload clears selection and schedules a filtered list reload through
  // the shared debounce. It resets ETag and pending synchronization immediately,
  // then marks daily statistics for refresh when loading finishes.
  const debouncedReload = () => {
    options.clearSelection()
    syncProviderListDerivedParams()
    hasPendingListSync.value = false
    resetAutoRefreshCache()
    // The shared debounce performs the load later; the loading watcher supplies
    // statistics when that deferred request completes.
    pendingTodayStatsRefresh.value = true
    baseDebouncedReload()
  }

  // handlePageChange requests a page through the shared loader's pagination
  // clamp. It updates derived query parameters, clears ETag and synchronization
  // state, and schedules daily statistics after the page load finishes.
  const handlePageChange = (page: number) => {
    syncProviderListDerivedParams()
    hasPendingListSync.value = false
    resetAutoRefreshCache()
    pendingTodayStatsRefresh.value = true
    baseHandlePageChange(page)
  }

  // handlePageSizeChange delegates page-size persistence and reloading to the
  // shared loader. It refreshes derived query parameters and resets ETag state
  // before marking daily statistics for the resulting page.
  const handlePageSizeChange = (size: number) => {
    syncProviderListDerivedParams()
    hasPendingListSync.value = false
    resetAutoRefreshCache()
    pendingTodayStatsRefresh.value = true
    baseHandlePageSizeChange(size)
  }

  // handleSort updates the selected sort key and order before loading the first
  // page. It synchronizes request parameters, resets ETag and pending
  // synchronization, and marks statistics for refresh with the sorted rows.
  const handleSort = (key: string, order: ProviderSortOrder) => {
    sortState.sort_by = key
    sortState.sort_order = order
    const requestParams = params
    requestParams.sort_by = key
    requestParams.sort_order = order
    syncProviderListDerivedParams()
    pagination.page = 1
    hasPendingListSync.value = false
    resetAutoRefreshCache()
    pendingTodayStatsRefresh.value = true
    load()
  }

  // Deferred loader operations do not await the page wrapper, so their loading
  // transition triggers the daily-statistics completion work here.
  watch(loading, (isLoading, wasLoading) => {
    if (wasLoading && !isLoading && pendingTodayStatsRefresh.value) {
      pendingTodayStatsRefresh.value = false
      options.refreshTodayStats().catch((error) => {
        console.error('Failed to refresh provider today stats after table load:', error)
      })
    }
  })

  // enterAutoRefreshSilentWindow delays automatic refresh after a local provider
  // edit. It samples the current time, extends the silent deadline, and resets
  // the visible countdown to the configured interval.
  const enterAutoRefreshSilentWindow = () => {
    autoRefreshSilentUntil.value = Date.now() + AUTO_REFRESH_SILENT_WINDOW_MS
    autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
  }

  // inAutoRefreshSilentWindow reports whether the post-edit refresh deadline is
  // still ahead.
  const inAutoRefreshSilentWindow = () => {
    return Date.now() < autoRefreshSilentUntil.value
  }

  // shouldReplaceAutoRefreshRow compares persisted configuration and runtime
  // observations that affect provider cells. It checks timestamps, scheduling
  // state, concurrency, cost, session counts, temporary restrictions, proxy
  // revision, and platform-specific usage-refresh identities. Upstream-capable
  // rows additionally compare connection settings and query configuration.
  //
  // A false result lets reconciliation retain the current row object and its
  // mounted usage-cell state. Array order is checked separately by the caller, so
  // an unchanged record can still move to its new server position without being
  // replaced.
  const shouldReplaceAutoRefreshRow = (current: Provider, next: Provider) => {
    const upstreamConnectionChanged =
      supportsUpstreamUsageQuery(current) || supportsUpstreamUsageQuery(next)
        ? current.type !== next.type ||
          current.platform !== next.platform ||
          current.proxy_id !== next.proxy_id ||
          current.proxy?.updated_at !== next.proxy?.updated_at ||
          current.credentials?.base_url !== next.credentials?.base_url ||
          JSON.stringify(current.extra?.upstream_usage_query ?? null) !==
            JSON.stringify(next.extra?.upstream_usage_query ?? null)
        : false
    return (
      current.updated_at !== next.updated_at ||
      current.current_concurrency !== next.current_concurrency ||
      current.current_window_cost !== next.current_window_cost ||
      current.active_sessions !== next.active_sessions ||
      current.schedulable !== next.schedulable ||
      current.status !== next.status ||
      current.rate_limit_reset_at !== next.rate_limit_reset_at ||
      current.overload_until !== next.overload_until ||
      current.temp_unschedulable_until !== next.temp_unschedulable_until ||
      current.proxy?.updated_at !== next.proxy?.updated_at ||
      upstreamConnectionChanged ||
      buildOpenAIUsageRefreshKey(current) !== buildOpenAIUsageRefreshKey(next) ||
      buildGrokUsageRefreshKey(current) !== buildGrokUsageRefreshKey(next)
    )
  }

  // mergeProvidersIncrementally reconciles a server page with the currently
  // displayed rows by provider ID. Missing upstream-capable rows notify the
  // caller first. New rows enter the result directly; changed rows notify the
  // caller before replacement, and unchanged rows retain their current object
  // identity.
  //
  // The list reference changes when the row count, a compared record, or ID order
  // differs. Reusing the existing array when all three remain stable avoids
  // unnecessary table updates, while callbacks keep cache invalidation and dialog
  // references synchronized with replacements.
  const mergeProvidersIncrementally = (nextRows: Provider[]) => {
    const currentRows = providers.value
    const currentByID = new Map(currentRows.map((row) => [row.id, row]))
    const nextIDs = new Set(nextRows.map((row) => row.id))
    for (const currentRow of currentRows) {
      if (supportsUpstreamUsageQuery(currentRow) && !nextIDs.has(currentRow.id)) {
        options.onRowMissing(currentRow)
      }
    }
    let changed = nextRows.length !== currentRows.length
    const mergedRows = nextRows.map((nextRow) => {
      const currentRow = currentByID.get(nextRow.id)
      if (!currentRow) {
        changed = true
        return nextRow
      }
      if (shouldReplaceAutoRefreshRow(currentRow, nextRow)) {
        // Notify the page before publishing the replacement array so cached query
        // observations and open-dialog references follow the accepted row
        // revision.
        options.onRowReplaced(currentRow, nextRow)
        changed = true
        return nextRow
      }
      return currentRow
    })
    if (!changed) {
      for (let i = 0; i < mergedRows.length; i += 1) {
        if (mergedRows[i].id !== currentRows[i]?.id) {
          changed = true
          break
        }
      }
    }
    // Publish a replacement array only after membership, compared values, or
    // order changes; unchanged rows keep their existing mounted-cell identity.
    if (changed) {
      providers.value = mergedRows
    }
  }

  // refreshProvidersIncrementally fetches the current page with its saved ETag
  // when no incremental request is active. A returned ETag replaces the previous
  // value. Modified responses update pagination and reconcile rows, while
  // not-modified responses retain the existing rows and still refresh visible
  // usage observations.
  //
  // Errors are logged and the active-request flag is released in finally. The
  // request uses the filters at invocation time; callers control interval
  // admission and this function does not add a separate request-generation check
  // for later filter changes.
  const refreshProvidersIncrementally = async () => {
    if (autoRefreshFetching.value) return
    syncProviderListDerivedParams()
    autoRefreshFetching.value = true
    try {
      const result = await adminAPI.providers.listWithEtag(
        pagination.page,
        pagination.page_size,
        toRaw(params) as {
          platform?: string // Selected platform filter.
          type?: string // Selected credential filter.
          status?: string // Selected status filter.
          privacy_mode?: string // Selected privacy filter.
          group?: string // Selected group filter.
          search?: string // Current name search.
          sort_by?: string // Requested sorting column.
          sort_order?: ProviderSortOrder // Requested sorting direction.
        },
        { etag: autoRefreshETag.value },
      )

      if (result.etag) {
        autoRefreshETag.value = result.etag
      }
      if (!result.notModified && result.data) {
        pagination.total = result.data.total || 0
        pagination.pages = result.data.pages || 0
        mergeProvidersIncrementally(result.data.items || [])
        hasPendingListSync.value = false
      }

      // Hydrate after row reconciliation so cache identities use the returned provider revisions before daily statistics are requested.
      await options.onLoaded()
    } catch (error) {
      console.error('Auto refresh failed:', error)
    } finally {
      autoRefreshFetching.value = false
    }
  }

  // The interval starts paused because the page resumes it after auxiliary
  // metadata resolves; VueUse owns interval cleanup when the scope is disposed.
  const { pause: pauseAutoRefresh, resume: resumeAutoRefresh } = useIntervalFn(
    async () => {
      if (!autoRefreshEnabled.value) return
      if (document.hidden) return
      if (loading.value || autoRefreshFetching.value) return
      if (options.isRefreshPaused()) return
      if (inAutoRefreshSilentWindow()) {
        // Round remaining milliseconds up to seconds so the countdown includes
        // the unfinished final second of the silent window.
        autoRefreshCountdown.value = Math.max(
          0,
          Math.ceil((autoRefreshSilentUntil.value - Date.now()) / 1000),
        )
        return
      }

      if (autoRefreshCountdown.value <= 0) {
        autoRefreshCountdown.value = autoRefreshIntervalSeconds.value
        await refreshProvidersIncrementally()
        return
      }

      autoRefreshCountdown.value -= 1
    },
    1000,
    { immediate: false },
  )
  if (typeof window !== 'undefined') loadSavedAutoRefresh()
  return {
    PROVIDER_SORT_STORAGE_KEY,
    providers,
    loading,
    params,
    pagination,
    sortState,
    hasPendingListSync,
    autoRefreshIntervals,
    autoRefreshEnabled,
    autoRefreshIntervalSeconds,
    autoRefreshCountdown,
    syncProviderListDerivedParams,
    setAutoRefreshEnabled,
    setAutoRefreshInterval,
    load,
    reload,
    debouncedReload,
    handlePageChange,
    handlePageSizeChange,
    handleSort,
    enterAutoRefreshSilentWindow,
    pauseAutoRefresh,
    resumeAutoRefresh,
  }
}
