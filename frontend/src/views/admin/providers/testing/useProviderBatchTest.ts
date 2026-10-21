import { computed, ref } from 'vue'
import {
  createProviderTestRun,
  executeProviderTest,
  type ProviderTestRequestBody,
  type ProviderTestRun,
} from './providerTestRun'

/**
 * useProviderBatchTest.ts schedules selected models through a bounded number of provider
 * connection tests and keeps per-model results for inspection or retry. Selection and result
 * rows are separate: retry replaces targeted rows while retaining other results. Model
 * identifiers are the keys used for both display and execution.
 *
 * Workers share a cursor and claim each target before awaiting its stream. Stopping prevents
 * further claims and lets active streams finish; aborting also cancels their shared request
 * signal. Remaining queued rows become stopped after workers settle. The caller owns component
 * cleanup and coordinates reset or a new run with that settlement, because the scheduler has no
 * generation guard for overlapping lifecycles.
 */

/**
 * MAX_BATCH_MODELS caps model selection for the batch panel; the scheduler receives its targets
 * from the caller.
 */
export const MAX_BATCH_MODELS = 50
/**
 * BATCH_CONCURRENCY_OPTIONS supplies the dialog choices for simultaneous requests, which share
 * one provider and cancellation signal.
 */
export const BATCH_CONCURRENCY_OPTIONS = [1, 2, 3] as const

/**
 * ProviderBatchRowState identifies waiting, executing, completed, and interrupted model rows.
 */
export type ProviderBatchRowState = 'queued' | 'running' | 'success' | 'failed' | 'stopped'

/**
 * ProviderBatchRow combines a model identifier, scheduler state, and mutable test output.
 */
export interface ProviderBatchRow {
  model: string // selected provider model identifier
  state: ProviderBatchRowState // current scheduler row state
  run: ProviderTestRun // mutable model test output
}

/**
 * Translate resolves localized provider-test messages with optional interpolation values.
 */
type Translate = (key: string, params?: Record<string, unknown>) => string

/**
 * useProviderBatchTest creates selection state, result rows, and a worker scheduler for one
 * dialog. Callers pass a translation function and use the returned abort operation when the
 * owning dialog closes or unmounts. The returned refs remain shared with the panel that renders
 * them.
 *
 * Each start snapshots target order and shares one cancellation signal across its workers. Stop
 * prevents another target from starting while current requests continue; abort also interrupts
 * those requests. Reset clears displayed rows immediately, so callers must account for any
 * pending worker settlement before treating the next lifecycle as independent.
 */
