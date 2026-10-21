import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { useKeyedDebouncedSearch } from '@/composables/useKeyedDebouncedSearch'

// useKeyedDebouncedSearch.spec.ts checks per-key scheduling and completion publication
// using fake timers and manually resolved requests. Separate keys must remain independent,
// and a late response from a replaced search must not reach the success consumer.

// flushPromises yields one microtask turn for resolved search callbacks to run.
const flushPromises = () => Promise.resolve()

describe('useKeyedDebouncedSearch', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // Two different keys receive keywords before their shared debounce duration elapses.
  // Each must dispatch once with its own key and cancellation signal after time advances.
  it('为不同 key 独立防抖触发搜索', async () => {
    const search = vi.fn().mockResolvedValue([])
    const onSuccess = vi.fn()

    const searcher = useKeyedDebouncedSearch<string[]>({
      delay: 100,
      search,
      onSuccess,
    })

    searcher.trigger('a', 'foo')
    searcher.trigger('b', 'bar')

    expect(search).not.toHaveBeenCalled()

    vi.advanceTimersByTime(100)
    await flushPromises()

    expect(search).toHaveBeenCalledTimes(2)
    expect(search).toHaveBeenNthCalledWith(
      1,
      'foo',
      expect.objectContaining({ key: 'a', signal: expect.any(AbortSignal) }),
    )
    expect(search).toHaveBeenNthCalledWith(
      2,
      'bar',
      expect.objectContaining({ key: 'b', signal: expect.any(AbortSignal) }),
    )
    expect(onSuccess).toHaveBeenCalledTimes(2)
  })

  // One key starts a second search while its first promise remains pending, then resolves
  // in reverse order. Only the second result may reach onSuccess.
  it('同 key 新请求会取消旧请求并忽略过期响应', async () => {
    const resolves: Array<(value: string[]) => void> = []
    const search = vi.fn().mockImplementation(
      () =>
        new Promise<string[]>((resolve) => {
          resolves.push(resolve)
        }),
    )
    const onSuccess = vi.fn()

    const searcher = useKeyedDebouncedSearch<string[]>({
      delay: 50,
      search,
      onSuccess,
    })

    searcher.trigger('rule-1', 'first')
    vi.advanceTimersByTime(50)
    await flushPromises()

    searcher.trigger('rule-1', 'second')
    vi.advanceTimersByTime(50)
    await flushPromises()

    expect(search).toHaveBeenCalledTimes(2)

    resolves[1](['second'])
    await flushPromises()
    expect(onSuccess).toHaveBeenCalledTimes(1)
    expect(onSuccess).toHaveBeenLastCalledWith('rule-1', ['second'])

    resolves[0](['first'])
    await flushPromises()
    expect(onSuccess).toHaveBeenCalledTimes(1)
  })

  // A keyword is scheduled and its key is cleared before debounce expiry. Advancing time
  // must invoke neither the search function nor the success callback.
  it('clearKey 会取消未执行任务', () => {
    const search = vi.fn().mockResolvedValue([])
    const onSuccess = vi.fn()

    const searcher = useKeyedDebouncedSearch<string[]>({
      delay: 100,
      search,
      onSuccess,
    })

    searcher.trigger('a', 'foo')
    searcher.clearKey('a')

    vi.advanceTimersByTime(100)

    expect(search).not.toHaveBeenCalled()
    expect(onSuccess).not.toHaveBeenCalled()
  })
})
