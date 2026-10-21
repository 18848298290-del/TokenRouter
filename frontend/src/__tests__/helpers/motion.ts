import { nextTick } from 'vue'
import { vi } from 'vitest'

// motion.ts supplies browser motion substitutes for jsdom component tests.
// Media-query listeners and computed transition durations share the reduced-motion setting, while frame and transition-end helpers let tests advance
// the component transition lifecycle deliberately.

// mockMotionEnvironment installs matchMedia and computed-style spies with a
// shared reduced-motion setting. Tests restore Vitest mocks during cleanup
// and drive transition completion explicitly because jsdom does not calculate
// browser transition timing.
export function mockMotionEnvironment(initiallyReduced = false) {
  // reduced controls both computed transition timing and media-query
  // matches. The listener set remains available to tests for subscription
  // assertions after preference changes.
  let reduced = initiallyReduced
  const listeners = new Set<(event: MediaQueryListEvent) => void>()
  vi.spyOn(window, 'matchMedia').mockImplementation(
    (query) =>
      ({
        media: query,
        // matches reports reduced motion for queries containing the supported
        // preference.
        get matches() {
          return query.includes('prefers-reduced-motion') && reduced
        },
        // addEventListener registers a callback for preference notifications
        // emitted by reduce.
        addEventListener: (_type: string, callback: () => void) => listeners.add(callback),
        // removeEventListener removes a registered callback from future
        // preference notifications.
        removeEventListener: (_type: string, callback: () => void) => listeners.delete(callback),
        // addListener supports legacy media-query subscriptions through the
        // shared listener set.
        addListener: (callback: () => void) => listeners.add(callback),
        // removeListener removes a legacy subscription from the shared
        // listener set.
        removeListener: (callback: () => void) => listeners.delete(callback),
        onchange: null,
        // dispatchEvent acknowledges synthetic dispatch requests with a
        // successful boolean result. Tests publish preference changes
        // through reduce, which supplies the matches field read by the
        // registered media-query listeners.
        dispatchEvent: () => true,
      }) as MediaQueryList,
  )
  const originalStyle = window.getComputedStyle.bind(window)
  vi.spyOn(window, 'getComputedStyle').mockImplementation((element) => {
    const style = originalStyle(element)
    return new Proxy(style, {
      // get supplies transition timing from the current motion preference.
      // Other properties delegate to the underlying style object, and
      // callable values are bound to that object for CSSStyleDeclaration
      // methods.
      get(target, key) {
        // Use a nonzero synthetic transition duration for ordinary motion so
        // Vue installs completion listeners that tests can drive through
        // transitionend.
        if (key === 'transitionDuration') return reduced ? '0s' : '0.22s'
        if (key === 'transitionDelay' || key === 'animationDuration' || key === 'animationDelay')
          return '0s'
        const value = Reflect.get(target, key)
        return typeof value === 'function' ? value.bind(target) : value
      },
    })
  })
  return {
    // reduce publishes a new reduced-motion preference to registered query
    // listeners. Tests call it after mounting subscribers, and each listener
    // receives the matches field consumed by VueUse.
    reduce(value: boolean) {
      reduced = value
      // The preference changes before listeners run, so subscribers reading
      // either the event or matches getter observe the same reduced-motion
      // value.
      listeners.forEach((callback) => callback({ matches: reduced } as MediaQueryListEvent))
    },
    listeners,
  }
}

// nextMotionFrame flushes Vue work around two animation-frame callbacks for
// transition tests. Callers await it after changing visibility so frame-scheduled transition setup has time to run before assertions.
export async function nextMotionFrame() {
  await nextTick()
  await new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
  )
  await nextTick()
}

// finishMotion dispatches a bubbling transition-end event after motion setup
// frames. Callers pass the transitioning element and await the helper before
// asserting Vue state updated by completion handlers.
export async function finishMotion(element: Element) {
  await nextMotionFrame()
  element.dispatchEvent(new Event('transitionend', { bubbles: true }))
  await nextTick()
}