export function useProviderBatchTest(t: Translate) {
  // Catalogs and selection belong to this dialog instance; result rows remain available when
  // the administrator changes the selected set.
  const baseModels = ref<string[]>([])
  const customModels = ref<string[]>([])
  const selected = ref(new Set<string>())
  const rows = ref<Record<string, ProviderBatchRow>>({})
  const running = ref(false)
  const stopping = ref(false)
  const concurrency = ref(2)
  const detailModel = ref<string | null>(null)
  let stopRequested = false
  let controller: AbortController | null = null

  // Display summaries derive from retained rows, so failed retries can coexist with successful
  // results from an earlier batch.
  const models = computed(() => [...new Set([...baseModels.value, ...customModels.value])])
  const entries = computed(() => Object.values(rows.value))
  const doneCount = computed(
    () => entries.value.filter((row) => row.state === 'success' || row.state === 'failed').length,
  )
  const successCount = computed(() => entries.value.filter((row) => row.state === 'success').length)
  const failedModels = computed(() =>
    entries.value.filter((row) => row.state === 'failed').map((row) => row.model),
  )
  const stoppedCount = computed(() => entries.value.filter((row) => row.state === 'stopped').length)
  const progress = computed(() =>
    entries.value.length
      ? (100 * (doneCount.value + stoppedCount.value)) / entries.value.length
      : 0,
  )
  const detailRow = computed(() =>
    detailModel.value ? (rows.value[detailModel.value] ?? null) : null,
  )

  /**
   * reset aborts current requests and replaces the model catalog, selection, results, and
   * detail view. Request settlement is asynchronous; this function returns immediately after
   * clearing the displayed state.
   */
  function reset(nextModels: string[]) {
    abort()
    baseModels.value = nextModels
    customModels.value = []
    selected.value = new Set()
    rows.value = {}
    detailModel.value = null
  }

  /**
   * toggle adds or removes one selected model using a replacement Set. Additions stop at
   * MAX_BATCH_MODELS, while removals remain available even when the selection has reached that
   * limit.
   */
  function toggle(model: string, checked: boolean) {
    const next = new Set(selected.value)
    if (checked && next.size < MAX_BATCH_MODELS) next.add(model)
    if (!checked) next.delete(model)
    selected.value = next
  }

  /**
   * setMany updates selection for supplied targets in their given order. The shared selection
   * limit applies to additions, so earlier targets consume the available slots when selecting a
   * filtered list.
   */
  function setMany(targets: string[], checked: boolean) {
    const next = new Set(selected.value)
    for (const model of targets) {
      if (!checked) next.delete(model)
      else if (next.size < MAX_BATCH_MODELS) next.add(model)
    }
    selected.value = next
  }

  /**
   * clearSelection replaces the selected-model set with a fresh empty selection.
   */
  function clearSelection() {
    selected.value = new Set()
  }

  /**
   * addModel adds a nonempty, previously unknown model to the custom catalog and attempts
   * selection. The model remains in the catalog even if the selection limit prevents selecting
   * it.
   */
  function addModel(model: string) {
    if (!model || models.value.includes(model)) return
    customModels.value = [...customModels.value, model]
    toggle(model, true)
  }

  /**
   * start executes the supplied model targets with the configured worker count and retains
   * results for inspection. An active run or empty target list produces no work. Retry replaces
   * targeted rows and keeps the remaining rows, while a fresh run replaces the complete result
   * map.
   *
   * Workers claim target indices synchronously before awaiting their streams, so each index is
   * assigned once within this invocation. Stop prevents subsequent claims; active calls finish
   * or observe the shared abort signal. After all workers settle, unclaimed targets become
   * stopped. Callers provide unique target identifiers and coordinate reset with settlement
   * because rows are accessed through the shared map.
   */
  async function start(options: {
    targets: string[] // ordered model test identifiers
    retry: boolean // retain untargeted result rows
    providerId: number // provider endpoint record identifier
    providerName: string // provider label for logs
    /**
     * Builds each request when its worker starts; callers keep any referenced form state
     * suitable for the batch.
     */
    buildBody: (model: string) => ProviderTestRequestBody
  }) {
    if (running.value || options.targets.length === 0) return
    stopRequested = false
    stopping.value = false
    running.value = true
    detailModel.value = null
    controller = new AbortController()
    const signal = controller.signal

    const queued: Record<string, ProviderBatchRow> = {}
    for (const model of options.targets) {
      queued[model] = { model, state: 'queued', run: createProviderTestRun() }
    }
    // Replace retried rows before workers start so every dispatched model writes into its fresh
    // run while other results stay inspectable.
    rows.value = options.retry ? { ...rows.value, ...queued } : queued

    const targets = [...options.targets]
    let index = 0
    /**
     * worker claims a target and awaits its stream before taking another target from this batch.
     * The shared cursor advances synchronously before the await, so concurrent workers claim
     * different indices within the invocation. Each claimed row remains running until the
     * executor reports a completed attempt or cancellation.
     *
     * A stop request prevents later claims while current streams settle. Cancellation marks the
     * claimed row stopped; completed attempts use the executor terminal status to choose success
     * or failed. Rows and cursor belong to the surrounding start invocation, whose caller must
     * coordinate resets of the shared row map.
     */
    const worker = async () => {
      while (!stopRequested && index < targets.length) {
        const row = rows.value[targets[index++]]
        row.state = 'running'
        const finished = await executeProviderTest({
          providerId: options.providerId,
          providerName: options.providerName,
          body: options.buildBody(row.model),
          run: row.run,
          signal,
          t,
        })
        if (!finished) {
          row.state = 'stopped'
        } else {
          row.state = row.run.status === 'success' ? 'success' : 'failed'
        }
      }
    }

    try {
      // Wait for every worker before final queue classification, including active requests
      // allowed to finish after graceful stop was requested.
      await Promise.all(Array.from({ length: Math.min(concurrency.value, targets.length) }, worker))
      // Workers have settled before queued rows are marked stopped, so completed rows keep
      // their success or failure outcome.
      for (; index < targets.length; index++) {
        rows.value[targets[index]].state = 'stopped'
      }
    } finally {
      running.value = false
      stopping.value = false
      controller = null
    }
  }

  /**
   * showDetail selects the result row displayed by the batch panel. Passing null returns to the
   * model list, and an unknown model resolves to an empty detail row.
   */
  function showDetail(model: string | null) {
    detailModel.value = model
  }

  /**
   * stop requests a graceful end to the active batch and exposes the stopping indicator.
   * Already dispatched requests continue, while workers stop taking queued targets after their
   * current requests settle.
   */
  function stop() {
    if (!running.value) return
    stopRequested = true
    stopping.value = true
  }

  /**
   * abort prevents further target dispatch and aborts the shared signal for active requests.
   * Callers still wait for worker settlement to observe running and stopping return to false.
   */
  function abort() {
    stopRequested = true
    // Signal cancellation reaches all active fetch calls; worker settlement performs the later
    // running-state cleanup after their promises resolve.
    controller?.abort()
  }

  return {
    models,
    selected,
    rows,
    running,
    stopping,
    concurrency,
    detailModel,
    detailRow,
    entries,
    doneCount,
    successCount,
    failedModels,
    stoppedCount,
    progress,
    reset,
    toggle,
    setMany,
    clearSelection,
    addModel,
    showDetail,
    start,
    stop,
    abort,
  }
}

/**
 * ProviderBatchTest exposes the refs and operations owned by the batch scheduler.
 */
export type ProviderBatchTest = ReturnType<typeof useProviderBatchTest>
