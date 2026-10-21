import type { Provider } from '@/api/admin/providers.types'

/**
 * usageLoadQueue.ts serializes Anthropic OAuth and setup-token usage requests
 * by proxy connection identity. Providers sharing host, port, and username
 * share one queue, including a direct queue for providers without a proxy.
 * Proxy record IDs and passwords do not participate in this grouping.
 *
 * Each group has one drain loop, while different groups progress
 * independently. Requests already waiting receive a randomized delay after the
 * preceding request settles, including failures. The delay reduces request
 * bursts but cannot guarantee upstream rate-limit acceptance. The queue has no
 * cancellation interface, so callers remain responsible for deciding whether a
 * completed result still belongs to their current provider.
 */

/**
 * GROUP_DELAY_MIN_MS and GROUP_DELAY_MAX_MS bound randomized pauses between
 * waiting requests in the same Anthropic group.
 */
const GROUP_DELAY_MIN_MS = 1000
const GROUP_DELAY_MAX_MS = 2000

/**
 * Task pairs a deferred usage callback with its caller promise continuations.
 * The owning group invokes fn once after admission, then settles resolve or
 * reject before advancing to another queued callback.
 */
type Task<T> = {
  fn: () => Promise<T> // deferred usage callback
  resolve: (value: T) => void // caller success continuation
  reject: (reason: unknown) => void // caller failure continuation
}

/**
 * queues stores pending callbacks by proxy identity, while running marks
 * groups whose single drain loop already owns admission and eventual cleanup.
 */
const queues = new Map<string, Task<unknown>[]>()
const running = new Set<string>()

/**
 * needsThrottle reports whether the provider uses an Anthropic credential type
 * subject to serialized usage requests.
 */
function needsThrottle(provider: Provider): boolean {
  return (
    provider.platform === 'anthropic' &&
    (provider.type === 'oauth' || provider.type === 'setup-token')
  )
}

/**
 * buildGroupKey identifies the Anthropic queue from proxy host, port, and
 * username. Providers without a proxy share the direct queue; distinct proxy
 * records with identical connection fields therefore share scheduling state.
 */
function buildGroupKey(provider: Provider): string {
  const proxy = provider.proxy
  const proxyIdentity = proxy ? `${proxy.host}:${proxy.port}:${proxy.username || ''}` : 'direct'
  return `anthropic:${proxyIdentity}`
}

/**
 * drain runs queued tasks sequentially for one group and settles each task's
 * own promise. A running marker prevents another enqueue from starting a
 * competing drain for the same group.
 *
 * The loop retains its queue across awaits, so newly appended tasks join the
 * existing sequence. A failed task rejects its caller but does not abandon
 * later work. Delay applies only while more tasks remain; after the queue
 * empties, both map membership and the running marker are removed.
 */
async function drain(groupKey: string) {
  if (running.has(groupKey)) return
  /**
   * Claim the group before awaiting any callback so another enqueue reuses this
   * drain instead of starting competing work.
   */
  running.add(groupKey)

  const queue = queues.get(groupKey)
  while (queue && queue.length > 0) {
    const task = queue.shift()!
    try {
      const result = await task.fn()
      task.resolve(result)
    } catch (err) {
      /**
       * Reject the failed task without ending this group drain, so later waiting
       * callers still run after their configured delay.
       */
      task.reject(err)
    }
    if (queue.length > 0) {
      /**
       * Delay only when another task is already waiting; an idle group is removed
       * instead of retaining a cooldown timer.
       */
      const jitter = GROUP_DELAY_MIN_MS + Math.random() * (GROUP_DELAY_MAX_MS - GROUP_DELAY_MIN_MS)
      await new Promise((r) => setTimeout(r, jitter))
    }
  }

  /**
   * Release group ownership together with the empty queue after all admitted
   * callbacks settle, allowing future requests to start a fresh drain.
   */
  running.delete(groupKey)
  queues.delete(groupKey)
}

/**
 * enqueueUsageRequest schedules a usage callback and returns its result
 * promise. Anthropic OAuth and setup-token callbacks join a queue selected
 * from the provider's current proxy fields; other callbacks execute
 * immediately.
 *
 * Enqueueing starts the drain unless one already owns that group, so callers
 * do not manage queue startup or release. Queued callback failures reject
 * their individual promises. There is no removal or abort argument: a callback
 * remains queued even if its requesting component disappears.
 */
export function enqueueUsageRequest<T>(provider: Provider, fn: () => Promise<T>): Promise<T> {
  if (!needsThrottle(provider)) {
    return fn()
  }

  const key = buildGroupKey(provider)

  return new Promise<T>((resolve, reject) => {
    let queue = queues.get(key)
    if (!queue) {
      queue = []
      queues.set(key, queue)
    }
    queue.push({ fn, resolve, reject } as Task<unknown>)
    drain(key)
  })
}
