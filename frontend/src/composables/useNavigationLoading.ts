import { computed, readonly, ref } from 'vue'

// useNavigationLoading.ts tracks router activity and its finishing animation with a
// monotonically increasing navigation identifier. Starting navigation replaces the active
// identifier and enters loading; completion enters a distinct animation phase, and the
// matching animation callback finally returns the indicator to idle.
//
// A stale completion or finishing callback cannot end a newer navigation because both
// compare identifiers before changing phase. Reset also advances the identifier to
// invalidate outstanding callbacks. Duration is available only while a start time is
// retained. A lazily created module singleton shares this state across router guards and
// the indicator component.

// useNavigationLoading creates navigation progress state and identifier-aware completion
// controls. Pass the identifier returned by startNavigation to delayed completion and
// animation callbacks; endNavigation defaults to the current identifier when omitted.
//
// The loading phase retains a start timestamp, completion clears it but keeps the
// indicator visible, and finishNavigation hides a matching completed indicator. Starting
// or resetting increments the identifier so earlier callbacks cannot mutate subsequent
// navigation state. The returned flags distinguish route work from the finishing
// animation.
export function useNavigationLoading() {
  // phase controls indicator visibility, navigationId rejects old callbacks, and
  // navigationStartTime exists only while duration can be measured for active navigation.
  const phase = ref<'idle' | 'loading' | 'complete'>('idle')
  const navigationId = ref(0)
  let navigationStartTime: number | null = null

  // startNavigation begins a loading phase and returns its new navigation identifier. Save
  // that identifier for asynchronous completions; starting again invalidates completion
  // and animation callbacks from earlier navigation.
  const startNavigation = (): number => {
    navigationId.value += 1
    navigationStartTime = Date.now()
    phase.value = 'loading'
    return navigationId.value
  }

  // endNavigation moves the matching navigation into its completion animation phase and
  // clears elapsed-time tracking. Omitting the identifier targets whichever navigation is
  // current at invocation, so delayed callers pass their saved identifier.
  //
  // An older identifier is ignored even while a newer navigation loads. The idle phase is
  // also ignored, while a matching completed phase can be completed again. Keeping
  // completion separate from idle lets the indicator remain visible until its animation
  // callback confirms the same navigation.
  const endNavigation = (id = navigationId.value): void => {
    if (id !== navigationId.value || phase.value === 'idle') return
    phase.value = 'complete'
    navigationStartTime = null
  }

  // finishNavigation returns a matching completed navigation to idle after its visual
  // finishing animation. Callers pass the identifier captured for that animation rather
  // than reading the current identifier when the callback runs.
  //
  // Both identity and phase must match: a stale animation cannot hide a newer route
  // operation, and an early callback cannot hide a navigation still loading. This final
  // phase check keeps asynchronous route completion and animation completion from
  // independently removing the current indicator.
  const finishNavigation = (id: number): void => {
    if (id !== navigationId.value || phase.value !== 'complete') return
    phase.value = 'idle'
  }

  // resetState returns the indicator to idle and invalidates outstanding callbacks. It
  // increments the navigation identifier even when already idle, then clears the timestamp
  // used for duration reporting.
  const resetState = (): void => {
    navigationId.value += 1
    phase.value = 'idle'
    navigationStartTime = null
  }

  // getNavigationDuration reports milliseconds since the active navigation started, or
  // null after completion or reset. It reads the current clock on each call rather than
  // maintaining a reactive timer.
  const getNavigationDuration = (): number | null =>
    navigationStartTime === null ? null : Date.now() - navigationStartTime

  return {
    isLoading: computed(() => phase.value !== 'idle'),
    isNavigating: computed(() => phase.value === 'loading'),
    navigationId: readonly(navigationId),
    startNavigation,
    endNavigation,
    finishNavigation,
    cancelNavigation: endNavigation,
    resetState,
    getNavigationDuration,
  }
}

// navigationLoadingInstance retains the shared navigation controller lazily until tests or
// callers reset its ownership.
let navigationLoadingInstance: ReturnType<typeof useNavigationLoading> | null = null

// useNavigationLoadingState returns the lazily initialized module-shared navigation
// indicator controller. Router guards and indicator components receive the same
// reactive state, so starting or resetting through one consumer affects all consumers.
export function useNavigationLoadingState() {
  if (!navigationLoadingInstance) {
    navigationLoadingInstance = useNavigationLoading()
  }
  return navigationLoadingInstance
}

// _resetNavigationLoadingInstance invalidates existing navigation callbacks and drops the
// singleton reference. Consumers already holding the old controller retain it in idle
// state, while later consumers receive a fresh instance.
export function _resetNavigationLoadingInstance(): void {
  navigationLoadingInstance?.resetState()
  navigationLoadingInstance = null
}
