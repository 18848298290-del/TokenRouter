import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'
import { installNavigationLoading } from '../navigationLoading'
import {
  _resetNavigationLoadingInstance,
  useNavigationLoadingState,
} from '@/composables/useNavigationLoading'

// navigationLoading.spec.ts exercises progress feedback through actual memory
// router navigation. Aborted and failed navigations finish their progress,
// while overlapping requests retain ownership of the newer progress state until
// that newer navigation settles or its completion animation finishes.

beforeEach(_resetNavigationLoadingInstance)
afterEach(_resetNavigationLoadingInstance)

/** setupRouter creates a memory router with application navigation progress installed. */
function setupRouter() {
  const view = { render: () => null }
  const router = createRouter({
    history: createMemoryHistory(),
    routes: ['/', '/slow', '/newer', '/error', '/blocked', '/redirect'].map((path) => ({
      path,
      component: view,
    })),
  })
  installNavigationLoading(router)
  return router
}

/**
 * deferred returns a pending navigation barrier and its release callback.
 * Each test releases every barrier it creates and awaits the corresponding router
 * navigation before checking the completed progress state.
 */
function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

describe('路由进度生命周期', () => {
  // A guard throws during one navigation and aborts a later navigation. Both
  // outcomes stop active progress, and finishing the error animation hides its
  // loading indicator.
  it('导航异常和守卫中止都会结束加载', async () => {
    const router = setupRouter()
    const state = useNavigationLoadingState()
    router.beforeEach((to) => {
      if (to.path === '/error') throw new Error('导航失败')
      if (to.path === '/blocked') return false
    })
    await expect(router.push('/error')).rejects.toThrow('导航失败')
    expect(state.isNavigating.value).toBe(false)
    state.finishNavigation(state.navigationId.value)
    expect(state.isLoading.value).toBe(false)
    await router.push('/blocked')
    expect(state.isNavigating.value).toBe(false)
  })

  // A slow navigation finishes after another pending navigation has taken
  // ownership. Its completion leaves the newer progress active until the newer
  // guard resolves.
  it('取消慢导航后，其完成回调不会关闭新导航', async () => {
    const router = setupRouter()
    const state = useNavigationLoadingState()
    const slow = deferred()
    const newer = deferred()
    router.beforeEach((to) => {
      if (to.path === '/slow') return slow.promise
      if (to.path === '/newer') return newer.promise
    })
    const first = router.push('/slow')
    await vi.waitFor(() => expect(state.isNavigating.value).toBe(true))
    const firstId = state.navigationId.value
    const second = router.push('/newer')
    await vi.waitFor(() => expect(state.navigationId.value).toBeGreaterThan(firstId))
    // Release the superseded guard first so its delayed completion exercises
    // ownership checks while the newer guard remains pending.
    slow.resolve()
    await first
    expect(state.isNavigating.value).toBe(true)
    newer.resolve()
    await second
    expect(state.isNavigating.value).toBe(false)
  })

  // A navigation guard redirects to the root route before navigation completes.
  // The final route settles while progress stays visible for its completion
  // animation.
  it('重定向完成后进入收尾状态', async () => {
    const router = setupRouter()
    const state = useNavigationLoadingState()
    router.beforeEach((to) => (to.path === '/redirect' ? '/' : undefined))
    await router.push('/redirect')
    expect(router.currentRoute.value.path).toBe('/')
    expect(state.isNavigating.value).toBe(false)
    expect(state.isLoading.value).toBe(true)
  })
})
