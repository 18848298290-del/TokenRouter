import { beforeEach, describe, expect, it } from 'vitest'
import {
  appStore,
  authStore,
  getRouter,
  resetRouterHarness,
  runGuard,
  setupStatus,
} from './routerHarness'

// guards.spec.ts checks authentication, installation, and backend-mode access
// using the guard installed by the application router. Resolved route records
// supply permission metadata, while controlled stores expose redirect targets
// and session restoration to each navigation assertion.

beforeEach(resetRouterHarness)

describe('application authentication guard', () => {
  // A visitor requests a protected destination with query and fragment state.
  // The login redirect retains that complete destination for the subsequent
  // authenticated visit.
  it.each(['/dashboard?range=week#usage', '/admin/users?page=2', '/missing-route'])(
    'preserves the intended destination for %s',
    async (path) => {
      const { navigation, next } = runGuard(path)
      await navigation
      expect(next).toHaveBeenCalledExactlyOnceWith({ path: '/login', query: { redirect: path } })
    },
  )

  // Public destinations remain reachable before a browser session has been
  // restored. Their registered metadata grants access without redirecting
  // visitors to the login page.
  it.each(['/login', '/home', '/models', '/legal/privacy', '/payment/result'])(
    'allows public route %s',
    async (path) => {
      expect(getRouter().resolve(path).meta.requiresAuth).toBe(false)
      const { navigation, next } = runGuard(path)
      await navigation
      expect(next).toHaveBeenCalledExactlyOnceWith()
    },
  )

  // Signed-in visitors enter authentication forms with either user or
  // administrator roles. Each role returns to its dashboard after the existing
  // session is recognized.
  it.each([
    ['/login', false, '/dashboard'],
    ['/register', false, '/dashboard'],
    ['/login', true, '/admin/dashboard'],
    ['/register', true, '/admin/dashboard'],
  ])('redirects %s for administrator=%s', async (path, isAdmin, target) => {
    authStore.isAuthenticated = true
    authStore.isAdmin = isAdmin
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next).toHaveBeenCalledExactlyOnceWith(target)
  })

  // Authenticated users visit ordinary pages and administrator pages with each
  // role. Registered administrator requirements determine whether navigation
  // proceeds or returns to the user dashboard.
  it.each([
    ['/dashboard', false, undefined],
    ['/dashboard', true, undefined],
    ['/admin/dashboard', false, '/dashboard'],
    ['/admin/users', false, '/dashboard'],
    ['/admin/dashboard', true, undefined],
  ])('enforces %s for administrator=%s', async (path, isAdmin, target) => {
    authStore.isAuthenticated = true
    authStore.isAdmin = isAdmin
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next.mock.calls).toEqual(target ? [[target]] : [[]])
  })

  // The first navigation restores an administrator session from browser
  // storage. Permission checks observe that restored state, and later
  // navigations do not restore it again.
  it('restores authentication once before checking access', async () => {
    authStore.checkAuth.mockImplementation(() => {
      authStore.isAuthenticated = true
      authStore.isAdmin = true
    })
    const first = runGuard('/admin/users')
    await first.navigation
    expect(first.next).toHaveBeenCalledExactlyOnceWith()
    const second = runGuard('/dashboard')
    await second.navigation
    expect(second.next).toHaveBeenCalledExactlyOnceWith()
    expect(authStore.checkAuth).toHaveBeenCalledTimes(1)
  })

  // An unauthenticated protected-page visit still assigns its document title.
  // The resolved route title and current site name reach the browser before
  // redirect completion.
  it('updates the document title before redirecting', async () => {
    appStore.siteName = 'Example Site'
    const { navigation } = runGuard('/missing-route')
    await navigation
    expect(document.title).toBe('404 Not Found - Example Site')
  })
})

describe('setup route access', () => {
  // An initialized installation receives setup visits from guests, users, and
  // administrators. Each session returns to its appropriate entry page even
  // when backend mode is enabled.
  it.each([
    [false, false, '/login'],
    [true, false, '/dashboard'],
    [true, true, '/admin/dashboard'],
  ])(
    'redirects completed setup for authenticated=%s administrator=%s',
    async (authenticated, isAdmin, target) => {
      appStore.backendModeEnabled = true
      authStore.isAuthenticated = authenticated
      authStore.isAdmin = isAdmin
      setupStatus.mockResolvedValue({ needs_setup: false })
      const { navigation, next } = runGuard('/setup')
      await navigation
      expect(next).toHaveBeenCalledExactlyOnceWith(target)
      expect(setupStatus).toHaveBeenCalledTimes(1)
    },
  )

  // Setup remains available while installation is required or its status
  // request fails. Both outcomes allow the installer to recover in backend mode
  // without a login redirect.
  it.each(['required', 'unavailable'])('allows setup when status is %s', async (status) => {
    appStore.backendModeEnabled = true
    if (status === 'unavailable') setupStatus.mockRejectedValue(new Error('Setup unavailable'))
    const { navigation, next } = runGuard('/setup')
    await navigation
    expect(next).toHaveBeenCalledExactlyOnceWith()
  })
})

describe('backend mode public access', () => {
  beforeEach(() => {
    appStore.backendModeEnabled = true
  })

  // Backend mode exposes selected public destinations and identity-provider
  // callbacks to guests. Each listed path resolves to a public route and
  // reaches its registered page.
  it.each([
    '/login',
    '/key-usage',
    '/setup',
    '/models',
    '/legal/privacy',
    '/payment/result',
    '/payment/airwallex',
    '/auth/callback',
    '/auth/oauth/callback',
    '/auth/linuxdo/callback',
    '/auth/oidc/callback',
    '/auth/wechat/callback',
    '/auth/wechat/payment/callback',
    '/auth/dingtalk/callback',
    '/auth/dingtalk/email-completion',
  ])('allows %s', async (path) => {
    expect(getRouter().resolve(path).meta.requiresAuth).toBe(false)
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next).toHaveBeenCalledExactlyOnceWith()
  })

  // Other registered public destinations remain closed to guests in backend
  // mode. These redirects protect the mode restriction independently of each
  // route's public authentication metadata.
  it.each([
    '/home',
    '/forgot-password',
    '/reset-password',
    '/payment/stripe',
    '/payment/stripe-popup',
  ])('blocks %s', async (path) => {
    expect(getRouter().resolve(path).meta.requiresAuth).toBe(false)
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next).toHaveBeenCalledExactlyOnceWith('/login')
  })

  // Registration and verification can continue an incomplete external
  // authentication session. Guests lacking that pending session are redirected
  // to login for both destinations.
  it.each([
    ['/register', false],
    ['/register', true],
    ['/email-verify', false],
    ['/email-verify', true],
  ])('checks pending session for %s pending=%s', async (path, pending) => {
    authStore.hasPendingAuthSession = pending
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next.mock.calls).toEqual(pending ? [[]] : [['/login']])
  })

  // Backend mode treats authenticated administrators and ordinary users
  // differently on protected pages. Login and registration remain usable by
  // ordinary users so blocked dashboards cannot form redirect loops.
  it.each([
    ['/dashboard', false, '/login'],
    ['/admin/dashboard', true, undefined],
    ['/login', false, undefined],
    ['/register', false, undefined],
    ['/key-usage', false, undefined],
    ['/login', true, '/admin/dashboard'],
  ])('checks signed-in %s administrator=%s', async (path, isAdmin, target) => {
    authStore.isAuthenticated = true
    authStore.isAdmin = isAdmin
    const { navigation, next } = runGuard(path)
    await navigation
    expect(next.mock.calls).toEqual(target ? [[target]] : [[]])
  })
})
