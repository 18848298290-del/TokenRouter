import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import subscriptionsAPI from '@/api/subscriptions'
import type { UserSubscription } from '@/api/subscriptions.types'

// subscriptions.ts caches active subscriptions and manages periodic refreshes for
// authenticated views. Ordinary reads reuse fresh data or join an active request; forced
// reads start another request and advance a module-level generation shared by store
// instances.
//
// Each response retains its own result for its caller, but only the latest generation
// publishes subscription state. Clearing the store advances the generation so older
// responses cannot restore cleared data. Promise identity controls loading cleanup
// independently from publication, allowing an older completion to leave a newer request
// active. Polling owns one interval and delegates failures to the same request path.

// CACHE_TTL_MS sets the sixty-second freshness window used by ordinary reads before
// requesting another subscription list.
const CACHE_TTL_MS = 60_000
// requestGeneration is shared across store instances and invalidates publication when a
// newer request starts or a store clears its data.
let requestGeneration = 0

// useSubscriptionStore exposes cached active subscriptions and their polling controls to
// Pinia consumers. Authentication owners start polling and call clear when the session
// ends to invalidate pending publication.
export const useSubscriptionStore = defineStore('subscriptions', () => {
  // Subscription refs retain displayed records, request activity, and the timestamp used
  // to decide whether ordinary reads can reuse cached data.
  const activeSubscriptions = ref<UserSubscription[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const lastFetchedAt = ref<number | null>(null)

  // activePromise lets ordinary callers share a pending request; forced calls replace
  // this owner so older completions cannot clear newer loading state.
  let activePromise: Promise<UserSubscription[]> | null = null

  // pollerInterval owns the single periodic refresh timer, released when authentication
  // clears the store or the caller stops polling.
  let pollerInterval: ReturnType<typeof setInterval> | null = null

  // hasActiveSubscriptions derives the presence of usable subscriptions from the current
  // displayed records for account consumers.
  const hasActiveSubscriptions = computed(() => activeSubscriptions.value.length > 0)

  // fetchActiveSubscriptions returns cached data within sixty seconds or obtains a
  // server response. Ordinary reads join an active promise, while force starts another
  // request and increments the shared generation. Only the latest generation publishes
  // data, although each caller still receives its own response. Promise identity decides
  // which completion clears loading. Request errors propagate after logging, and
  // clearing the store invalidates publication without cancelling transport.
  async function fetchActiveSubscriptions(force = false): Promise<UserSubscription[]> {
    const now = Date.now()
    // Ordinary reads reuse the displayed subscriptions during their freshness window,
    // avoiding another request until expiry or explicit invalidation.
    if (!force && loaded.value && lastFetchedAt.value && now - lastFetchedAt.value < CACHE_TTL_MS) {
      return activeSubscriptions.value
    }
    // Ordinary concurrent reads join the active request; force deliberately bypasses
    // this owner and starts a new publication generation.
    if (activePromise && !force) {
      return activePromise
    }

    const currentGeneration = ++requestGeneration
    loading.value = true
    const requestPromise = subscriptionsAPI
      .getActiveSubscriptions()
      .then((data) => {
        // A newer request or clear operation invalidates this response for shared state,
        // while the caller still receives its own resolved data.
        if (currentGeneration === requestGeneration) {
          activeSubscriptions.value = data
          loaded.value = true
          lastFetchedAt.value = Date.now()
        }
        return data
      })
      .catch((error) => {
        console.error('Failed to fetch active subscriptions:', error)
        throw error
      })
      .finally(() => {
        // An older request may settle after replacement, so only the promise that still
        // owns loading can release it.
        if (activePromise === requestPromise) {
          loading.value = false
          activePromise = null
        }
      })

    activePromise = requestPromise

    return activePromise
  }

  // startPolling starts one five-minute subscription refresh interval when none is
  // active. Each tick forces a refresh and logs failures; the authentication owner calls
  // stopPolling or clear to release it.
  function startPolling() {
    if (pollerInterval) return

    pollerInterval = setInterval(
      () => {
        fetchActiveSubscriptions(true).catch((error) => {
          console.error('Subscription polling failed:', error)
        })
      },
      5 * 60 * 1000,
    )
  }

  // stopPolling releases the subscription refresh interval and clears its handle.
  // Repeated calls after cleanup do nothing. Cached records remain available, and
  // requests already in flight continue under their existing publication generation.
  function stopPolling() {
    if (pollerInterval) {
      // Release the polling timer before clearing its handle, allowing a later
      // authenticated session to establish a fresh interval.
      clearInterval(pollerInterval)
      pollerInterval = null
    }
  }

  // clear invalidates pending publication, removes cached subscriptions, and stops the
  // polling interval. Advancing the module-level generation first prevents older
  // responses from restoring the cleared records. The active promise is detached, but
  // its transport continues and its caller can still receive the response. Loading
  // retains its current value because detached completion no longer owns cleanup; a
  // subsequent request can acquire and release that state through its own promise.
  function clear() {
    // Advance the publication generation before clearing visible data so responses
    // already in flight cannot repopulate the cleared subscription list.
    requestGeneration++
    activePromise = null
    activeSubscriptions.value = []
    loaded.value = false
    lastFetchedAt.value = null
    stopPolling()
  }

  // invalidateCache removes the freshness timestamp so the next idle read requests data.
  // Existing subscription records remain visible, and an active request can still be
  // joined by an ordinary caller.
  function invalidateCache() {
    lastFetchedAt.value = null
  }

  return {
    activeSubscriptions,
    loading,
    hasActiveSubscriptions,
    fetchActiveSubscriptions,
    startPolling,
    stopPolling,
    clear,
    invalidateCache,
  }
})
