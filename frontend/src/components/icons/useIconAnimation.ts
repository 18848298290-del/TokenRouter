import { onBeforeUnmount, onMounted, watch, type Ref } from 'vue'
import { useAnimationControls } from 'motion-v'
import type { IconDefinition } from './types'

// useIconAnimation.ts binds decorative artwork playback to pointer, focus, and
// programmatic activity on the nearest interactive element. Hover and focus
// remain independent so leaving one interaction source does not reset an icon
// still held active by another. Disabled ancestors, loading state, and the
// reduced-motion preference suppress playback as those conditions change.
//
// Each playback transition advances a generation before stopping the current
// controller. Awaited animation steps check that generation before starting
// their successor, preventing a cancelled sequence from resuming after playback
// replacement, an artwork replacement, or unmount. Cleanup releases listeners,
// watchers, and attribute observation owned by the mounted instance.

// INTERACTIVE_SELECTOR identifies controls whose text and surrounding hit area
// share the icon feedback trigger.
const INTERACTIVE_SELECTOR = [
  'button',
  'a[href]',
  'summary',
  'label',
  '[data-icon-trigger]',
  '[role="button"]',
  '[role="tab"]',
  '[role="menuitem"]',
  '[role="menuitemcheckbox"]',
  '[role="menuitemradio"]',
  '[role="option"]',
  '[role="checkbox"]',
  '[role="radio"]',
  '[role="switch"]',
].join(', ')

