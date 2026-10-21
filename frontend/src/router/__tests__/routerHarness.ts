import { vi } from 'vitest'
import type { NavigationGuard, Router, RouterOptions } from 'vue-router'

// routerHarness.ts loads the application's route records and captures its
// installed authentication guard for focused navigation assertions. Tests use
// Vue Router's matcher to obtain metadata while store and setup responses
// remain controllable, so permissions are evaluated by the production guard.

// routerHarness retains the current router and installed guard across the mock
// factory and test helpers; resetRouterHarness replaces both before each test.
const routerHarness = vi.hoisted(() => ({
  router: null as Router | null, // Current application router instance.
  guard: null as NavigationGuard | null, // Installed application authentication guard.
}))

// authStore supplies observable authentication state and restoration calls to
// the production guard; every test begins with an unauthenticated browser
// session.
const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(), // Browser session restoration spy.
  isAuthenticated: false, // Current authenticated session status.
  isAdmin: false, // Current administrator role status.
  hasPendingAuthSession: false, // Incomplete external authentication status.
}))

// appStore controls public configuration independently from authentication so
// feature availability and backend mode can be exercised with the same routes.
const appStore = vi.hoisted(() => ({
  siteName: 'TokenRouter', // Configured document title suffix.
  backendModeEnabled: false, // Administrator-only backend mode status.
  publicSettingsLoaded: false, // Successful public settings load status.
  cachedPublicSettings: null as Record<string, unknown> | null, // Cached public settings response.
  fetchPublicSettings: vi.fn(), // Public settings request spy.
}))

// setupStatus controls installation status requests independently of store
// restoration, including rejected requests that still allow the installation
// page.
const setupStatus = vi.hoisted(() => vi.fn())

vi.mock('vue-router', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-router')>()
  return {
    ...actual,
    createWebHistory: actual.createMemoryHistory,
    createRouter: (options: RouterOptions) => {
      const router = actual.createRouter(options)
      const register = router.beforeEach.bind(router)
      vi.spyOn(router, 'beforeEach').mockImplementation((guard) => {
        routerHarness.guard = guard
        return register(guard)
      })
      routerHarness.router = router
      return router
    },
  }
})
vi.mock('@/stores/auth', () => ({ useAuthStore: () => authStore }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))
vi.mock('@/api/setup', () => ({ getSetupStatus: setupStatus }))
vi.mock('@/router/navigationLoading', () => ({ installNavigationLoading: vi.fn() }))
vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({ triggerPrefetch: vi.fn() }),
}))

/**
 * resetRouterHarness creates a fresh application router with unauthenticated stores.
 * Call it before each test so module-scoped authentication initialization and
 * mocked response implementations cannot carry over from earlier navigation cases.
 */
export async function resetRouterHarness(): Promise<void> {
  routerHarness.router?.options.history.destroy()
  // Reset the module cache before importing so each test owns its
  // authentication initialization flag and observes the first session
  // restoration independently.
  vi.resetModules()
  routerHarness.router = null
  routerHarness.guard = null
  authStore.checkAuth.mockReset()
  authStore.isAuthenticated = false
  authStore.isAdmin = false
  authStore.hasPendingAuthSession = false
  appStore.siteName = 'TokenRouter'
  appStore.backendModeEnabled = false
  appStore.publicSettingsLoaded = false
  appStore.cachedPublicSettings = null
  appStore.fetchPublicSettings.mockReset().mockResolvedValue(null)
  setupStatus.mockReset().mockResolvedValue({ needs_setup: true })
  await import('@/router')
}

/**
 * getRouter returns the application router after the harness has initialized.
 * Call resetRouterHarness before requesting it; an uninitialized harness throws
 * so missing setup cannot silently skip route or guard assertions.
 */
export function getRouter(): Router {
  if (!routerHarness.router) throw new Error('Application router has not been initialized')
  return routerHarness.router
}

/**
 * runGuard evaluates the installed authentication guard with resolved route metadata.
 * Callers await navigation before checking its next spy, or retain the pending
 * promise to assert that asynchronous setup or feature checks delay navigation.
 */
export function runGuard(path: string) {
  const router = getRouter()
  const guard = routerHarness.guard
  if (!guard) throw new Error('Application authentication guard was not registered')
  const next = vi.fn()
  const navigation = Promise.resolve(
    guard.call(router, router.resolve(path), router.resolve('/home'), next),
  )
  return { navigation, next }
}

export { authStore, appStore, setupStatus }
