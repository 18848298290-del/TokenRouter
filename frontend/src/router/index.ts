import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import { initializeTfCliImportSession } from '@/utils/tfCliImport'
import { installNavigationLoading } from './navigationLoading'
import { installRouteGuards } from './guards'
import { publicRoutes, notFoundRoute } from './routes/public'
import { userRoutes } from './routes/user'
import { adminRoutes } from './routes/admin'

// index.ts assembles the application router and installs navigation feedback
// before access checks. Public, user, and administrator records stay in their
// declared order, followed by the catch-all destination with default protected
// access. Components remain lazy imports owned by each route group.
//
// CLI import fragments contain one-time credentials. Session initialization
// removes them before browser history captures its initial location, so login
// redirects use the cleaned destination. Navigation feedback is registered
// before authentication because redirects and asynchronous access checks also
// need a visible loading lifecycle. The guard installer owns session restoration,
// feature checks, prefetching, and chunk-load recovery for this router instance.

// Sanitize the initial browser URL before history captures it; otherwise a
// subsequent login redirect can copy the one-time secret into its query string.
initializeTfCliImportSession()

// routes defines matcher precedence and keeps the catch-all after every specific application destination.
const routes: RouteRecordRaw[] = [...publicRoutes, ...userRoutes, ...adminRoutes, notFoundRoute]

// router uses browser history and restores saved scroll positions when the user navigates backward.
const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes,
  // scrollBehavior restores the browser's saved position when available. New
  // navigation starts at the top of the page, so route changes do not inherit
  // the previous page's scroll offset.
  scrollBehavior(_to, _from, savedPosition) {
    if (savedPosition) {
      return savedPosition
    }
    return { top: 0 }
  },
})

// Register progress first so authentication redirects and awaiting settings
// participate in the same navigation lifecycle as lazy component loading.
installNavigationLoading(router)
installRouteGuards(router)

export default router
