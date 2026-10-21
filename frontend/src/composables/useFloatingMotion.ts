import { ref, watch, type Ref } from 'vue'

// useFloatingMotion.ts tracks an open floating panel while its trigger or ancestors change
// layout. Attribute observation checks hidden, inert, class, and style state along the
// trigger's current ancestor chain. A blocked trigger closes the panel, while visible
// collapsing ancestors keep repositioning it on animation frames.
//
// Each watcher run owns its observer and frame handle. Changing trigger or visibility
// disposes that run before establishing another, and the stopped flag prevents its
// callbacks from continuing layout work after cleanup. Ancestors are captured when
// observation starts; moving the trigger into a new hierarchy requires a watcher rerun.

// useFloatingMotion returns whether the current trigger is hidden or inert while managing
// floating-panel positioning. Supply reactive trigger and visibility sources during setup,
// plus callbacks that close and reposition the panel.
//
// The post-render watcher observes the current ancestor chain and follows active collapse
// animations. Each run owns a MutationObserver and animation frame; cleanup stops both
// before replacement observation proceeds. A visibility or trigger change resets blocked
// state, and hidden ancestors close the panel once per transition into blocking.
export function useFloatingMotion(
  trigger: Readonly<Ref<HTMLElement | null>>,
  visible: () => boolean,
  close: () => void,
  reposition: () => void,
) {
  // blocked describes the current watcher run's hidden or inert trigger state and is reset
  // when visibility or trigger changes.
  const blocked = ref(false)
  watch(
    [trigger, visible],
    ([element, open], _previous, onCleanup) => {
      blocked.value = false
      if (!element || !open) return
      let frame = 0
      let stopped = false
      // cancelFrame cancels this watcher run's pending reposition frame. Clearing the
      // handle makes repeated cleanup safe when attribute changes interrupt movement or
      // the watcher run is disposed.
      const cancelFrame = () => {
        if (frame) cancelAnimationFrame(frame)
        frame = 0
      }
      // follow repositions the panel and continues while the trigger belongs to an active
      // collapse animation. It clears the frame handle before checking whether this
      // watcher run was stopped or its trigger became blocked.
      //
      // Those checks prevent a disposed observer run from scheduling successors and
      // prevent hidden triggers from continuing layout work. Repositioning can run once
      // without a moving ancestor; the movement class alone decides whether another frame
      // is scheduled after that measurement.
      const follow = () => {
        frame = 0
        if (stopped || blocked.value) return
        reposition()
        if (element.closest('.motion-collapse-moving')) frame = requestAnimationFrame(follow)
      }
      // update recomputes blocking from trigger ancestors and restarts movement tracking.
      // Hidden or inert content closes the floating panel on the transition into blocked
      // state. It checks computed display along the ancestor chain because a parent can
      // hide descendants without changing the trigger itself.
      //
      // After publishing blocked state, the existing frame is cancelled before following
      // visible movement again. This ordering gives each attribute update one active frame
      // sequence and prevents hidden panels from continuing reposition work.
      const update = () => {
        let next = !!element.closest('[inert], [hidden]')
        let current: HTMLElement | null = element
        while (current && !next) {
          next = getComputedStyle(current).display === 'none'
          current = current.parentElement
        }
        if (next && !blocked.value) close()
        blocked.value = next
        cancelFrame()
        if (!next) follow()
      }
      const observer = new MutationObserver(update)

      // Observe the trigger and its current ancestors because a parent class or inline
      // display change can hide teleported content's anchor.
      let ancestor: HTMLElement | null = element
      while (ancestor) {
        observer.observe(ancestor, {
          attributes: true,
          attributeFilter: ['inert', 'hidden', 'class', 'style'],
        })
        ancestor = ancestor.parentElement
      }
      // Mark this observer run stopped before disconnecting and cancelling frames so
      // callbacks from a replaced trigger cannot continue following layout.
      onCleanup(() => {
        stopped = true
        observer.disconnect()
        cancelFrame()
      })
      update()
    },
    { immediate: true, flush: 'post' },
  )
  return blocked
}
