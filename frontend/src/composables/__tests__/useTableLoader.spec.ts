import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { useTableLoader } from '@/composables/useTableLoader'

// useTableLoader.spec.ts checks pagination, debounce dispatch, request cancellation, and
// error propagation through the shared table loader. Fetch functions and timers are
// controlled locally, and the unmount hook is stubbed because these cases instantiate the
// composable outside mounted components.

vi.mock('@vueuse/core', () => ({
  useDebounceFn: (fn: Function, ms: number) => {
    let timer: ReturnType<typeof setTimeout> | null = null
    // debounced replaces the previous mock timer and forwards the latest arguments after
    // the delay. The mock leaves promise handling to the test and exposes separate timer
    // cancellation.
    const debounced = (...args: any[]) => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(() => fn(...args), ms)
    }
    debounced.cancel = () => {
      if (timer) clearTimeout(timer)
    }
    return debounced
  },
}))

vi.mock('vue', async () => {
  const actual = await vi.importActual('vue')
  return {
    ...actual,
    onUnmounted: vi.fn(),
  }
})

// createMockFetchFn creates a resolving fetch spy with configurable rows and pagination
// totals. Empty rows, zero total, and one page are the defaults for tests concerned with
// request arguments.
const createMockFetchFn = (items: any[] = [], total = 0, pages = 1) => {
  return vi.fn().mockResolvedValue({ items, total, pages })
}

