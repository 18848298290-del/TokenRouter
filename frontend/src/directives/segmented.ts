import type { ObjectDirective } from 'vue'

// segmented.ts positions a segmented control's background using the selected child's
// layout geometry. Callers own button state, keyboard behavior, and active classes; the
// directive writes CSS variables used by the shared track styles.
//
// Each control observes its current children and updates observer membership when Vue
// changes the option list. Offset geometry remains relative to layout rather than
// transformed viewport bounds, allowing controls inside scaled dialogs to retain the
// correct indicator placement. An absent or unmeasurable selection clears readiness so
// hidden controls do not show stale geometry. Unmount disconnects observation and
// removes the directive-owned attributes and variables.

// SegmentedState tracks the resize observer and current children for one control.
interface SegmentedState {
  // observer is absent when ResizeObserver is unavailable; mount and Vue updates still
  // perform measurements in that environment.
  observer?: ResizeObserver // Optional browser resize observer.
  // items tracks the children already observed for this control and is replaced after
  // membership reconciliation on each measurement.
  items: Set<HTMLElement> // Currently observed option elements.
}

// states retains each mounted control's observer membership, while properties lists the
// geometry variables written to the shared indicator styles.
const states = new WeakMap<HTMLElement, SegmentedState>()
const properties = ['x', 'y', 'width', 'height'] as const

// syncIndicator reconciles observed options and positions the background beneath the
// active child. Removed children stop being observed before new children join the
// observer, keeping future resize updates tied to the current option set. A missing or
// zero-sized active item removes readiness and leaves display suppression to CSS.
// Otherwise offset geometry supplies position and dimensions relative to the track,
// independent from an ancestor dialog's visual scale.
function syncIndicator(element: HTMLElement) {
  const state = states.get(element)
  if (!state) return

  const items = new Set(
    Array.from(element.children).filter(
      (child): child is HTMLElement =>
        child instanceof HTMLElement && child.classList.contains('segmented-item'),
    ),
  )
  // Reconcile observer membership before measuring so removed options stop producing
  // updates and newly added children can trigger later layout corrections.
  for (const item of state.items) {
    if (!items.has(item)) state.observer?.unobserve(item)
  }
  for (const item of items) {
    if (!state.items.has(item)) state.observer?.observe(item)
  }
  state.items = items

  const active = Array.from(items).find((item) => item.classList.contains('segmented-item-active'))
  if (!active || !active.offsetWidth || !active.offsetHeight) {
    // An empty selection or hidden item has no usable geometry, so clear readiness and
    // let CSS hide the previous indicator position.
    element.removeAttribute('data-segmented-ready')
    return
  }
  // Use layout offsets because transformed viewport rectangles include ancestor dialog
  // scaling and would distort the indicator's local position and dimensions.
  const values = [active.offsetLeft, active.offsetTop, active.offsetWidth, active.offsetHeight]
  properties.forEach((property, index) => {
    element.style.setProperty(`--segmented-${property}`, `${values[index]}px`)
  })
  element.setAttribute('data-segmented-ready', '')
}

// @project-doc docs/architecture/frontend_ui_conventions.md#ui_motion
// vSegmented binds indicator measurement to Vue updates and browser resize observation.
// It works without ResizeObserver through mount and update hooks, and unmount removes
// observation plus directive-owned geometry and readiness state.
export const vSegmented: ObjectDirective<HTMLElement> = {
  // mounted creates per-control observation and performs the first indicator
  // measurement. Browsers without ResizeObserver still receive measurement during mount
  // and Vue updates, while resize-driven updates depend on observer support.
  mounted(element) {
    const observer =
      typeof ResizeObserver === 'undefined'
        ? undefined
        : new ResizeObserver(() => syncIndicator(element))
    states.set(element, { observer, items: new Set() })
    observer?.observe(element)
    syncIndicator(element)
  },
  updated: syncIndicator,
  // beforeUnmount disconnects observation and removes the control's indicator state. It
  // clears readiness and all geometry variables so reused DOM does not retain
  // measurements owned by this directive instance.
  beforeUnmount(element) {
    states.get(element)?.observer?.disconnect()
    states.delete(element)
    element.removeAttribute('data-segmented-ready')
    properties.forEach((property) => element.style.removeProperty(`--segmented-${property}`))
  },
}
