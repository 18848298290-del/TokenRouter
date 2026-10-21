import { describe, it, expect, vi } from 'vitest'
import {
  useStepUp,
  isStepUpRequired,
  isStepUpBlocked,
  isStepUpCancelled,
  stepUpBlockReason,
  StepUpCancelledError,
} from '../useStepUp'

// useStepUp.spec.ts checks classification and dialog-mediated retries for sensitive
// actions. The cases distinguish required verification, blocked prerequisites, ordinary
// failures, approval, and cancellation, while manually settling the dialog lets the
// pending action promise reveal each outcome.

describe('useStepUp error classification', () => {
  // API failures expose required-verification markers through either code or reason,
  // alongside unrelated and null errors. Classification must accept either supported
  // marker location and reject unrelated values.
  it('detects STEP_UP_REQUIRED from code field', () => {
    expect(isStepUpRequired({ status: 403, code: 'STEP_UP_REQUIRED' })).toBe(true)
    expect(isStepUpRequired({ status: 403, reason: 'STEP_UP_REQUIRED' })).toBe(true)
    expect(isStepUpRequired({ status: 500, code: 'INTERNAL' })).toBe(false)
    expect(isStepUpRequired(null)).toBe(false)
  })

  // Blocked prerequisite markers are supplied through both supported error fields
  // alongside a retryable marker. Only missing TOTP setup and forbidden administrator-key
  // errors must be classified as blocked.
  it('detects blocked (non-retryable) step-up errors', () => {
    expect(isStepUpBlocked({ code: 'STEP_UP_TOTP_NOT_ENABLED' })).toBe(true)
    expect(isStepUpBlocked({ reason: 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' })).toBe(true)
    expect(isStepUpBlocked({ code: 'STEP_UP_REQUIRED' })).toBe(false)
  })

  // A blocked error supplies its reason marker while an unrelated error supplies another
  // code. The reason helper must expose the blocked marker and return empty for the
  // unrelated value.
  it('surfaces the block reason marker', () => {
    expect(stepUpBlockReason({ reason: 'STEP_UP_ADMIN_API_KEY_FORBIDDEN' })).toBe(
      'STEP_UP_ADMIN_API_KEY_FORBIDDEN',
    )
    expect(stepUpBlockReason({ code: 'OTHER' })).toBe('')
  })
})

describe('useStepUp.run', () => {
  // A sensitive action resolves successfully on its first invocation. The wrapper must
  // return that value directly and leave the verification dialog hidden throughout the
  // result.
  it('returns the action result directly on success', async () => {
    const stepUp = useStepUp()
    const result = await stepUp.run(async () => 42)
    expect(result).toBe(42)
    expect(stepUp.visible.value).toBe(false)
  })

  // A sensitive action rejects with an ordinary internal error. The wrapper must reject
  // with the same object and keep the verification dialog hidden afterward.
  it('re-throws non-step-up errors without prompting', async () => {
    const stepUp = useStepUp()
    const err = { status: 500, code: 'INTERNAL' }
    await expect(
      stepUp.run(async () => {
        throw err
      }),
    ).rejects.toBe(err)
    expect(stepUp.visible.value).toBe(false)
  })

  // A sensitive action rejects because TOTP has not been enabled. The wrapper must
  // propagate the blocked error object and avoid opening the verification prompt.
  it('re-throws blocked errors without prompting', async () => {
    const stepUp = useStepUp()
    const err = { status: 403, code: 'STEP_UP_TOTP_NOT_ENABLED' }
    await expect(
      stepUp.run(async () => {
        throw err
      }),
    ).rejects.toBe(err)
    expect(stepUp.visible.value).toBe(false)
  })

  // The first action attempt requires verification and the next attempt succeeds after
  // approval. The wrapper must open the dialog, retry once, return success, and close the
  // dialog.
  it('prompts on STEP_UP_REQUIRED and retries after verification', async () => {
    const stepUp = useStepUp()
    let calls = 0
    // action rejects its first invocation with required verification and succeeds on the
    // next call. Its counter lets the test verify one retry after dialog approval.
    const action = async () => {
      calls++
      if (calls === 1) throw { status: 403, code: 'STEP_UP_REQUIRED' }
      return 'ok'
    }
    const promise = stepUp.run(action)

    // Wait until the action failure opens the prompt before delivering approval so the
    // stored dialog resolver already exists.
    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    stepUp.onVerified()
    await expect(promise).resolves.toBe('ok')
    expect(calls).toBe(2)
    expect(stepUp.visible.value).toBe(false)
  })

  // A required-verification action waits for a dialog that the user cancels. Its promise
  // must reject with StepUpCancelledError and leave the dialog hidden after cancellation.
  it('throws a cancellation sentinel (not the original error) if the user cancels the prompt', async () => {
    const stepUp = useStepUp()
    const err = { status: 403, code: 'STEP_UP_REQUIRED' }
    const promise = stepUp.run(async () => {
      throw err
    })
    await vi.waitFor(() => expect(stepUp.visible.value).toBe(true))
    stepUp.onCancel()
    await expect(promise).rejects.toBeInstanceOf(StepUpCancelledError)
    expect(stepUp.visible.value).toBe(false)
  })

  // A constructed cancellation sentinel is compared with server-required verification
  // errors. Cancellation classification must recognize the sentinel while required and
  // blocked classification remain false for it.
  it('classifies the cancellation sentinel distinctly from step-up errors', () => {
    const cancelled = new StepUpCancelledError()
    expect(isStepUpCancelled(cancelled)).toBe(true)
    expect(isStepUpRequired(cancelled)).toBe(false)
    expect(isStepUpBlocked(cancelled)).toBe(false)
    expect(isStepUpCancelled({ status: 403, code: 'STEP_UP_REQUIRED' })).toBe(false)
  })
})
