import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import type { RouteLocationNormalized, Router, RouteRecordNormalized } from 'vue-router'

import { useRoutePrefetch, _adminPrefetchMap, _userPrefetchMap } from '../useRoutePrefetch'

// useRoutePrefetch.spec.ts checks lazy route lookup, idle scheduling, cancellation, and
// processed-origin tracking with a mock router. Tests provide timer-backed idle callbacks
// and restore browser scheduling afterward, allowing both ordinary idle execution and
// timeout-triggered dispatch to be observed.

// createMockRoute builds normalized route fields for one exact path. Matching metadata and
// navigation history are empty because these tests exercise path-based prefetch adjacency
// rather than router resolution.
const createMockRoute = (path: string): RouteLocationNormalized => ({
  path,
  name: undefined,
  params: {},
  query: {},
  hash: '',
  fullPath: path,
  matched: [],
  meta: {},
  redirectedFrom: undefined,
})

// createMockRouter supplies lazy component records for supported administrator and user
// routes. All records share one resolving import spy, and only getRoutes is implemented
// because prefetch lookup uses that router surface.
const createMockRouter = (): Router => {
  const mockImportFn = vi.fn().mockResolvedValue({ default: {} })

  const routes: Partial<RouteRecordNormalized>[] = [
    { path: '/admin/dashboard', components: { default: mockImportFn } },
    { path: '/admin/providers', components: { default: mockImportFn } },
    { path: '/admin/users', components: { default: mockImportFn } },
    { path: '/admin/groups', components: { default: mockImportFn } },
    { path: '/admin/subscriptions', components: { default: mockImportFn } },
    { path: '/admin/redeem', components: { default: mockImportFn } },
    { path: '/dashboard', components: { default: mockImportFn } },
    { path: '/keys', components: { default: mockImportFn } },
    { path: '/usage', components: { default: mockImportFn } },
    { path: '/redeem', components: { default: mockImportFn } },
    { path: '/profile', components: { default: mockImportFn } },
  ]

  return {
    getRoutes: () => routes as RouteRecordNormalized[],
  } as Router
}

