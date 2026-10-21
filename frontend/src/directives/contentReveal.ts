import type { ObjectDirective } from 'vue'

// contentReveal.ts animates opacity for already mounted content when its directive value
// changes. Each element owns its media-query listener and current Web Animation,
// allowing rapid changes to cancel the earlier effect before another begins.
//
// Reduced-motion preferences suppress new effects and cancel an active effect when the
// preference changes. Completion releases the animation reference only if that animation
// still owns the element, so an older completion cannot clear a replacement. Unmount
// cancels the effect, removes the media listener, and releases state. Opacity is
// decorative: the directive keeps existing content and interactive elements mounted
// throughout the animation.

// RevealState owns one mounted element's animation and reduced-motion listener cleanup.
interface RevealState {
  // animation holds the currently owned effect; completion clears it only when the
  // finishing animation still matches that reference.
  animation?: Animation // Currently owned opacity effect.
  media: MediaQueryList // Observed reduced-motion preference query.
  // stop cancels the current animation and releases its element-owned reference. The
  // same closure handles preference changes and unmount, so repeated cleanup safely
  // leaves the element without an active effect.
  stop: () => void // Owned animation cancellation callback.
}

// states associates mounted elements with their owned animation and media-listener
// resources until directive cleanup releases them.
const states = new WeakMap<HTMLElement, RevealState>()

// reveal restarts the decorative opacity animation for an element with registered
// directive state. Existing animation work is cancelled before checking reduced motion,
// animation support, and the configured duration. A usable duration creates an animation
// whose completion clears the state reference only when it still owns that element.
// Cancellation rejects the finished promise and is intentionally ignored, because the
// stop operation has already released its owned reference.
function reveal(element: HTMLElement) {
  const state = states.get(element)
  // Cancel the previous effect before checking current preferences, so a disabled
  // binding or reduced-motion change cannot leave an earlier animation running.
  state?.stop()
  if (!state || state.media.matches || !element.animate) return
  const style = getComputedStyle(element)
  const duration = Number.parseFloat(style.getPropertyValue('--motion-fast'))
  if (!Number.isFinite(duration) || duration <= 0) return
  const animation = element.animate([{ opacity: 0 }, { opacity: 1 }], {
    duration,
    easing: style.getPropertyValue('--motion-ease').trim() || 'ease-out',
  })
  state.animation = animation
  // An earlier animation can finish after replacement; release the reference only for
  // its current owner and ignore cancellation rejection.
  void animation.finished.then(
    () => {
      if (state.animation === animation) state.animation = undefined
    },
    () => {},
  )
}

// @project-doc docs/architecture/frontend_ui_conventions.md#ui_motion
// vContentReveal binds opacity animation and reduced-motion observation to a mounted
// element. Changing the binding restarts the effect unless the new value is false;
// unmount cancels work and removes the listener.
export const vContentReveal: ObjectDirective<HTMLElement, unknown> = {
  // mounted registers the element's reduced-motion listener and starts eligible content
  // feedback. A false initial binding keeps the listener active while skipping the
  // initial animation, ready for later updates.
  mounted(element, binding) {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)')
    const state: RevealState = {
      media,
      stop: () => {
        state.animation?.cancel()
        state.animation = undefined
      },
    }
    states.set(element, state)
    media.addEventListener('change', state.stop)
    if (binding.value !== false) reveal(element)
  },
  // updated restarts content feedback when the binding changes by Object.is comparison.
  // A false value cancels the current animation, while an unchanged binding keeps the
  // existing effect and content state.
  updated(element, binding) {
    if (Object.is(binding.value, binding.oldValue)) return
    if (binding.value === false) states.get(element)?.stop()
    else reveal(element)
  },
  // beforeUnmount cancels the element's animation and removes its media listener. It
  // then deletes the state entry so neither later preference events nor directive
  // updates retain those owned resources.
  beforeUnmount(element) {
    const state = states.get(element)
    state?.stop()
    state?.media.removeEventListener('change', state.stop)
    states.delete(element)
  },
}
