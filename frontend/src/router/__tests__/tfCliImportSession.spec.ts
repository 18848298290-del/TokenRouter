import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Router } from 'vue-router'

// tfCliImportSession.spec.ts checks CLI secret removal during application
// router construction and the first login redirect. Browser history must
// capture the cleaned destination, including its query string, before
// authentication stores the intended route in a redirect parameter.

// authStore models an anonymous browser whose first protected navigation must
// redirect to login after the router has initialized its CLI import session.
const authStore = vi.hoisted(() => ({
  checkAuth: vi.fn(), // Browser session restoration spy.
  isAuthenticated: false, // Anonymous browser authentication status.
  isAdmin: false, // Anonymous browser administrator status.
  hasPendingAuthSession: false, // External authentication completion status.
}))

// appStore supplies the public configuration read during title and permission
// checks so the integration test can traverse the actual login redirect.
const appStore = vi.hoisted(() => ({
  siteName: 'TokenRouter', // Document title site suffix.
  backendModeEnabled: false, // Backend access restriction status.
  cachedPublicSettings: null as null | Record<string, unknown>, // Cached public settings response.
  publicSettingsLoaded: false, // Successful settings load status.
  fetchPublicSettings: vi.fn(), // Public settings request spy.
}))

vi.mock('@/stores/auth', () => ({ useAuthStore: () => authStore }))
vi.mock('@/stores/app', () => ({ useAppStore: () => appStore }))
vi.mock('@/stores/adminSettings', () => ({
  useAdminSettingsStore: () => ({ customMenuItems: [] }),
}))
vi.mock('@/composables/useNavigationLoading', () => ({
  useNavigationLoadingState: () => ({
    startNavigation: vi.fn(),
    endNavigation: vi.fn(),
    isLoading: { value: false },
  }),
}))
vi.mock('@/composables/useRoutePrefetch', () => ({
  useRoutePrefetch: () => ({
    triggerPrefetch: vi.fn(),
    cancelPendingPrefetch: vi.fn(),
    resetPrefetchState: vi.fn(),
  }),
}))
vi.mock('@/router/title', () => ({
  resolveRouteDocumentTitle: () => 'TokenRouter',
}))

// activeRouter retains the router whose web-history listeners must be released
// after each case before another application instance observes the browser URL.
let activeRouter: Router | undefined

describe('router tf CLI session initialization', () => {
  beforeEach(() => {
    vi.resetModules()
  })
  afterEach(async () => {
    const { clearTfCliImportSession } = await import('@/utils/tfCliImport')
    clearTfCliImportSession()
    activeRouter?.options.history.destroy()
    activeRouter = undefined
    window.history.replaceState({}, '', '/')
    vi.restoreAllMocks()
  })

  // CLI links arrive with valid or malformed one-time secrets in their
  // fragments. Router history and the later login redirect both capture the
  // cleaned destination before exposing it elsewhere.
  it.each([
    ['/keys?scope=personal', '#tfcli=1.43110.AAECAwQFBgcICQoLDA0ODw'],
    ['/keys?scope=personal', '#tfcli=malformed-secret'],
    ['/dashboard?scope=personal', '#tfcli=1.43110.AAECAwQFBgcICQoLDA0ODw'],
  ])('clears the secret before history captures %s with %s', async (path, fragment) => {
    vi.spyOn(window, 'scrollTo').mockImplementation(() => {})
    window.history.replaceState({}, '', `${path}${fragment}`)

    const { default: router } = await import('@/router')
    activeRouter = router
    expect(window.location.hash).toBe('')
    expect(router.options.history.location).toBe(path)

    await router.push(router.options.history.location)
    await router.isReady()

    expect(router.currentRoute.value.path).toBe('/login')
    expect(router.currentRoute.value.query.redirect).toBe(path)
    expect(JSON.stringify(router.currentRoute.value.query)).not.toContain('AAECAwQFBgcICQoLDA0ODw')
  })
})
