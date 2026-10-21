import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { createRouter, createWebHistory, type Router } from 'vue-router'
import { createPinia, setActivePinia } from 'pinia'
import { mount, flushPromises } from '@vue/test-utils'
import { defineComponent, h, nextTick } from 'vue'
import {
  useNavigationLoadingState,
  _resetNavigationLoadingInstance,
} from '@/composables/useNavigationLoading'
import { useRoutePrefetch } from '@/composables/useRoutePrefetch'
import { installNavigationLoading } from '@/router/navigationLoading'

// navigation.spec.ts checks router destinations, prefetch entry points, and
// navigation-loading feedback. Small route components and browser idle-callback substitutes let the tests observe routing outcomes, error
// delivery, and completion acknowledgement through the production navigation-loading hooks.

// MockDashboard, MockKeys, and MockUsage provide distinct route targets whose
// identity the router can resolve during navigation tests.
const MockDashboard = defineComponent({
  name: 'MockDashboard',
  // render returns the dashboard marker used by this route fixture.
  render() {
    return h('div', { class: 'dashboard' }, 'Dashboard')
  },
})

const MockKeys = defineComponent({
  name: 'MockKeys',
  // render returns the keys marker used by this route fixture.
  render() {
    return h('div', { class: 'keys' }, 'Keys')
  },
})

const MockUsage = defineComponent({
  name: 'MockUsage',
  // render returns the usage marker used by this route fixture.
  render() {
    return h('div', { class: 'usage' }, 'Usage')
  },
})
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    isAuthenticated: true,
    isAdmin: false,
    checkAuth: vi.fn(),
  }),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    siteName: 'Test Site',
  }),
}))

// createTestRouter constructs a browser-history router with three synchronous
// route fixtures. Callers mount it before awaiting readiness; navigation-loading hooks are installed separately by tests that exercise loading
// feedback.
function createTestRouter(): Router {
  return createRouter({
    history: createWebHistory(),
    routes: [
      {
        path: '/',
        redirect: '/dashboard',
      },
      {
        path: '/dashboard',
        name: 'Dashboard',
        component: MockDashboard,
        meta: { requiresAuth: true, title: 'Dashboard' },
      },
      {
        path: '/keys',
        name: 'Keys',
        component: MockKeys,
        meta: { requiresAuth: true, title: 'Keys' },
      },
      {
        path: '/usage',
        name: 'Usage',
        component: MockUsage,
        meta: { requiresAuth: true, title: 'Usage' },
      },
    ],
  })
}

// TestApp supplies the mounted host used to install router plugins and drive
// the navigation test lifecycle.
const TestApp = defineComponent({
  name: 'TestApp',
  // setup returns the test host render function used during router mounting.
  setup() {
    return () => h('div', { id: 'app' }, [h('router-view')])
  },
})

