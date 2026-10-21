import type { Router, RouteLocationNormalized } from 'vue-router'
import { useNavigationLoadingState } from '@/composables/useNavigationLoading'

// navigationLoading.ts associates each router destination with a navigation-progress
// identifier. Vue Router can finish an older navigation after a newer one starts, so
// completion is forwarded with the identifier captured for that destination.
//
// The shared loading state decides whether the completed identifier still owns the
// visible indicator. A WeakMap keeps this association without retaining destinations
// after the router releases them. The same completion handler covers successful
// navigation, cancellation, and router errors. Installation precedes the access guard so
// redirects and asynchronous permission checks also start progress before they can
// finish or replace a navigation.

// @project-doc docs/architecture/frontend_ui_conventions.md#loading_feedback
// installNavigationLoading registers progress lifecycle handlers for a router before
// access guards run. Each destination receives an identifier from shared loading state
// and keeps it in a WeakMap. Successful, cancelled, and failed navigation all report
// completion using that captured identifier. The shared state can therefore reject stale
// completion events when a newer destination owns progress. Call this once for each
// application router instance.
export function installNavigationLoading(router: Router): void {
  const loading = useNavigationLoadingState()
  const navigationIds = new WeakMap<RouteLocationNormalized, number>()

  router.beforeEach((to) => {
    navigationIds.set(to, loading.startNavigation())
  })

  // finish forwards a destination's recorded progress identifier to shared loading
  // state. Destinations that never reached the start hook have no identifier and leave
  // the current progress unchanged.
  const finish = (to: RouteLocationNormalized): void => {
    const id = navigationIds.get(to)
    if (id !== undefined) loading.endNavigation(id)
  }

  router.afterEach(finish)
  router.onError((_error, to) => finish(to))
}
