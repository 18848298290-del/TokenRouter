import { ref, reactive, onUnmounted, toRaw } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'
import type { BasePaginationResponse, FetchOptions } from '@/api/common.types'
import { getPersistedPageSize, setPersistedPageSize } from './usePersistedPageSize'

// useTableLoader.ts loads paginated table data with reactive filters, debounced reloads,
// and request cancellation. Each load aborts the previous controller, passes the current
// page and raw parameter object to the caller's fetch function, and publishes the returned
// rows and totals. Fetch implementations are responsible for honoring cancellation.
//
// Controller identity decides which completion may clear the loading flag, but successful
// response writes have no identity guard. A fetch function that resolves after abort can
// therefore overwrite newer data. Unmount aborts the current controller; this composable
// does not explicitly cancel a pending debounced reload. Page changes retain filters and
// reload from their chosen page.

// PaginationState holds the requested page and the server's result totals.
interface PaginationState {
  // page and page_size select the next fetch, while total and pages describe the latest
  // published response; changing pages retains the same filter object.
  page: number // one-based requested page number
  page_size: number // requested rows per page
  total: number // server-reported matching row count
  pages: number // server-reported result page count
}

// TableLoaderOptions supplies paginated fetching, initial filters, and optional timing
// defaults.
interface TableLoaderOptions<T, P> {
  // fetchFn receives pagination, a raw filter object, and the current AbortSignal. It must
  // honor cancellation to prevent stale successful results from replacing newer rows in
  // this loader.
  fetchFn: (
    page: number,
    pageSize: number,
    params: P,
    options?: FetchOptions,
  ) => Promise<BasePaginationResponse<T>>
  // initialParams is shallow-copied at construction; nested values remain shared with the
  // supplied object and its owners.
  initialParams?: P // initial shallow-copied filter values
  // pageSize overrides the configured runtime default for this table instance when it is
  // supplied, including zero.
  pageSize?: number // initial rows per page
  // debounceMs controls delayed reload dispatch in milliseconds and defaults to
  // SEARCH_DEBOUNCE_MS when omitted from the options.
  debounceMs?: number // reload debounce delay milliseconds
}

// useTableLoader creates rows, filters, pagination, and loading actions for a component.
// Invoke it during setup so unmount aborts the current request, and provide a fetch
// function that honors its AbortSignal. Initial filters are shallow-copied and page size
// defaults to runtime table preferences.
//
// Each load replaces the controller before awaiting results. Identity guards loading
// completion only; successful results are published directly. Debounced reload resets the
// page when it executes, and this composable provides no explicit timer disposal.
export function useTableLoader<T, P extends Record<string, any>>(
  options: TableLoaderOptions<T, P>,
) {
  const { fetchFn, initialParams, pageSize, debounceMs = SEARCH_DEBOUNCE_MS } = options

  // items and pagination publish fetch results, params holds reactive filters, and loading
  // follows the controller currently owned by this table instance.
  const items = ref<T[]>([])
  const loading = ref(false)
  const params = reactive<P>({ ...(initialParams || {}) } as P)
  const pagination = reactive<PaginationState>({
    page: 1,
    page_size: pageSize ?? getPersistedPageSize(),
    total: 0,
    pages: 0,
  })

  let abortController: AbortController | null = null

  // isAbortError recognizes DOM and Axios-style cancellation names and codes.
  const isAbortError = (error: any) => {
    return (
      error?.name === 'AbortError' ||
      error?.code === 'ERR_CANCELED' ||
      error?.name === 'CanceledError'
    )
  }

  // load requests the current page and filters after aborting the prior controller. It
  // passes the raw reactive parameter object, so fetch implementations decide when to
  // snapshot or encode those values. Cancellation-shaped errors are absorbed; other
  // failures are logged and rethrown.
  //
  // Rows and pagination totals are written after every successful response, including a
  // response whose transport ignored abort. Controller identity prevents an older finally
  // block from clearing a newer loading indicator, but does not suppress stale response
  // data.
  const load = async () => {
    // Abort prior transport work before replacing the controller so only the new request
    // owns completion of the loading indicator.
    if (abortController) {
      abortController.abort()
    }
    const currentController = new AbortController()
    abortController = currentController
    loading.value = true

    try {
      const response = await fetchFn(pagination.page, pagination.page_size, toRaw(params) as P, {
        signal: currentController.signal,
      })

      items.value = response.items || []
      pagination.total = response.total || 0
      pagination.pages = response.pages || 0
    } catch (error) {
      // Cancellation is an expected outcome of replacement loads and unmount; unrelated
      // errors remain observable through logging and promise rejection.
      if (!isAbortError(error)) {
        console.error('Table load error:', error)
        throw error
      }
    } finally {
      // An aborted request can settle after a replacement starts, so its finally block
      // must leave the replacement loading flag active.
      if (abortController === currentController) {
        loading.value = false
      }
    }
  }

  // reload resets pagination to the first page and returns the load promise. Callers
  // awaiting it observe ordinary fetch failures, while recognized cancellation errors
  // resolve through load's cancellation handling.
  const reload = () => {
    pagination.page = 1
    return load()
  }

  const debouncedReload = useDebounceFn(reload, debounceMs)

  // handlePageChange clamps the requested page to available pagination bounds and starts
  // loading. It returns immediately rather than exposing the load promise, so callers
  // cannot await fetch failures through this handler.
  const handlePageChange = (page: number) => {
    // Clamp navigation against at least one available page.
    const validPage = Math.max(1, Math.min(page, pagination.pages || 1))
    pagination.page = validPage
    load()
  }

  // handlePageSizeChange applies a new page size, resets to page one, and starts loading.
  // It invokes the compatibility preference setter before loading and returns before the
  // fetch promise settles.
  const handlePageSizeChange = (size: number) => {
    pagination.page_size = size
    pagination.page = 1
    setPersistedPageSize(size)
    load()
  }

  // Abort the last transport request when the component leaves; successful publication
  // still depends on the fetch implementation respecting cancellation.
  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    items,
    loading,
    params,
    pagination,
    load,
    reload,
    debouncedReload,
    handlePageChange,
    handlePageSizeChange,
  }
}
