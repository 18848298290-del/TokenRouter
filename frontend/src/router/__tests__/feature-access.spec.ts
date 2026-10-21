import { beforeEach, describe, expect, it, vi } from 'vitest'
import { appStore, authStore, getRouter, resetRouterHarness, runGuard } from './routerHarness'

// feature-access.spec.ts checks optional-feature access through registered
// routes and the installed application guard. Public settings distinguish
// unavailable configuration from a loaded false flag, while affiliate access
// requires a positive flag even when other feature settings remain unknown.

/** FeatureRouteCase identifies a destination, its switch, and its disabled redirect. */
interface FeatureRouteCase {
  path: string // Registered feature route path.
  flag: string // Public settings boolean key.
  target: string // Disabled feature redirect path.
  isAdmin: boolean // Current visitor administrator status.
}

// featureRoutes pairs each registered feature destination with the public
// setting that gates it and the redirect used when that feature is disabled.
const featureRoutes: FeatureRouteCase[] = [
  { path: '/purchase', flag: 'payment_enabled', target: '/dashboard', isAdmin: false },
  { path: '/admin/orders', flag: 'payment_enabled', target: '/admin/dashboard', isAdmin: true },
  {
    path: '/admin/risk-control',
    flag: 'risk_control_enabled',
    target: '/admin/settings',
    isAdmin: true,
  },
  { path: '/team', flag: 'team_enabled', target: '/dashboard', isAdmin: false },
  { path: '/admin/teams', flag: 'team_enabled', target: '/admin/settings', isAdmin: true },
  { path: '/usage-ranking', flag: 'usage_ranking_enabled', target: '/dashboard', isAdmin: false },
  { path: '/creative', flag: 'creative_enabled', target: '/dashboard', isAdmin: false },
]

/**
 * createDeferred returns a promise whose fulfillment is controlled by its caller.
 * Resolve it during the test to release the settings request and await navigation
 * before asserting the guard's final decision.
 */
function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

beforeEach(async () => {
  await resetRouterHarness()
  authStore.isAuthenticated = true
})

