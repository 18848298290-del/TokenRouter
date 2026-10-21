import { config } from '@vue/test-utils'
import { vi } from 'vitest'

// setup.ts installs browser API doubles and shared Vue Test Utils defaults for Vitest.
// Missing storage, idle callbacks, and media queries receive deterministic
// implementations, while observer constructors are replaced with spies so component
// lifecycle tests can run in jsdom.

// createMemoryStorage creates an independent in-memory implementation of browser
// Storage. Values are stringified on assignment, missing keys return null, and key
// indexes follow the current Map insertion order for isolated test persistence.
function createMemoryStorage(): Storage {
  const values = new Map<string, string>()

  return {
    // length reports the current number of stored entries for browser-storage consumers.
    get length() {
      return values.size
    },
    // clear removes every entry from this test-owned browser storage instance.
    clear() {
      values.clear()
    },
    // getItem returns the stored string or null when the requested key is absent.
    // Map membership distinguishes a stored empty string from absence, so consumers can
    // treat null as a missing value.
    getItem(key: string) {
      return values.has(key) ? values.get(key)! : null
    },
    // key returns the key at an index in the Map's current insertion order. Each call
    // builds a fresh key array and returns null when the indexed entry is unavailable.
    key(index: number) {
      return Array.from(values.keys())[index] ?? null
    },
    // removeItem deletes the supplied key from this test-owned storage instance.
    removeItem(key: string) {
      values.delete(key)
    },
    // setItem stores a string representation under the supplied browser storage key.
    setItem(key: string, value: string) {
      values.set(key, String(value))
    },
  }
}

// Some test runtimes expose storage without working methods; install isolated memory
// storage when the global API is absent or unusable.
if (
  typeof globalThis.localStorage === 'undefined' ||
  typeof globalThis.localStorage.getItem !== 'function'
) {
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: createMemoryStorage(),
  })
}

// Share the replacement storage with window when its native stub is unusable, keeping
// browser and global consumers on one test session.
if (typeof window !== 'undefined' && typeof window.localStorage.getItem !== 'function') {
  Object.defineProperty(window, 'localStorage', {
    configurable: true,
    value: globalThis.localStorage,
  })
}

// Use a short timer when idle callbacks are unavailable so prefetch work can progress
// under jsdom and fake-clock controlled component tests.
if (typeof globalThis.requestIdleCallback === 'undefined') {
  globalThis.requestIdleCallback = ((callback: IdleRequestCallback) => {
    return window.setTimeout(() => callback({ didTimeout: false, timeRemaining: () => 50 }), 1)
  }) as unknown as typeof requestIdleCallback
}

// The cancellation shim clears the timer returned by the idle shim, allowing component
// cleanup to release scheduled test work.
if (typeof globalThis.cancelIdleCallback === 'undefined') {
  globalThis.cancelIdleCallback = ((id: number) => {
    window.clearTimeout(id)
  }) as unknown as typeof cancelIdleCallback
}

// When jsdom lacks matchMedia, every query matches unconditionally. Tests whose
// outcomes depend on viewport size, color preference, or reduced motion replace this
// implementation to supply the query results their scenarios require.
if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
  window.matchMedia = ((query: string) => ({
    matches: true, // Unconditional media query match.
    media: query, // Requested media query text.
    onchange: null, // No initial change handler.
    addListener: vi.fn(), // Legacy listener registration spy.
    removeListener: vi.fn(), // Legacy listener removal spy.
    addEventListener: vi.fn(), // Modern listener registration spy.
    removeEventListener: vi.fn(), // Modern listener removal spy.
    dispatchEvent: vi.fn(), // Media event dispatch spy.
  })) as unknown as typeof window.matchMedia
}

// MockIntersectionObserver provides lifecycle spies for components that register
// intersection observation.
class MockIntersectionObserver {
  observe = vi.fn() // Observed element registration spy.
  disconnect = vi.fn() // Observer resource cleanup spy.
  unobserve = vi.fn() // Observed element removal spy.
}

globalThis.IntersectionObserver = MockIntersectionObserver as unknown as typeof IntersectionObserver

// MockResizeObserver provides lifecycle spies for components that register element
// resize observation.
class MockResizeObserver {
  observe = vi.fn() // Observed element registration spy.
  disconnect = vi.fn() // Observer resource cleanup spy.
  unobserve = vi.fn() // Observed element removal spy.
}

globalThis.ResizeObserver = MockResizeObserver as unknown as typeof ResizeObserver

// Shared Vue Test Utils defaults begin with no global component substitutions;
// individual suites register the stubs required by their fixtures.
config.global.stubs = {}

// The shared test timeout allows ten seconds for asynchronous component work before
// Vitest reports an unresolved test.
vi.setConfig({ testTimeout: 10000 })
