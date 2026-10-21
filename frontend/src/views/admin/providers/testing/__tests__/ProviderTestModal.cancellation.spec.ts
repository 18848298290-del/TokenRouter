import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { useProviderBatchTest } from '../useProviderBatchTest'
import ProviderTestModal from '../ProviderTestModal.vue'

// ProviderTestModal.cancellation.spec.ts checks queued and running provider
// tests when a batch stops or its dialog disappears. The real batch scheduler
// and SSE runner receive controlled fetch responses, exposing request signals
// and queued model admission through the same execution path used by the
// dialog.

// getAvailableModels supplies three models so the default two workers leave
// one queued request for cancellation assertions.
const { getAvailableModels } = vi.hoisted(() => ({ getAvailableModels: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { providers: { getAvailableModels } } }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual<typeof import('vue-i18n')>('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))

// PendingTest exposes one controlled network request and its completion
// action.
interface PendingTest {
  // signal and complete belong to the same pending fetch: the test observes
  // cancellation through signal or releases a successful response through
  // complete.
  signal: AbortSignal // request cancellation signal
  complete: () => void // successful SSE completion
}

// pendingFetch installs a transport whose responses stay pending until
// released. Abort events reject with the browser error recognized by
// executeProviderTest, allowing callers to inspect scheduler state after
// cancellation has propagated.
function pendingFetch() {
  const pending: PendingTest[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(
      (_url: string, options: RequestInit) =>
        new Promise<Response>((resolve, reject) => {
          const signal = options.signal as AbortSignal
          // Cancellation rejects the pending fetch, matching the runner's
          // browser AbortError path so the scheduler can release its workers.
          signal.addEventListener(
            'abort',
            () => reject(new DOMException('Request aborted', 'AbortError')),
            { once: true },
          )
          pending.push({
            signal,
            complete: () =>
              resolve(new Response('data: {"type":"test_complete","success":true}\n')),
          })
        }),
    ),
  )
  return pending
}

// startBatch starts three model tests against the real scheduler with two
// workers. The returned completion promise settles after active requests
// finish or abort; callers control those outcomes through the installed
// pending-fetch fixture.
function startBatch(batch: ReturnType<typeof useProviderBatchTest>) {
  return batch.start({
    targets: ['model-a', 'model-b', 'model-c'],
    retry: false,
    providerId: 42,
    providerName: 'Batch provider',
    buildBody: (model) => ({ model_id: model, test_type: 'text' }),
  })
}

enableAutoUnmount(afterEach)

describe('provider test cancellation', () => {
  beforeEach(() => {
    getAvailableModels.mockResolvedValue(
      ['model-a', 'model-b', 'model-c'].map((id) => ({ id, display_name: id })),
    )
  })
  afterEach(() => {
    // Remove the controlled fetch replacement after each scenario so later
    // tests cannot inherit requests whose completion belongs to an earlier
    // batch.
    vi.unstubAllGlobals()
  })

  // Stopping a batch leaves its active network requests running to completion.
  // The queued model stays unrequested, and the scheduler marks it stopped
  // after both active models report success.
  it('stops queued work while allowing active requests to finish', async () => {
    const pending = pendingFetch()
    const batch = useProviderBatchTest((key) => key)
    const finished = startBatch(batch)
    expect(pending).toHaveLength(2)
    // Stop admission while both workers are occupied, then release their
    // responses so the queued model cannot start through an already-free
    // worker.
    batch.stop()
    expect(pending.every((request) => !request.signal.aborted)).toBe(true)
    pending.forEach((request) => request.complete())
    await finished
    expect(pending).toHaveLength(2)
    expect(batch.rows.value['model-a'].state).toBe('success')
    expect(batch.rows.value['model-b'].state).toBe('success')
    expect(batch.rows.value['model-c'].state).toBe('stopped')
    expect(batch.running.value).toBe(false)
  })

  // Aborting a batch cancels both active fetches and leaves the queued model
  // unrequested. Awaiting the scheduler confirms all three rows become stopped
  // and the running flag is released.
  it('aborts active requests and stops queued work', async () => {
    const pending = pendingFetch()
    const batch = useProviderBatchTest((key) => key)
    const finished = startBatch(batch)
    expect(pending).toHaveLength(2)
    // Abort while both fetch promises are pending so their rejection listeners
    // release workers before the final stopped-row assertions run.
    batch.abort()
    await finished
    expect(pending.every((request) => request.signal.aborted)).toBe(true)
    expect(pending).toHaveLength(2)
    expect(Object.values(batch.rows.value).map((row) => row.state)).toEqual([
      'stopped',
      'stopped',
      'stopped',
    ])
    expect(batch.running.value).toBe(false)
  })

  // Closing or unmounting the dialog reaches the scheduler's active request
  // signals. These cases exercise the component's cleanup hooks, proving that
  // queued models cannot start after the dialog releases its batch.
  it.each(['close', 'unmount'] as const)('aborts the mounted batch on %s', async (action) => {
    const pending = pendingFetch()
    const wrapper = mount(ProviderTestModal, {
      props: {
        show: false,
        provider: {
          id: 42,
          name: 'Batch provider',
          platform: 'openai',
          type: 'apikey',
          credentials: {},
          extra: {},
        } as any,
      },
      global: {
        stubs: {
          BaseDialog: {
            emits: [
              'close', // close the owning dialog
            ],
            template:
              '<div><button data-test="close" @click="$emit(\'close\')">close</button><slot /><slot name="footer" /></div>',
          },
          Select: true,
          PlatformIcon: true,
          TextArea: true,
          Icon: true,
          ProviderTestBatchPanel: true,
          ProviderTestResultView: true,
        },
      },
    })
    await wrapper.setProps({ show: true })
    await flushPromises()
    const state = (wrapper.vm as any).$?.setupState
    state.startBatch(['model-a', 'model-b', 'model-c'], false)
    expect(pending).toHaveLength(2)
    // Close or unmount only after both workers acquire fetches, making cleanup
    // responsible for cancelling active requests as well as stopping queued
    // admission.
    if (action === 'close') await wrapper.get('[data-test="close"]').trigger('click')
    else wrapper.unmount()
    await flushPromises()
    expect(pending.every((request) => request.signal.aborted)).toBe(true)
    expect(pending).toHaveLength(2)
    expect(state.batch.running).toBe(false)
    if (action === 'close') expect(wrapper.emitted('close')).toHaveLength(1)
  })
})