// useIconAnimation returns the controller for one icon and installs interaction
// handling when its SVG mounts. Call it during component setup with reactive
// getters for artwork and activation; the optional programmatic getter defaults
// to false.
//
// The nearest matching ancestor owns pointer and focus feedback, with a
// standalone SVG acting as its own trigger. Generation checks stop an awaited
// sequence from starting another step after cancellation, while unmount removes
// the listeners and watchers registered for that mounted SVG.
export function useIconAnimation(
  svgRef: Ref<SVGSVGElement | null>,
  definition: () => IconDefinition,
  enabled: () => boolean,
  animationActive: () => boolean = () => false,
) {
  // controls drives this icon instance; cleanup is assigned after mount and
  // releases that instance's DOM listeners and reactive observers.
  const controls = useAnimationControls()
  let cleanup: (() => void) | undefined

  onMounted(() => {
    const svg = svgRef.value
    if (!svg) return

    // trigger captures the mounted interaction host, labelledControl resolves
    // label forwarding, and media supplies the live reduced-motion condition
    // used by update.
    const trigger = svg.closest(INTERACTIVE_SELECTOR) ?? svg
    const labelledControl = trigger instanceof HTMLLabelElement ? trigger.control : null
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    // hovered and focused track independent interaction sources. active records
    // the combined allowed state, and generation invalidates awaited sequences
    // whenever playback ownership changes.
    let hovered = false
    let focused = false
    let active = false
    let generation = 0

    // reset stops current playback and restores the selected artwork's resting
    // target. Immediate resets set that target synchronously for suppressed
    // motion; other resets animate toward it using the artwork's transition.
    const reset = (immediate: boolean) => {
      controls.stop()
      if (immediate) controls.set(definition().normal)
      else void controls.start(definition().normal)
    }

    // update reconciles activation sources and suppression conditions, then
    // changes playback ownership when the allowed state changes. Repeated
    // updates with the same active state leave the current sequence alone, so
    // sustained hover or focus does not restart it.
    //
    // Suppression also interrupts an in-progress reset when active is already
    // false. Advancing generation before starting or resetting playback
    // prevents the pending continuation of an older sequence from launching its
    // next target after the transition.
    const update = () => {
      const suppressed =
        (!enabled() && !animationActive()) ||
        media.matches ||
        trigger.matches(':disabled') ||
        Boolean(labelledControl?.matches(':disabled, [aria-disabled="true"]')) ||
        Boolean(svg.closest('fieldset:disabled, [aria-disabled="true"], [inert]')) ||
        svg.classList.contains('animate-spin')
      const next = !suppressed && (animationActive() || (enabled() && (hovered || focused)))
      if (suppressed && !active) {
        // A preference change can arrive while the return animation is still
        // running; invalidate its sequence and apply the resting target
        // immediately.
        ++generation
        reset(true)
        return
      }
      if (next === active) return
      active = next
      const current = ++generation
      controls.stop()
      if (!next) {
        reset(suppressed)
        return
      }

      // Await actual completion before each successor, then compare ownership
      // so a stopped sequence cannot resume after leaving, replacement, or
      // unmount.
      void (async () => {
        for (const target of definition().animate) {
          if (current !== generation) return
          await controls.start(target)
        }
      })()
    }

    // enter records pointer hover and reconciles playback for the trigger.
    // Touch events leave the existing hover flag untouched; mouse and pen
    // entries can activate the shared interaction state.
    const enter = (event: Event) => {
      // Touch pointer entry is ignored because a tap has no matching hover
      // lifetime to reset this decorative feedback.
      if ('pointerType' in event && event.pointerType === 'touch') return
      hovered = true
      update()
    }
    // leave clears pointer activity and reconciles the remaining focus or
    // programmatic activation. A focused trigger can therefore keep its current
    // sequence active after the pointer moves away.
    const leave = () => {
      hovered = false
      update()
    }
    // focus records focus activity and redirects focus received by decorative
    // SVG nodes. Labels forward to their associated form control; other HTML
    // triggers receive focus directly, and an SVG that remains focused is
    // blurred before activity is recomputed.
    const focus = (event: Event) => {
      // A negative tabindex still permits programmatic or pointer focus, so
      // forward or clear decorative focus before treating it as control
      // activity.
      if (event.target instanceof SVGElement && svg.contains(event.target)) {
        const control = labelledControl ?? trigger
        if (control instanceof HTMLElement) {
          control.focus({ preventScroll: true })
        }
        const activeElement = svg.ownerDocument.activeElement
        if (activeElement instanceof SVGElement && svg.contains(activeElement)) {
          activeElement.blur()
          focused = false
          update()
          return
        }
      }
      focused = true
      update()
    }
    // blur clears focus activity when focus leaves the trigger subtree.
    // Transfers between descendants retain activity, allowing a control with
    // nested content to keep feedback during internal focus changes.
    const blur = (event: Event) => {
      if (trigger.contains((event as FocusEvent).relatedTarget as Node | null)) return
      focused = false
      update()
    }

    trigger.addEventListener('pointerenter', enter)
    trigger.addEventListener('pointerleave', leave)
    trigger.addEventListener('focusin', focus)
    trigger.addEventListener('focusout', blur)
    media.addEventListener('change', update)

    // Observe attributes while interaction remains active so asynchronous
    // disabling, loading, and ancestor inert changes can suppress playback
    // immediately.
    const observer = new MutationObserver(update)
    if (labelledControl) {
      observer.observe(labelledControl, {
        attributes: true,
        attributeFilter: ['disabled', 'aria-disabled'],
      })
    }
    let ancestor: Element | null = svg
    while (ancestor) {
      observer.observe(ancestor, {
        attributes: true,
        attributeFilter:
          ancestor === svg
            ? ['disabled', 'aria-disabled', 'inert', 'class']
            : ['disabled', 'aria-disabled', 'inert'],
      })
      ancestor = ancestor.parentElement
    }

    // unwatchEnabled, unwatchActive, and unwatchDefinition retain the disposers
    // for mounted reactive subscriptions; cleanup stops each before releasing
    // the trigger listeners.
    const unwatchEnabled = watch(enabled, update)
    // Programmatic activation has its own watcher because it can request
    // playback while hover handling is disabled by the caller.
    const unwatchActive = watch(animationActive, update)
    const unwatchDefinition = watch(
      definition,
      () => {
        // The new artwork has different motion nodes after the update;
        // invalidate the old sequence before resetting and reevaluating its
        // activation.
        ++generation
        active = false
        reset(true)
        update()
      },
      { flush: 'post' },
    )

    // cleanup invalidates pending continuations and releases the mounted icon
    // resources. It stops controls before disconnecting attribute observers,
    // reactive subscriptions, and trigger listeners so a detached host cannot
    // restart playback.
    cleanup = () => {
      ++generation
      controls.stop()
      observer.disconnect()
      unwatchEnabled()
      unwatchActive()
      unwatchDefinition()
      trigger.removeEventListener('pointerenter', enter)
      trigger.removeEventListener('pointerleave', leave)
      trigger.removeEventListener('focusin', focus)
      trigger.removeEventListener('focusout', blur)
      media.removeEventListener('change', update)
    }
    if (animationActive()) update()
  })

  onBeforeUnmount(() => cleanup?.())
  return controls
}