describe('feature route guard', () => {
  // The first purchase navigation arrives before public settings are available.
  // Navigation waits for the pending settings request and uses the enabled
  // response when it arrives.
  it('waits for the first public-settings request before deciding payment access', async () => {
    const deferred = createDeferred<{ payment_enabled: boolean }>()
    appStore.fetchPublicSettings.mockImplementation(async () => {
      const settings = await deferred.promise
      appStore.cachedPublicSettings = settings
      appStore.publicSettingsLoaded = true
      return settings
    })
    const { navigation, next } = runGuard('/purchase')
    await vi.waitFor(() => expect(appStore.fetchPublicSettings).toHaveBeenCalledTimes(1))
    expect(next).not.toHaveBeenCalled()
    deferred.resolve({ payment_enabled: true })
    await navigation
    expect(next).toHaveBeenCalledExactlyOnceWith()
  })

  // Public settings may fail to load while an authenticated visitor opens a
  // feature. An unknown flag allows navigation because configuration failure
  // does not establish that the feature is disabled.
  it.each(featureRoutes)(
    'allows $path when settings are unavailable',
    async ({ path, isAdmin }) => {
      authStore.isAdmin = isAdmin
      const { navigation, next } = runGuard(path)
      await navigation
      expect(appStore.fetchPublicSettings).toHaveBeenCalledTimes(1)
      expect(appStore.publicSettingsLoaded).toBe(false)
      expect(next).toHaveBeenCalledExactlyOnceWith()
    },
  )

  // A public-settings request can reject rather than return an unavailable
  // result. The guard reports the failure and still permits a feature whose
  // enabled state remains unknown.
  it('allows access after a rejected public-settings request', async () => {
    const warning = vi.spyOn(console, 'warn').mockImplementation(() => {})
    const failure = new Error('Settings unavailable')
    appStore.fetchPublicSettings.mockRejectedValue(failure)
    try {
      const { navigation, next } = runGuard('/purchase')
      await navigation
      expect(warning).toHaveBeenCalledWith('Failed to load public settings in route guard', failure)
      expect(next).toHaveBeenCalledExactlyOnceWith()
    } finally {
      warning.mockRestore()
    }
  })

  // Loaded false flags disable their registered feature destinations for
  // authenticated visitors. The redirect depends on the feature and
  // administrator role, and cached settings prevent another request.
  it.each(featureRoutes)(
    'redirects disabled $path to $target',
    async ({ path, flag, target, isAdmin }) => {
      authStore.isAdmin = isAdmin
      appStore.publicSettingsLoaded = true
      appStore.cachedPublicSettings = { [flag]: false }
      const { navigation, next } = runGuard(path)
      await navigation
      expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
      expect(next).toHaveBeenCalledExactlyOnceWith(target)
    },
  )

  // Enabled feature flags grant authenticated visitors access to each
  // registered destination. These positive controls verify that the route's
  // metadata is not rejected unconditionally by the guard.
  it.each(featureRoutes)('allows enabled $path', async ({ path, flag, isAdmin }) => {
    authStore.isAdmin = isAdmin
    appStore.publicSettingsLoaded = true
    appStore.cachedPublicSettings = { [flag]: true }
    const { navigation, next } = runGuard(path)
    await navigation
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
    expect(next).toHaveBeenCalledExactlyOnceWith()
  })

  // Loaded settings may omit an optional flag during configuration evolution.
  // An absent value remains distinct from an explicit false value for every
  // ordinary optional feature.
  it.each(featureRoutes)(
    'allows $path when its loaded flag is absent',
    async ({ path, isAdmin }) => {
      authStore.isAdmin = isAdmin
      appStore.publicSettingsLoaded = true
      appStore.cachedPublicSettings = {}
      const { navigation, next } = runGuard(path)
      await navigation
      expect(next).toHaveBeenCalledExactlyOnceWith()
    },
  )

  // Administrators can visit user feature pages as well as administrator pages.
  // Disabled user features still return them to administrator settings rather
  // than permitting access based on role.
  it.each(['/team', '/usage-ranking', '/creative'])(
    'redirects administrators from disabled %s',
    async (path) => {
      authStore.isAdmin = true
      appStore.publicSettingsLoaded = true
      appStore.cachedPublicSettings = {
        team_enabled: false,
        usage_ranking_enabled: false,
        creative_enabled: false,
      }
      const { navigation, next } = runGuard(path)
      await navigation
      expect(next).toHaveBeenCalledExactlyOnceWith('/admin/settings')
    },
  )

  // Affiliate pages require a positive cached flag for user and administrator
  // routes. Unknown and false settings redirect immediately, and this feature
  // does not request public settings itself.
  it.each([
    ['/affiliate', false, undefined, '/dashboard'],
    ['/affiliate', false, false, '/dashboard'],
    ['/affiliate', false, true, undefined],
    ['/admin/affiliates/invites', true, undefined, '/admin/settings'],
    ['/admin/affiliates/invites', true, false, '/admin/settings'],
    ['/admin/affiliates/invites', true, true, undefined],
  ])('checks affiliate %s administrator=%s enabled=%s', async (path, isAdmin, enabled, target) => {
    authStore.isAdmin = isAdmin
    appStore.cachedPublicSettings = { affiliate_enabled: enabled }
    const { navigation, next } = runGuard(path)
    await navigation
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
    expect(next.mock.calls).toEqual(target ? [[target]] : [[]])
  })

  // Authentication and administrator checks precede optional-feature loading
  // for restricted pages. Rejected visitors receive their permission redirect
  // before the guard requests settings or considers feature availability.
  it.each([
    [false, false, '/purchase'],
    [true, false, '/admin/risk-control'],
  ])(
    'checks permissions before feature settings authenticated=%s administrator=%s',
    async (authenticated, isAdmin, path) => {
      authStore.isAuthenticated = authenticated
      authStore.isAdmin = isAdmin
      const { navigation, next } = runGuard(path)
      await navigation
      expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
      expect(next).toHaveBeenCalledExactlyOnceWith(
        authenticated ? '/dashboard' : { path: '/login', query: { redirect: path } },
      )
    },
  )

  // Request diagnostics have an administrator destination and no registered
  // user destination. Ordinary users are rejected by that record while
  // administrators can enter the same diagnostic page.
  it('registers request diagnostics as administrator-only', async () => {
    expect(
      getRouter()
        .getRoutes()
        .find((route) => route.path === '/requests'),
    ).toBeUndefined()
    expect(getRouter().resolve('/admin/requests').meta.requiresAdmin).toBe(true)
    const blocked = runGuard('/admin/requests')
    await blocked.navigation
    expect(blocked.next).toHaveBeenCalledExactlyOnceWith('/dashboard')
    authStore.isAdmin = true
    const allowed = runGuard('/admin/requests')
    await allowed.navigation
    expect(allowed.next).toHaveBeenCalledExactlyOnceWith()
  })
})