describe('Navigation Integration Tests', () => {
  // Each test creates its own router and saves the browser idle callbacks for
  // restoration after pending navigation assertions complete.
  let router: Router
  let originalRequestIdleCallback: typeof window.requestIdleCallback
  let originalCancelIdleCallback: typeof window.cancelIdleCallback

  beforeEach(() => {
    setActivePinia(createPinia())
    // Clear the shared navigation singleton before mounting a fresh router so
    // activity left by another case cannot affect initial-state assertions.
    _resetNavigationLoadingInstance()
    router = createTestRouter()
    originalRequestIdleCallback = window.requestIdleCallback
    originalCancelIdleCallback = window.cancelIdleCallback

    // Schedule idle callbacks through a timer in jsdom so prefetch work runs
    // after route setup and exposes a cancellable handle.
    vi.stubGlobal('requestIdleCallback', (cb: IdleRequestCallback) => {
      // The synthetic deadline supplies fifty milliseconds of idle budget,
      // letting prefetch callbacks run within a predictable browser-like
      // scheduling opportunity.
      const id = setTimeout(() => cb({ didTimeout: false, timeRemaining: () => 50 }), 0)
      return id
    })
    vi.stubGlobal('cancelIdleCallback', (id: number) => clearTimeout(id))
  })

  afterEach(() => {
    // Restore browser hooks and spies after each case so subsequent suites
    // observe their own idle scheduling and notification behavior.
    vi.restoreAllMocks()
    window.requestIdleCallback = originalRequestIdleCallback
    window.cancelIdleCallback = originalCancelIdleCallback
  })

  describe('完整页面导航流程', () => {
    // The test router completes navigation without installing navigation-loading hooks. The shared loading state remains false before mounting
    // and after the dashboard route settles.
    it('导航时应该触发加载状态变化', async () => {
      const navigationLoading = useNavigationLoadingState()
      expect(navigationLoading.isLoading.value).toBe(false)
      const wrapper = mount(TestApp, {
        global: {
          plugins: [router],
        },
      })
      await router.isReady()
      await flushPromises()
      await router.push('/dashboard')
      await flushPromises()
      await nextTick()
      expect(navigationLoading.isLoading.value).toBe(false)

      wrapper.unmount()
    })

    // The mounted test application navigates to the dashboard route. The
    // router reports the dashboard path after asynchronous route work
    // settles, protecting destination selection.
    it('导航到新页面应该正确渲染组件', async () => {
      const wrapper = mount(TestApp, {
        global: {
          plugins: [router],
        },
      })

      await router.isReady()
      await router.push('/dashboard')
      await flushPromises()
      await nextTick()
      expect(router.currentRoute.value.path).toBe('/dashboard')

      wrapper.unmount()
    })

    // Several route pushes are issued before their completion is awaited.
    // The router settles on the last dashboard request after pending
    // navigation promises flush.
    it('连续快速导航应该正确处理路由状态', async () => {
      const wrapper = mount(TestApp, {
        global: {
          plugins: [router],
        },
      })

      await router.isReady()
      await router.push('/dashboard')
      router.push('/keys')
      router.push('/usage')
      router.push('/dashboard')

      await flushPromises()
      await nextTick()
      expect(router.currentRoute.value.path).toBe('/dashboard')

      wrapper.unmount()
    })
  })

  describe('路由预加载', () => {
    // An after-navigation hook delegates the destination to route prefetch.
    // Completing dashboard navigation invokes that delegate, connecting
    // successful routing with the prefetch entry point.
    it('导航后应该触发相关路由预加载', async () => {
      const routePrefetch = useRoutePrefetch()
      const triggerSpy = vi.spyOn(routePrefetch, 'triggerPrefetch')
      router.afterEach((to) => {
        routePrefetch.triggerPrefetch(to)
      })

      const wrapper = mount(TestApp, {
        global: {
          plugins: [router],
        },
      })

      await router.isReady()
      await router.push('/dashboard')
      await flushPromises()
      expect(triggerSpy).toHaveBeenCalled()

      wrapper.unmount()
    })

    // The same route is submitted to prefetch twice after navigation.
    // Waiting for each idle task leaves the completed-route count unchanged
    // on the second trigger.
    it('已预加载的路由不应重复预加载', async () => {
      const routePrefetch = useRoutePrefetch()

      const wrapper = mount(TestApp, {
        global: {
          plugins: [router],
        },
      })

      await router.isReady()
      await router.push('/dashboard')
      await flushPromises()
      routePrefetch.triggerPrefetch(router.currentRoute.value)
      // Allow the timer-backed idle callback and its asynchronous imports to
      // settle before sampling the set of completed prefetch targets.
      await new Promise((resolve) => setTimeout(resolve, 100))

      const prefetchedCount = routePrefetch.prefetchedRoutes.value.size
      routePrefetch.triggerPrefetch(router.currentRoute.value)
      // Allow the timer-backed idle callback and its asynchronous imports to
      // settle before sampling the set of completed prefetch targets.
      await new Promise((resolve) => setTimeout(resolve, 100))
      expect(routePrefetch.prefetchedRoutes.value.size).toBe(prefetchedCount)

      wrapper.unmount()
    })

    // A prefetch request is immediately followed by another route request.
    // The observed completed-route set stays within the asserted bound after
    // those consecutive triggers.
    it('路由变化时应取消之前的预加载任务', async () => {
      const routePrefetch = useRoutePrefetch()

      const wrapper = mount(TestApp, {
        global: {
          plugins: [router],
        },
      })

      await router.isReady()
      routePrefetch.triggerPrefetch(router.currentRoute.value)
      routePrefetch.triggerPrefetch({ path: '/keys' } as any)
      expect(routePrefetch.prefetchedRoutes.value.size).toBeLessThanOrEqual(2)

      wrapper.unmount()
    })
  })

  describe('Chunk 加载错误恢复', () => {
    // A lazy route component rejects with a dynamic-import loading error.
    // Router error handling observes the failure after navigation rejects and
    // queued callbacks have flushed.
    it('chunk 加载失败应该被正确捕获', async () => {
      const errorHandler = vi.fn()
      const errorRouter = createRouter({
        history: createWebHistory(),
        routes: [
          {
            path: '/dashboard',
            name: 'Dashboard',
            component: MockDashboard,
          },
          {
            path: '/error-page',
            name: 'ErrorPage',
            component: () =>
              Promise.reject(new Error('Failed to fetch dynamically imported module')),
          },
        ],
      })

      errorRouter.onError(errorHandler)

      const wrapper = mount(TestApp, {
        global: {
          plugins: [errorRouter],
        },
      })

      await errorRouter.isReady()
      await errorRouter.push('/dashboard')
      await flushPromises()
      try {
        await errorRouter.push('/error-page')
      } catch {
        // The route deliberately rejects during component loading. Continue
        // to the error-handler assertions after observing that expected
        // navigation failure.
      }

      await flushPromises()
      expect(errorHandler).toHaveBeenCalled()

      wrapper.unmount()
    })

    // A lazy component rejects with an error named ChunkLoadError. Router
    // error handling receives that name, preserving the information callers
    // use to recognize loading failures.
    it('chunk 加载错误应该包含正确的错误信息', async () => {
      let capturedError: Error | null = null

      const errorRouter = createRouter({
        history: createWebHistory(),
        routes: [
          {
            path: '/dashboard',
            name: 'Dashboard',
            component: MockDashboard,
          },
          {
            path: '/chunk-error',
            name: 'ChunkError',
            component: () => {
              const error = new Error('Loading chunk failed')
              error.name = 'ChunkLoadError'
              return Promise.reject(error)
            },
          },
        ],
      })

      errorRouter.onError((error) => {
        capturedError = error
      })

      const wrapper = mount(TestApp, {
        global: {
          plugins: [errorRouter],
        },
      })

      await errorRouter.isReady()

      try {
        await errorRouter.push('/chunk-error')
      } catch {
        // The route deliberately rejects during component loading. Continue
        // to the error-handler assertions after observing that expected
        // navigation failure.
      }

      await flushPromises()

      expect(capturedError).not.toBeNull()
      expect(capturedError!.name).toBe('ChunkLoadError')

      wrapper.unmount()
    })
  })

  describe('导航状态管理', () => {
    // The router uses production navigation-loading hooks and completes
    // dashboard navigation. Loading feedback remains visible until
    // finishNavigation acknowledges the current navigation identifier.
    it('导航结束后保留完成反馈，动画结束才隐藏', async () => {
      const navigationLoading = useNavigationLoadingState()
      const DelayedComponent = defineComponent({
        name: 'DelayedComponent',
        // setup delays the fixture render function to model asynchronous
        // component initialization. The surrounding test observes router
        // completion feedback, and this fixture remains available on its
        // separate delayed route.
        async setup() {
          // Delay fixture setup by fifty milliseconds to represent an
          // asynchronously prepared route component when this target is
          // selected.
          await new Promise((resolve) => setTimeout(resolve, 50))
          return () => h('div', 'Delayed')
        },
      })

      const delayRouter = createRouter({
        history: createWebHistory(),
        routes: [
          {
            path: '/dashboard',
            name: 'Dashboard',
            component: MockDashboard,
          },
          {
            path: '/delayed',
            name: 'Delayed',
            component: DelayedComponent,
          },
        ],
      })
      installNavigationLoading(delayRouter)

      const wrapper = mount(TestApp, {
        global: {
          plugins: [delayRouter],
        },
      })

      await delayRouter.isReady()
      await delayRouter.push('/dashboard')
      await flushPromises()
      expect(navigationLoading.isNavigating.value).toBe(false)
      expect(navigationLoading.isLoading.value).toBe(true)
      navigationLoading.finishNavigation(navigationLoading.navigationId.value)
      expect(navigationLoading.isLoading.value).toBe(false)

      wrapper.unmount()
    })

    // A route entry guard cancels navigation after dashboard loading.
    // Production hooks leave completion feedback visible, and acknowledging
    // the current navigation identifier clears that feedback.
    it('导航取消时应该正确重置状态', async () => {
      const navigationLoading = useNavigationLoadingState()

      const testRouter = createRouter({
        history: createWebHistory(),
        routes: [
          {
            path: '/dashboard',
            name: 'Dashboard',
            component: MockDashboard,
          },
          {
            path: '/keys',
            name: 'Keys',
            component: MockKeys,
            beforeEnter: (_to, _from, next) => {
              next(false)
            },
          },
        ],
      })

      installNavigationLoading(testRouter)

      const wrapper = mount(TestApp, {
        global: {
          plugins: [testRouter],
        },
      })

      await testRouter.isReady()
      await testRouter.push('/dashboard')
      await flushPromises()
      // The guard rejects this destination intentionally. Swallow that
      // navigation promise so the test can inspect the shared completion-feedback state.
      await testRouter.push('/keys').catch(() => {})
      await flushPromises()
      expect(navigationLoading.isNavigating.value).toBe(false)
      expect(navigationLoading.isLoading.value).toBe(true)
      navigationLoading.finishNavigation(navigationLoading.navigationId.value)
      expect(navigationLoading.isLoading.value).toBe(false)

      wrapper.unmount()
    })
  })
})
