import { nextTick, onBeforeUnmount, watch, type Ref } from 'vue'

// useDialogLifecycle.ts coordinates body scroll locking and focus restoration across
// nested dialogs. Every open dialog holds a unique token in a shared stack, while closing
// retains that token through the leave transition. Reopening during that transition reuses
// the held token and clears the remembered closing panel.
//
// The top dialog receives initial focus after Vue renders its panel. Release removes its
// token and restores prior focus only when the dialog was topmost, focus still belongs to
// the exiting panel or document, and the previous target remains connected and outside
// inert content. The final release unlocks body scrolling.

// dialogs records held dialog tokens in opening order so focus and body scroll locking
// share one stack.
const dialogs: symbol[] = []

// useDialogLifecycle holds scroll locking and restores focus for a rendered dialog. Call
// it during setup and wire afterLeave to the panel transition; a visibility change alone
// retains ownership while the panel exits.
//
// Nested dialogs use the shared token stack to decide who may receive focus. Reopening
// cancels pending release through the visibility check, while unmount releases
// immediately. Focus restoration respects newer focus outside the exiting panel and skips
// disconnected or inert previous targets.
export function useDialogLifecycle(visible: () => boolean, panel: Ref<HTMLElement | null>) {
  // token identifies this dialog in the shared stack. previousFocus is captured once per
  // held interval; closingPanel retains the exiting DOM for focus ownership checks.
  const token = Symbol('dialog')
  let previousFocus: HTMLElement | null = null
  let held = false
  let closingPanel: HTMLElement | null = null

  watch(
    visible,
    async (open) => {
      if (!open) {
        closingPanel = panel.value
        return
      }
      if (!held) {
        previousFocus =
          document.activeElement instanceof HTMLElement ? document.activeElement : null
        dialogs.push(token)
        document.body.classList.add('modal-open')
        held = true
      }
      closingPanel = null
      // Wait for panel rendering before choosing a focus target, then recheck visibility
      // and stack ownership after the asynchronous render boundary.
      await nextTick()
      if (!visible() || dialogs[dialogs.length - 1] !== token) return
      const target = panel.value?.querySelector<HTMLElement>(
        'button:not(:disabled), [href], input:not(:disabled), textarea:not(:disabled), [tabindex]:not([tabindex="-1"])',
      )
      const focusTarget = target ?? panel.value
      focusTarget?.focus({ preventScroll: true })
    },
    { immediate: true },
  )

  // release drops this dialog's held stack entry and conditionally restores its previous
  // focus. Repeated calls stop after the first release. The last dialog removes the body
  // scroll-lock class, while nested dialogs leave it active.
  //
  // Restoration requires this token to have been topmost and current focus to remain in
  // either the current or closing panel, the document body, or a disconnected node. A
  // connected previous target inside inert content is also excluded.
  function release() {
    if (!held) return
    const wasTop = dialogs[dialogs.length - 1] === token
    dialogs.splice(dialogs.indexOf(token), 1)
    held = false
    if (dialogs.length === 0) document.body.classList.remove('modal-open')
    const active = document.activeElement
    const focusStillInside =
      active === document.body ||
      !active?.isConnected ||
      panel.value?.contains(active) ||
      closingPanel?.contains(active)

    // Restore the captured node only while focus still belongs to this exit; another
    // dialog or control keeps focus it acquired.
    if (
      wasTop &&
      focusStillInside &&
      previousFocus?.isConnected &&
      !previousFocus.closest('[inert]')
    ) {
      previousFocus.focus({ preventScroll: true })
    }
    previousFocus = null
    closingPanel = null
  }

  // afterLeave releases dialog ownership when its leave transition has finished and
  // visibility still reports closed. Wire it to the actual panel transition rather than
  // invoking it immediately on a visibility change.
  //
  // The current visibility check decides whether a delayed leave callback may release the
  // held token. If the dialog reopened, its existing scroll lock and captured focus remain
  // held for the new visible interval, and release waits for a later completed exit or
  // unmount.
  function afterLeave() {
    if (!visible()) release()
  }

  onBeforeUnmount(release)
  return { afterLeave, isTop: () => dialogs[dialogs.length - 1] === token }
}