describe('useTableLoader', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.clearAllMocks()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  describe('基础加载', () => {
    // A fetch resolves with two rows and pagination totals while loading begins empty.
    // Awaiting load must publish rows and counts and clear the loading flag.
    it('load 执行 fetchFn 并更新 items', async () => {
      const mockItems = [
        { id: 1, name: 'item1' },
        { id: 2, name: 'item2' },
      ]
      const fetchFn = createMockFetchFn(mockItems, 2, 1)

      const { items, loading, load, pagination } = useTableLoader({
        fetchFn,
      })

      expect(items.value).toHaveLength(0)

      await load()

      expect(items.value).toEqual(mockItems)
      expect(pagination.total).toBe(2)
      expect(pagination.pages).toBe(1)
      expect(loading.value).toBe(false)
    })

    // A deferred fetch remains pending after load begins and later receives its response.
    // The loading flag must stay true while pending and clear after completion.
    it('load 期间 loading 为 true', async () => {
      let resolveLoad: (v: any) => void
      const fetchFn = vi.fn(
        () =>
          new Promise((resolve) => {
            resolveLoad = resolve
          }),
      )

      const { loading, load } = useTableLoader({ fetchFn })

      const p = load()
      expect(loading.value).toBe(true)

      resolveLoad!({ items: [], total: 0, pages: 0 })
      await p

      expect(loading.value).toBe(false)
    })

    // The loader starts without a caller-provided page size in the default test
    // environment. Fetch must receive page one and twenty rows with a cancellation signal.
    it('使用默认 pageSize=20', async () => {
      const fetchFn = createMockFetchFn()
      const { load, pagination } = useTableLoader({ fetchFn })

      await load()

      expect(fetchFn).toHaveBeenCalledWith(
        1,
        20,
        expect.anything(),
        expect.objectContaining({ signal: expect.any(AbortSignal) }),
      )
      expect(pagination.page_size).toBe(20)
    })

    // The caller initializes the loader with a fifty-row page size. The initial fetch must
    // receive that size together with page one and its usual request arguments.
    it('可自定义 pageSize', async () => {
      const fetchFn = createMockFetchFn()
      const { load } = useTableLoader({ fetchFn, pageSize: 50 })

      await load()

      expect(fetchFn).toHaveBeenCalledWith(1, 50, expect.anything(), expect.anything())
    })
  })

  describe('分页', () => {
    // An initial response establishes five available pages before page three is selected.
    // The page handler must update pagination and dispatch a fetch for the selected page.
    it('handlePageChange 更新页码并加载', async () => {
      const fetchFn = createMockFetchFn([], 100, 5)
      const { handlePageChange, pagination, load } = useTableLoader({ fetchFn })

      await load()
      fetchFn.mockClear()

      handlePageChange(3)

      expect(pagination.page).toBe(3)

      await vi.runAllTimersAsync()
      expect(fetchFn).toHaveBeenCalledWith(3, 20, expect.anything(), expect.anything())
    })

    // A table on page three changes its page size to fifty after initial loading. The
    // handler must reset the current page to one and apply the new size.
    it('handlePageSizeChange 重置到第1页并加载', async () => {
      const fetchFn = createMockFetchFn([], 100, 5)
      const { handlePageSizeChange, pagination, load } = useTableLoader({ fetchFn })

      await load()
      pagination.page = 3
      fetchFn.mockClear()

      handlePageSizeChange(50)

      expect(pagination.page).toBe(1)
      expect(pagination.page_size).toBe(50)
    })

    // A response establishes a five-page result before out-of-range page requests arrive.
    // Large and zero requests must clamp to the last and first available pages.
    it('handlePageChange 限制页码范围', async () => {
      const fetchFn = createMockFetchFn([], 100, 5)
      const { handlePageChange, pagination, load } = useTableLoader({ fetchFn })

      await load()

      handlePageChange(999)
      expect(pagination.page).toBe(5)

      handlePageChange(0)
      expect(pagination.page).toBe(1)
    })
  })

  describe('搜索防抖', () => {
    // Three reload requests arrive before the default debounce interval expires. No fetch
    // should occur immediately, and advancing the interval must dispatch one fetch call.
    it('debouncedReload 在 300ms 内多次调用只执行一次', async () => {
      const fetchFn = createMockFetchFn()
      const { debouncedReload } = useTableLoader({ fetchFn })

      debouncedReload()
      debouncedReload()
      debouncedReload()

      expect(fetchFn).not.toHaveBeenCalled()

      vi.advanceTimersByTime(300)

      await vi.runAllTimersAsync()

      expect(fetchFn).toHaveBeenCalledTimes(1)
    })

    // A loaded table moves away from its first page before reload is awaited. Reload must
    // reset pagination to page one before completing its fetch.
    it('reload 重置到第 1 页', async () => {
      const fetchFn = createMockFetchFn([], 100, 5)
      const { reload, pagination, load } = useTableLoader({ fetchFn })

      await load()
      pagination.page = 3

      await reload()

      expect(pagination.page).toBe(1)
    })
  })

  describe('请求取消', () => {
    // A second load starts while an abort-aware first fetch remains pending. Both promises
    // are settled after time advances, and the fetch spy must record two dispatches.
    it('新请求取消前一个未完成的请求', async () => {
      let callCount = 0
      const fetchFn = vi.fn((_page, _size, _params, options) => {
        callCount++
        const currentCall = callCount
        return new Promise((resolve, reject) => {
          if (options?.signal) {
            // The fetch stub observes AbortSignal and rejects promptly, making this case
            // exercise cancellation-aware transport rather than stale successful response
            // suppression.
            options.signal.addEventListener('abort', () => {
              reject({ name: 'CanceledError', code: 'ERR_CANCELED' })
            })
          }

          setTimeout(() => {
            resolve({ items: [{ id: currentCall }], total: 1, pages: 1 })
          }, 1000)
        })
      })

      const { load } = useTableLoader({ fetchFn })

      const p1 = load()

      const p2 = load()

      vi.advanceTimersByTime(1000)
      await vi.runAllTimersAsync()

      await Promise.allSettled([p1, p2])

      expect(fetchFn).toHaveBeenCalledTimes(2)
    })
  })

  describe('错误处理', () => {
    // A fetch rejects with a server error unrelated to cancellation. Awaiting load must
    // reject with that message so the caller can handle ordinary request failure.
    it('非取消错误会被抛出', async () => {
      const fetchFn = vi.fn().mockRejectedValue(new Error('Server error'))
      const { load } = useTableLoader({ fetchFn })

      await expect(load()).rejects.toThrow('Server error')
    })

    // A fetch rejects with the recognized Axios cancellation name and code. Awaiting load
    // must resolve without throwing that expected cancellation outcome to the caller.
    it('取消错误被静默处理', async () => {
      const fetchFn = vi.fn().mockRejectedValue({ name: 'CanceledError', code: 'ERR_CANCELED' })
      const { load } = useTableLoader({ fetchFn })

      await load()
    })
  })
})
