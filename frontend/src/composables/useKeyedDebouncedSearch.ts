import { getCurrentInstance, onUnmounted } from 'vue'
import { SEARCH_DEBOUNCE_MS } from '@/constants/ui'

// useKeyedDebouncedSearch.ts debounces searches independently by caller key and rejects
// results from superseded searches. Each key owns a timer, an abort controller once
// dispatched, and a version identifying the latest trigger. Replacing one key leaves
// timers and requests for other keys intact.
//
// Cancellation asks the search implementation to stop through its signal, while completion
// checks also prevent publication when the implementation ignores cancellation. Controller
// identity protects newer requests from older finally handlers. Component teardown clears
// all keys when created during setup; callers creating this helper outside a component own
// cleanup themselves.

// KeyedDebouncedSearchContext identifies the search owner and supplies its cancellation
// signal.
export interface KeyedDebouncedSearchContext {
  key: string // caller-owned search slot identifier
  // signal belongs to one dispatched search and becomes aborted when its key is replaced
  // or cleared.
  signal: AbortSignal // current search cancellation signal
}

// UseKeyedDebouncedSearchOptions supplies debounce timing, asynchronous search, and
// publication callbacks.
interface UseKeyedDebouncedSearchOptions<T> {
  // delay is measured in milliseconds and defaults to the shared search debounce duration
  // when omitted.
  delay?: number // debounce delay in milliseconds
  // search may honor signal to stop transport work; publication still checks cancellation
  // after its returned promise settles.
  search: (keyword: string, context: KeyedDebouncedSearchContext) => Promise<T> // asynchronous keyword search implementation
  // onSuccess receives the current key's accepted result after both controller
  // cancellation and request generation checks pass.
  onSuccess: (key: string, result: T) => void // current successful result consumer
  // onError receives errors from current un-aborted requests; obsolete errors are
  // discarded before this optional callback runs.
  onError?: (key: string, error: unknown) => void // current failed request consumer
}

// useKeyedDebouncedSearch creates independent debounce and request state for each string
// key. It defaults delay to SEARCH_DEBOUNCE_MS and calls success or error handlers only
// for the currently active request of that key.
//
// Each trigger advances the version and aborts prior work before scheduling replacement
// dispatch. Completion checks both abort state and version even if the search
// implementation ignores its signal. Setup-created instances clear automatically on
// unmount; other callers invoke clearKey or clearAll when their owners disappear.
export function useKeyedDebouncedSearch<T>(options: UseKeyedDebouncedSearchOptions<T>) {
  const delay = options.delay ?? SEARCH_DEBOUNCE_MS
  // timers, controllers, and versions belong to independent search keys. Timer dispatch
  // creates a controller, and versions reject completions superseded by a later trigger.
  const timers = new Map<string, ReturnType<typeof setTimeout>>()
  const controllers = new Map<string, AbortController>()
  const versions = new Map<string, number>()

  // clearKey removes a key's pending timer, aborts its current request, and drops its
  // version. Search implementations may continue running, but the aborted controller
  // prevents their completion from publishing.
  const clearKey = (key: string) => {
    const timer = timers.get(key)
    if (timer) {
      clearTimeout(timer)
      timers.delete(key)
    }

    const controller = controllers.get(key)
    if (controller) {
      controller.abort()
      controllers.delete(key)
    }

    versions.delete(key)
  }

  // clearAll clears every key represented by timers, controllers, or versions. It includes
  // completed keys still holding a version, and callers outside component setup use it for
  // disposal.
  const clearAll = () => {
    const allKeys = new Set<string>([...timers.keys(), ...controllers.keys(), ...versions.keys()])

    allKeys.forEach((key) => clearKey(key))
  }

  // trigger replaces pending and in-flight work for one key, then dispatches its latest
  // keyword after the configured delay. Other keys remain independently scheduled.
  // Aborting the old controller also marks completions invalid when the search
  // implementation keeps running.
  //
  // The delayed callback records its version and controller before awaiting search. Both
  // successful and failed completions check those values before publication, and finally
  // removes a controller only when it still belongs to that callback.
  const trigger = (key: string, keyword: string) => {
    // Advance the key's generation before cancelling prior work so asynchronous
    // completions cannot publish for the superseded keyword.
    const nextVersion = (versions.get(key) ?? 0) + 1
    versions.set(key, nextVersion)

    const existingTimer = timers.get(key)
    if (existingTimer) {
      clearTimeout(existingTimer)
      timers.delete(key)
    }

    const inFlight = controllers.get(key)
    if (inFlight) {
      inFlight.abort()
      controllers.delete(key)
    }

    const timer = setTimeout(async () => {
      timers.delete(key)

      const controller = new AbortController()
      controllers.set(key, controller)
      const requestVersion = versions.get(key)

      try {
        const result = await options.search(keyword, { key, signal: controller.signal })
        // Recheck abort state and generation after awaiting search because implementations
        // that ignore AbortSignal can still resolve with stale results.
        if (controller.signal.aborted) return
        if (versions.get(key) !== requestVersion) return
        options.onSuccess(key, result)
        // Errors from replaced searches are ignored for the same reason as stale results:
        // their owner has already requested a newer keyword.
      } catch (error) {
        if (controller.signal.aborted) return
        if (versions.get(key) !== requestVersion) return
        options.onError?.(key, error)
      } finally {
        // An older completion may overlap a replacement dispatch, so only its own
        // controller may be removed from the map.
        if (controllers.get(key) === controller) {
          controllers.delete(key)
        }
      }
    }, delay)

    timers.set(key, timer)
  }

  if (getCurrentInstance()) {
    onUnmounted(() => {
      clearAll()
    })
  }

  return {
    trigger,
    clearKey,
    clearAll,
  }
}
