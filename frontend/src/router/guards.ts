import type { Router } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAppStore } from '@/stores/app'
import { useAdminSettingsStore } from '@/stores/adminSettings'
import { useRoutePrefetch } from '@/composables/useRoutePrefetch'
import { getSetupStatus } from '@/api/setup'
import { resolveCompletedSetupRedirectPath } from './setupRedirect'
import { resolveRouteDocumentTitle } from './title'

// guards.ts installs access checks, successful-navigation prefetching, and chunk-load
// recovery on an application router. Session restoration runs once per installation
// before authentication and role checks. Public settings are fetched for protected
// feature routes when their availability is still unknown.
//
// Loaded false feature flags redirect to the appropriate dashboard or settings page,
// while failed settings reads leave those features accessible. Affiliate access
// separately requires a true cached flag. Backend mode limits public destinations and
// blocks non-administrator protected navigation, with a login exception that prevents
// redirect loops. A per-installation prefetch handle is created after the first
// successful navigation. Chunk failures use a session timestamp to limit reload
// attempts.

// installRouteGuards attaches access, prefetch, and error handlers once to the supplied
// router. Call it after navigation-progress installation and before starting navigation.
// The installation retains its own first-navigation flag and lazily created prefetch
// controller. Access checks restore authentication before resolving the title, then
// apply public-route, role, feature, and backend-mode rules in order. Successful
// navigation schedules prefetching, while chunk failures may reload the browser using a
// session-scoped retry timestamp.
export function installRouteGuards(router: Router): void {
  // The installation owns one authentication-restoration marker and one lazy prefetch
  // controller, preventing repeated startup work during subsequent navigation.
  let authInitialized = false
  let routePrefetch: ReturnType<typeof useRoutePrefetch> | null = null
  // Backend-mode general destinations use prefix matching, callbacks use exact matching,
  // and pending registration destinations additionally require an incomplete
  // authentication session.
  const BACKEND_MODE_ALLOWED_PATHS = [
    '/login',
    '/key-usage',
    '/setup',
    '/models',
    '/payment/result',
    '/payment/airwallex',
    '/legal',
  ]
  const BACKEND_MODE_CALLBACK_PATHS = [
    '/auth/callback',
    '/auth/oauth/callback',
    '/auth/linuxdo/callback',
    '/auth/dingtalk/callback',
    '/auth/dingtalk/email-completion',
    '/auth/oidc/callback',
    '/auth/wechat/callback',
    '/auth/wechat/payment/callback',
  ]
  const BACKEND_MODE_PENDING_AUTH_PATHS = ['/register', '/email-verify']

  // isBackendModePublicRouteAllowed reports whether backend mode admits a destination.
  // General allowed paths use prefix matching, callback paths require exact matching,
  // and registration or verification additionally require pending authentication.
  function isBackendModePublicRouteAllowed(path: string, hasPendingAuthSession: boolean): boolean {
    if (
      BACKEND_MODE_ALLOWED_PATHS.some(
        (allowedPath) => path === allowedPath || path.startsWith(allowedPath),
      )
    ) {
      return true
    }

    if (BACKEND_MODE_CALLBACK_PATHS.some((callbackPath) => path === callbackPath)) {
      return true
    }

    if (
      hasPendingAuthSession &&
      BACKEND_MODE_PENDING_AUTH_PATHS.some((allowedPath) => path === allowedPath)
    ) {
      return true
    }

    return false
  }

  router.beforeEach(async (to, _from, next) => {
    const authStore = useAuthStore()
    // Restore browser authentication before title and permission evaluation, then retain
    // the initialization marker for later navigations on this router.
    if (!authInitialized) {
      authStore.checkAuth()
      authInitialized = true
    }
    const appStore = useAppStore()
    const adminSettingsStore = useAdminSettingsStore()
    const customMenuItems = [
      ...(appStore.cachedPublicSettings?.custom_menu_items ?? []),
      ...(authStore.isAdmin ? adminSettingsStore.customMenuItems : []),
    ]
    document.title = resolveRouteDocumentTitle(to, appStore.siteName, customMenuItems)
    const requiresAuth = to.meta.requiresAuth !== false // Default to true
    const requiresAdmin = to.meta.requiresAdmin === true

    if (to.path === '/setup') {
      try {
        const status = await getSetupStatus()
        if (!status.needs_setup) {
          next(resolveCompletedSetupRedirectPath(authStore.isAuthenticated, authStore.isAdmin))
          return
        }
      } catch {
        // An unavailable setup endpoint leaves installation status unknown, so allow
        // the setup page to load and present its own installation flow.
      }
    }
    if (!requiresAuth) {
      if (authStore.isAuthenticated && (to.path === '/login' || to.path === '/register')) {
        // Backend mode blocks non-administrator dashboards, so allowing the login page
        // here prevents authenticated users from entering a redirect loop.
        if (appStore.backendModeEnabled && !authStore.isAdmin) {
          next()
          return
        }
        next(authStore.isAdmin ? '/admin/dashboard' : '/dashboard')
        return
      }
      if (appStore.backendModeEnabled && !authStore.isAuthenticated) {
        const isAllowed = isBackendModePublicRouteAllowed(to.path, authStore.hasPendingAuthSession)
        if (!isAllowed) {
          next('/login')
          return
        }
      }
      next()
      return
    }
    if (!authStore.isAuthenticated) {
      next({
        path: '/login',
        query: { redirect: to.fullPath }, // Save intended destination
      })
      return
    }
    if (requiresAdmin && !authStore.isAdmin) {
      next('/dashboard')
      return
    }
    // Initial navigation can precede the shell settings read, so feature routes await
    // settings before interpreting loaded false as disabled.
    const requiresPublicFeature =
      to.meta.requiresPayment ||
      to.meta.requiresRiskControl ||
      to.meta.requiresTeam ||
      to.meta.requiresUsageRanking ||
      to.meta.requiresCreative
    if (requiresPublicFeature && !appStore.publicSettingsLoaded) {
      try {
        await appStore.fetchPublicSettings()
      } catch (error) {
        console.warn('Failed to load public settings in route guard', error)
      }
    }
    // Unknown settings leave these feature routes accessible; redirects require a
    // successfully loaded configuration with an explicitly false feature value.
    if (
      to.meta.requiresPayment &&
      appStore.publicSettingsLoaded &&
      appStore.cachedPublicSettings?.payment_enabled === false
    ) {
      next(authStore.isAdmin ? '/admin/dashboard' : '/dashboard')
      return
    }

    if (
      to.meta.requiresRiskControl &&
      appStore.publicSettingsLoaded &&
      appStore.cachedPublicSettings?.risk_control_enabled === false
    ) {
      next(authStore.isAdmin ? '/admin/settings' : '/dashboard')
      return
    }

    if (
      to.meta.requiresTeam &&
      appStore.publicSettingsLoaded &&
      appStore.cachedPublicSettings?.team_enabled === false
    ) {
      next(authStore.isAdmin ? '/admin/settings' : '/dashboard')
      return
    }

    if (
      to.meta.requiresUsageRanking &&
      appStore.publicSettingsLoaded &&
      appStore.cachedPublicSettings?.usage_ranking_enabled === false
    ) {
      next(authStore.isAdmin ? '/admin/settings' : '/dashboard')
      return
    }

    if (
      to.meta.requiresCreative &&
      appStore.publicSettingsLoaded &&
      appStore.cachedPublicSettings?.creative_enabled === false
    ) {
      next(authStore.isAdmin ? '/admin/settings' : '/dashboard')
      return
    }

    if (to.meta.requiresAffiliate) {
      const affiliateEnabled = appStore.cachedPublicSettings?.affiliate_enabled === true
      if (!affiliateEnabled) {
        next(authStore.isAdmin ? '/admin/settings' : '/dashboard')
        return
      }
    }
    if (appStore.backendModeEnabled) {
      if (authStore.isAuthenticated && authStore.isAdmin) {
        next()
        return
      }
      const isAllowed = isBackendModePublicRouteAllowed(to.path, authStore.hasPendingAuthSession)
      if (!isAllowed) {
        next('/login')
        return
      }
    }
    next()
  })

  router.afterEach((to, _from, failure) => {
    // Cancelled and aborted destinations do not schedule prefetching because they did
    // not become the successfully displayed route for this navigation.
    if (failure) return
    if (!routePrefetch) {
      routePrefetch = useRoutePrefetch(router)
    }
    routePrefetch.triggerPrefetch(to)
  })

  router.onError((error) => {
    console.error('Router error:', error)
    const isChunkLoadError =
      error.message?.includes('Failed to fetch dynamically imported module') ||
      error.message?.includes('Loading chunk') ||
      error.message?.includes('Loading CSS chunk') ||
      error.name === 'ChunkLoadError'

    if (isChunkLoadError) {
      const reloadKey = 'chunk_reload_attempted'
      const lastReload = sessionStorage.getItem(reloadKey)
      const now = Date.now()
      // Permit one reload per ten-second session window so stale deployment chunks can
      // recover without an immediate repeated reload loop.
      if (!lastReload || now - parseInt(lastReload) > 10000) {
        sessionStorage.setItem(reloadKey, now.toString())
        console.warn('Chunk load error detected, reloading page to fetch latest version...')
        window.location.reload()
      } else {
        console.error('Chunk load error persists after reload. Please clear browser cache.')
      }
    }
  })
}