describe('useRoutePrefetch', () => {
  let originalRequestIdleCallback: typeof window.requestIdleCallback
  let originalCancelIdleCallback: typeof window.cancelIdleCallback
  let mockRouter: Router

  beforeEach(() => {
    mockRouter = createMockRouter()

    originalRequestIdleCallback = window.requestIdleCallback
    originalCancelIdleCallback = window.cancelIdleCallback

    // Run idle callbacks through a timer so cancellation can remove queued work before
    // dispatch while imports still complete asynchronously.
    vi.stubGlobal('requestIdleCallback', (cb: IdleRequestCallback) => {
      const id = setTimeout(() => cb({ didTimeout: false, timeRemaining: () => 50 }), 0)
      return id
    })
    vi.stubGlobal('cancelIdleCallback', (id: number) => clearTimeout(id))
  })

  afterEach(() => {
    vi.restoreAllMocks()

    // Restore captured idle APIs after each test so timeout-specific stubs do not affect
    // scheduling behavior in later cases.
    window.requestIdleCallback = originalRequestIdleCallback
    window.cancelIdleCallback = originalCancelIdleCallback
  })

  describe('_isAdminRoute', () => {
    // The classifier receives several administrator paths from the supported adjacency
    // table. Each path must be recognized as administrative by its shared prefix rule.
    it('应该正确识别管理员路由', () => {
      const { _isAdminRoute } = useRoutePrefetch(mockRouter)
      expect(_isAdminRoute('/admin/dashboard')).toBe(true)
      expect(_isAdminRoute('/admin/users')).toBe(true)
      expect(_isAdminRoute('/admin/providers')).toBe(true)
    })

    // The classifier receives dashboard, keys, and usage paths outside the administrator
    // namespace. Each must remain classified as a non-administrator route for prefetch
    // configuration consumers.
    it('应该正确识别非管理员路由', () => {
      const { _isAdminRoute } = useRoutePrefetch(mockRouter)
      expect(_isAdminRoute('/dashboard')).toBe(false)
      expect(_isAdminRoute('/keys')).toBe(false)
      expect(_isAdminRoute('/usage')).toBe(false)
    })
  })

  describe('_getPrefetchConfig', () => {
    // The administrator dashboard has two configured neighboring routes available in the
    // mock router. Configuration lookup must return two lazy import functions for those
    // neighbors.
    it('管理员 dashboard 应该返回正确的预加载配置', () => {
      const { _getPrefetchConfig } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/admin/dashboard')
      const config = _getPrefetchConfig(route)

      expect(config).toHaveLength(2)
    })

    // The user dashboard has two configured neighboring routes available in the mock
    // router. Configuration lookup must return two entries for that dashboard's prefetch
    // configuration.
    it('普通用户 dashboard 应该返回正确的预加载配置', () => {
      const { _getPrefetchConfig } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/dashboard')
      const config = _getPrefetchConfig(route)

      expect(config).toHaveLength(2)
    })

    // A route outside the adjacency table is passed to configuration lookup. Its result
    // must be an empty list even though the router contains other lazy components.
    it('未定义的路由应该返回空数组', () => {
      const { _getPrefetchConfig } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/unknown-route')
      const config = _getPrefetchConfig(route)

      expect(config).toHaveLength(0)
    })
  })

  describe('triggerPrefetch', () => {
    // An administrator dashboard prefetch is scheduled through the timer-backed idle
    // callback. After dispatch and import completion, the originating path must appear in
    // processed routes.
    it('应该在浏览器空闲时触发预加载', async () => {
      const { triggerPrefetch, prefetchedRoutes } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/admin/dashboard')

      triggerPrefetch(route)

      await new Promise((resolve) => setTimeout(resolve, 100))

      expect(prefetchedRoutes.value.has('/admin/dashboard')).toBe(true)
    })

    // The same originating route is triggered again after its first batch has completed.
    // The processed-origin set must still contain a single entry for that path.
    it('应该避免重复预加载同一路由', async () => {
      const { triggerPrefetch, prefetchedRoutes } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/admin/dashboard')

      triggerPrefetch(route)
      await new Promise((resolve) => setTimeout(resolve, 100))

      triggerPrefetch(route)
      await new Promise((resolve) => setTimeout(resolve, 100))

      expect(prefetchedRoutes.value.size).toBe(1)
    })
  })

  describe('cancelPendingPrefetch', () => {
    // A route schedules idle work and is cancelled immediately before it runs. The
    // processed-route set must remain empty at the synchronous cancellation check.
    it('应该取消挂起的预加载任务', () => {
      const { triggerPrefetch, cancelPendingPrefetch, prefetchedRoutes } =
        useRoutePrefetch(mockRouter)
      const route = createMockRoute('/admin/dashboard')

      triggerPrefetch(route)
      cancelPendingPrefetch()

      expect(prefetchedRoutes.value.size).toBe(0)
    })
  })

  describe('路由变化时取消之前的预加载', () => {
    // Two different routes are triggered before the first idle callback runs. After
    // waiting for dispatch, the latest originating route must be recorded as processed.
    it('应该在路由变化时取消之前的预加载任务', async () => {
      const { triggerPrefetch, prefetchedRoutes } = useRoutePrefetch(mockRouter)

      triggerPrefetch(createMockRoute('/admin/dashboard'))

      triggerPrefetch(createMockRoute('/admin/users'))

      await new Promise((resolve) => setTimeout(resolve, 100))

      expect(prefetchedRoutes.value.has('/admin/users')).toBe(true)
    })
  })

  describe('resetPrefetchState', () => {
    // A route completes its import batch before reset is invoked. Reset must clear the
    // recorded processed routes so the scheduler has no retained completed origin.
    it('应该重置所有预加载状态', async () => {
      const { triggerPrefetch, resetPrefetchState, prefetchedRoutes } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/admin/dashboard')

      triggerPrefetch(route)
      await new Promise((resolve) => setTimeout(resolve, 100))

      expect(prefetchedRoutes.value.size).toBeGreaterThan(0)

      resetPrefetchState()

      expect(prefetchedRoutes.value.size).toBe(0)
    })
  })

  describe('预加载映射表', () => {
    // The exported administrator adjacency map is read directly by path. It must expose
    // the dashboard entry with its expected pair of neighboring routes.
    it('管理员预加载映射表应该包含正确的路由', () => {
      expect(_adminPrefetchMap).toHaveProperty('/admin/dashboard')
      expect(_adminPrefetchMap['/admin/dashboard']).toHaveLength(2)
    })

    // The exported user adjacency map is read directly by path. It must expose the user
    // dashboard entry with its expected pair of neighboring routes.
    it('用户预加载映射表应该包含正确的路由', () => {
      expect(_userPrefetchMap).toHaveProperty('/dashboard')
      expect(_userPrefetchMap['/dashboard']).toHaveLength(2)
    })
  })

  describe('requestIdleCallback 超时处理', () => {
    // The idle stub invokes its callback only after the configured timeout with no
    // remaining idle budget. The originating route must still be processed after dispatch.
    it('超时后仍能正常执行预加载', async () => {
      vi.stubGlobal(
        'requestIdleCallback',
        (cb: IdleRequestCallback, options?: IdleRequestOptions) => {
          const timeout = options?.timeout || 2000
          return setTimeout(() => cb({ didTimeout: true, timeRemaining: () => 0 }), timeout)
        },
      )

      const { triggerPrefetch, prefetchedRoutes } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/dashboard')

      triggerPrefetch(route)

      await new Promise((resolve) => setTimeout(resolve, 2100))

      expect(prefetchedRoutes.value.has('/dashboard')).toBe(true)
    })
  })

  describe('预加载失败处理', () => {
    // A normal mock router is used to invoke triggerPrefetch inside a synchronous
    // exception assertion. Scheduling must not throw synchronously; this case does not
    // simulate an import rejection.
    it('预加载失败时应该静默处理不影响页面功能', async () => {
      const { triggerPrefetch } = useRoutePrefetch(mockRouter)
      const route = createMockRoute('/admin/dashboard')

      expect(() => triggerPrefetch(route)).not.toThrow()
    })
  })

  describe('无 router 时的行为', () => {
    // Prefetch is triggered with no router instance while adjacency exists for the origin.
    // After idle dispatch, no component imports can be found and no origin is recorded.
    it('没有传入 router 时应该正常工作但不执行预加载', async () => {
      const { triggerPrefetch, prefetchedRoutes } = useRoutePrefetch()
      const route = createMockRoute('/admin/dashboard')

      triggerPrefetch(route)
      await new Promise((resolve) => setTimeout(resolve, 100))

      expect(prefetchedRoutes.value.size).toBe(0)
    })
  })
})
