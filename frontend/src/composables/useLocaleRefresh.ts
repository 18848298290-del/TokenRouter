import { onScopeDispose } from 'vue'

// useLocaleRefresh.ts registers a locale-change listener that refreshes translated display
// data within a Vue scope. Refresh failures are logged while the page continues owning its
// form drafts, and scope disposal removes the event listener from the browser.

// useLocaleRefresh schedules refresh when the browser emits locale-changed and removes its
// listener on scope disposal. Call it within an active Vue scope; server environments
// return immediately. An already queued refresh is not cancelled by disposal.
export function useLocaleRefresh(refresh: () => Promise<unknown> | void): void {
  if (typeof window === 'undefined') return
  let active = true
  // listener queues the supplied refresh after an active locale-change event. The promise
  // chain catches synchronous throws and rejected refreshes for logging; work already
  // queued may still run after scope disposal.
  const listener = () => {
    if (!active) return
    Promise.resolve()
      .then(refresh)
      // Refresh failure is reported through the console so a locale-change event does not
      // produce an unhandled promise rejection.
      .catch((error) => {
        console.warn('Failed to refresh display data after locale change', error)
      })
  }
  window.addEventListener('locale-changed', listener)
  // Disable future listener work before unregistering it; already queued promise callbacks
  // retain their separate asynchronous execution lifetime.
  onScopeDispose(() => {
    active = false
    window.removeEventListener('locale-changed', listener)
  })
}
