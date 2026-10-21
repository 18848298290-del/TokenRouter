import { ref, watch } from 'vue'

// useLeavingPresence.ts keeps lazily mounted dialog content present through its shell's
// leave transition. Visibility opens content synchronously, while a close leaves the
// mounted content available for the animation. The shell reports actual exit completion
// through the returned afterLeave callback.
//
// A callback can arrive after the user has reopened the dialog. It therefore reads current
// visibility before clearing presence, keeping the same content mounted when a newer open
// state has superseded the exit. The composable owns presence state, while the caller owns
// visibility and the transition event that determines when removal is allowed.

// useLeavingPresence retains lazily mounted dialog content until a closed shell finishes
// its leave transition. Invoke it during setup and connect afterLeave to that shell's
// completion event. Opening sets presence synchronously so the shell can render its
// content.
//
// Closing keeps presence true while animation proceeds. The delayed callback consults
// current visibility before clearing presence, allowing a dialog reopened during exit to
// keep its mounted content. Unmounting the owning component still follows Vue's ordinary
// teardown lifecycle.
export function useLeavingPresence(visible: () => boolean) {
  const present = ref(visible())
  watch(
    visible,
    (open) => {
      if (open) present.value = true
    },
    { flush: 'sync' },
  )
  // afterLeave clears content presence after an exit only while visibility still reports
  // closed. It receives transition completion after the original close event, so its
  // decision uses the current visibility source rather than a captured close value.
  //
  // When the dialog reopened during animation, leaving presence intact keeps the content
  // mounted for the renewed visible state. Callers attach this operation to the shell that
  // owns the exit animation; an early manual call can remove content before that animation
  // finishes.
  function afterLeave() {
    if (!visible()) present.value = false
  }
  return { present, afterLeave }
}
