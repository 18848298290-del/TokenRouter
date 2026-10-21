/**
 * leavingElement.ts isolates elements during leave transitions without
 * disabling every form control. The leaving root becomes inert, while only
 * currently enabled invalid descendants are disabled to remove them from form
 * validation during the animation.
 *
 * A WeakMap records the exact controls changed for each root, so a cancelled
 * leave can restore those controls without enabling ones that were already
 * disabled. Repeated isolation keeps the first recorded set. Restoration also
 * removes a captured list-width style. The caller coordinates these helpers
 * with transition hooks; detached elements need no explicit map cleanup for
 * garbage collection.
 */

/**
 * FormControl identifies form elements whose validation state participates in
 * leave isolation.
 */
type FormControl = HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement | HTMLButtonElement
/**
 * disabledControls records only controls changed by each leaving root,
 * allowing restoration without enabling controls that were already disabled.
 */
const disabledControls = new WeakMap<Element, FormControl[]>()

/**
 * isolateLeavingElement marks a leaving root inert and disables its enabled
 * invalid descendants. This keeps disappearing required fields from blocking
 * form validation while other controls retain their existing appearance.
 *
 * The first call records only controls changed by this helper, and later calls
 * for the same root leave that list intact. A cancelled transition restores
 * that recorded set through restoreEnteringElement. The helper assumes those
 * recorded controls remain owned by the transition until restoration; it does
 * not reconcile independent disabled-state changes made meanwhile.
 */
export function isolateLeavingElement(element: Element) {
  element.setAttribute('inert', '')
  if (disabledControls.has(element)) return
  const controls = Array.from(
    element.querySelectorAll<FormControl>('input, textarea, select, button'),
  ).filter((control) => !control.disabled && !control.validity.valid)
  /**
   * Remember the originally enabled invalid controls before disabling them, so a
   * cancelled transition restores only the changes owned by this leave
   * operation.
   */
  disabledControls.set(element, controls)
  controls.forEach((control) => {
    control.disabled = true
  })
}

/**
 * restoreEnteringElement removes transition isolation and restores the
 * controls recorded for that root. It also removes the captured list width and
 * forgets the record; controls disabled before isolation remain untouched.
 */
export function restoreEnteringElement(element: Element) {
  element.removeAttribute('inert')
  if (element instanceof HTMLElement) element.style.removeProperty('--motion-list-width')
  disabledControls.get(element)?.forEach((control) => {
    control.disabled = false
  })
  /**
   * Forget the restored control set after reenabling it, allowing a later leave
   * to capture current validation and disabled state afresh.
   */
  disabledControls.delete(element)
}

/**
 * prepareListLeave records an HTML element width before isolating its form
 * controls. Transition styles can keep that width after the element leaves
 * normal flow, and restoration removes the temporary custom property.
 */
export function prepareListLeave(element: Element) {
  if (element instanceof HTMLElement) {
    element.style.setProperty('--motion-list-width', `${element.getBoundingClientRect().width}px`)
  }
  isolateLeavingElement(element)
}
