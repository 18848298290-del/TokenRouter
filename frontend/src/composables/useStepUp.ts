import { ref } from 'vue'

// useStepUp.ts coordinates sensitive actions with a TOTP verification dialog. An action
// that reports STEP_UP_REQUIRED waits for the dialog result and retries once after
// verification. Blocked prerequisites propagate their original error, and cancellation
// produces a recognizable sentinel error for callers.
//
// Each controller stores one pending dialog resolver, so callers serialize prompts and
// sensitive actions sharing that controller. Opening another prompt replaces the resolver
// for the previous prompt. The dialog calls onVerified or onCancel to settle the current
// promise; controller disposal has no automatic settlement. Consumers mount the dialog and
// handle error feedback around the returned action promise.

// STEP_UP_REQUIRED and the blocked prerequisite markers classify API failures before the
// controller decides whether to prompt.
const STEP_UP_REQUIRED = 'STEP_UP_REQUIRED'
const STEP_UP_TOTP_NOT_ENABLED = 'STEP_UP_TOTP_NOT_ENABLED'
const STEP_UP_ADMIN_API_KEY_FORBIDDEN = 'STEP_UP_ADMIN_API_KEY_FORBIDDEN'

// StepUpCancelledError identifies a user-cancelled verification prompt for caller error
// handling.
export class StepUpCancelledError extends Error {
  readonly code = 'STEP_UP_CANCELLED' // stable cancellation error marker
  // constructor creates the cancellation sentinel with its stable error identity.
  constructor() {
    super('step-up verification cancelled by user')
    this.name = 'StepUpCancelledError'
  }
}

// isStepUpCancelled identifies cancellation errors created by this controller
// implementation.
export function isStepUpCancelled(err: unknown): boolean {
  return err instanceof StepUpCancelledError
}

// ApiError describes optional fields inspected when classifying step-up API failures.
interface ApiError {
  status?: number // optional HTTP response status
  code?: string | number // primary API error marker
  reason?: string // secondary API error marker
  message?: string // optional API error message
}

// markerOf returns the first string code or reason beginning with STEP_UP. Code takes
// precedence over reason; missing or non-string markers produce an empty string for
// unrelated failures.
function markerOf(err: unknown): string {
  const e = (err ?? {}) as ApiError
  const candidates = [e.code, e.reason].map((v) => (typeof v === 'string' ? v : ''))
  return candidates.find((v) => v.startsWith('STEP_UP')) || ''
}

// isStepUpRequired reports whether an API failure requests additional TOTP verification.
export function isStepUpRequired(err: unknown): boolean {
  return markerOf(err) === STEP_UP_REQUIRED
}

// isStepUpBlocked identifies missing TOTP setup or forbidden administrator API-key
// verification.
export function isStepUpBlocked(err: unknown): boolean {
  const m = markerOf(err)
  return m === STEP_UP_TOTP_NOT_ENABLED || m === STEP_UP_ADMIN_API_KEY_FORBIDDEN
}

// stepUpBlockReason returns the first recognized step-up marker from an error. It
// returns an empty string for unrelated markers, and callers use that distinction when
// selecting verification failure text.
export function stepUpBlockReason(err: unknown): string {
  return markerOf(err)
}

// StepUpController describes the dialog state and operations returned by useStepUp.
export type StepUpController = ReturnType<typeof useStepUp>

// useStepUp creates a dialog controller that retries sensitive actions after verification.
// Consumers mount TotpStepUpDialog with this controller and connect its verification and
// cancellation events. Callers serialize prompts because one resolver slot represents the
// pending decision.
//
// A blocked action records its marker and propagates its error. A required action waits
// for the current prompt, converts cancellation into StepUpCancelledError, and retries
// once after approval. The retry executes outside the original catch, so repeated failures
// propagate directly.
export function useStepUp() {
  // visible exposes the dialog, blockedReason retains the latest blocked marker, and
  // resolver stores one pending decision that callers settle through dialog events.
  const visible = ref(false)
  const blockedReason = ref<string>('')
  let resolver: ((ok: boolean) => void) | null = null

  // prompt opens the verification dialog and returns its pending decision promise. Callers
  // allow only one outstanding prompt per controller because a later call replaces the
  // stored resolver.
  function prompt(): Promise<boolean> {
    visible.value = true
    return new Promise<boolean>((resolve) => {
      resolver = resolve
    })
  }

  // onVerified closes the dialog and resolves the current prompt with approval. It clears
  // the stored resolver after settlement; repeated calls have no pending promise to
  // resolve.
  function onVerified() {
    visible.value = false
    resolver?.(true)
    resolver = null
  }

  // onCancel closes the dialog and resolves the current prompt with cancellation. It
  // clears the stored resolver after settlement, leaving the action wrapper to create its
  // cancellation sentinel.
  function onCancel() {
    visible.value = false
    resolver?.(false)
    resolver = null
  }

  // run executes a sensitive action and returns its result, prompting when the initial
  // failure requests step-up verification. Blocked prerequisite failures update
  // blockedReason and propagate unchanged; other unrelated failures also propagate.
  //
  // A required prompt waits for user approval before one retry. Cancellation throws
  // StepUpCancelledError, and the retry's failure propagates directly because it is
  // outside the first attempt's catch. Actions sharing this controller must be serialized
  // to prevent one prompt from replacing another unresolved decision.
  async function run<T>(action: () => Promise<T>): Promise<T> {
    try {
      return await action()
    } catch (err) {
      if (isStepUpBlocked(err)) {
        blockedReason.value = markerOf(err)
        throw err
      }
      if (!isStepUpRequired(err)) {
        throw err
      }
      const ok = await prompt()
      if (!ok) {
        throw new StepUpCancelledError()
      }

      // After verification, retry once outside the initial attempt's catch so repeated
      // authorization failures return to the caller.
      return await action()
    }
  }

  return {
    visible,
    blockedReason,
    prompt,
    onVerified,
    onCancel,
    run,
  }
}
