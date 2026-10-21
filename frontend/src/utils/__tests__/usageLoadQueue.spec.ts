import { describe, expect, it } from 'vitest'
import { enqueueUsageRequest } from '../usageLoadQueue'
import type { Provider } from '@/api/admin/providers.types'

/**
 * usageLoadQueue.spec.ts checks usage scheduling by provider type and proxy
 * connection identity. Real-clock assertions distinguish serialized Anthropic
 * credential requests from independent groups and immediate bypasses, while
 * rejection and result cases verify that queue settlement remains local to
 * each caller.
 */

/**
 * makeProvider creates a minimal provider fixture with optional proxy
 * connection details. OAuth is the default credential type, and each ID is
 * sampled independently from a finite random range; proxy connection fields
 * determine the shared queue in these tests.
 */
function makeProvider(
  platform: string,
  type: string = 'oauth',
  proxy?: {
    host: string // proxy connection host
    port: number // proxy connection port
    username?: string | null // proxy authentication username
  } | null,
): Provider {
  return {
    /**
     * Fixture IDs vary independently of proxy identity grouping.
     */
    id: Math.floor(Math.random() * 10000),
    platform,
    type,
    name: 'test',
    status: 'active',
    proxy_id: proxy ? 1 : null,
    proxy: proxy
      ? {
          id: 1,
          name: 'p',
          protocol: 'http',
          host: proxy.host,
          port: proxy.port,
          username: proxy.username ?? null,
          status: 'active',
          created_at: '',
          updated_at: '',
        }
      : undefined,
    credentials: {},
    created_at: '',
    updated_at: '',
  } as unknown as Provider
}

describe('usageLoadQueue', () => {
  /**
   * Three Anthropic OAuth requests share one proxy identity and are queued
   * together. Their recorded start times must remain serial with gaps near the
   * configured randomized delay range.
   */
  it('Anthropic 同代理出口串行执行，间隔 >= 1s', async () => {
    const timestamps: number[] = []
    /**
     * makeFn creates a callback that records execution in the current test
     * collection.
     */
    const makeFn = () => async () => {
      timestamps.push(Date.now())
      return 'ok'
    }

    const acc = makeProvider('anthropic', 'oauth', { host: '1.2.3.4', port: 8080, username: 'u1' })

    const p1 = enqueueUsageRequest(acc, makeFn())
    const p2 = enqueueUsageRequest(acc, makeFn())
    const p3 = enqueueUsageRequest(acc, makeFn())

    await Promise.all([p1, p2, p3])

    expect(timestamps).toHaveLength(3)
    /**
     * The timing assertion allows small clock measurement tolerance below the
     * nominal delay while retaining a separate upper bound for each observed gap.
     */
    expect(timestamps[1] - timestamps[0]).toBeGreaterThanOrEqual(950)
    expect(timestamps[1] - timestamps[0]).toBeLessThan(2100)
    expect(timestamps[2] - timestamps[1]).toBeGreaterThanOrEqual(950)
    expect(timestamps[2] - timestamps[1]).toBeLessThan(2100)
  })

  /**
   * Two Anthropic providers use different proxy connection identities. Their
   * callbacks must start close together so a busy group does not serialize
   * independent proxy groups.
   */
  it('Anthropic 不同代理出口并行执行', async () => {
    const timestamps: Record<string, number> = {}
    /**
     * makeTracked creates a callback that records its execution timestamp under
     * the supplied label.
     */
    const makeTracked = (key: string) => async () => {
      timestamps[key] = Date.now()
      return key
    }

    const acc1 = makeProvider('anthropic', 'oauth', { host: '1.2.3.4', port: 8080, username: 'u1' })
    const acc2 = makeProvider('anthropic', 'oauth', { host: '5.6.7.8', port: 3128, username: 'u2' })

    const p1 = enqueueUsageRequest(acc1, makeTracked('proxy1'))
    const p2 = enqueueUsageRequest(acc2, makeTracked('proxy2'))

    await Promise.all([p1, p2])

    /**
     * Independent groups have no intentional scheduling delay; the short tolerance
     * checks their starts without requiring identical millisecond timestamps from
     * the wall clock.
     */
    const spread = Math.abs(timestamps['proxy1'] - timestamps['proxy2'])
    expect(spread).toBeLessThan(50)
  })

  /**
   * Different Anthropic providers share host, port, and username but use
   * different credential types. Their requests must share one serial queue
   * despite separate provider records.
   */
  it('Anthropic 相同代理连接信息的不同提供商归为同一队列', async () => {
    const timestamps: number[] = []
    /**
     * makeFn creates a callback that records execution in the current test
     * collection.
     */
    const makeFn = () => async () => {
      timestamps.push(Date.now())
      return 'ok'
    }

    const acc1 = makeProvider('anthropic', 'oauth', {
      host: '10.0.0.1',
      port: 3128,
      username: 'admin',
    })
    const acc2 = makeProvider('anthropic', 'setup-token', {
      host: '10.0.0.1',
      port: 3128,
      username: 'admin',
    })

    const p1 = enqueueUsageRequest(acc1, makeFn())
    const p2 = enqueueUsageRequest(acc2, makeFn())

    await Promise.all([p1, p2])

    expect(timestamps).toHaveLength(2)
    expect(timestamps[1] - timestamps[0]).toBeGreaterThanOrEqual(950)
  })

  /**
   * Two eligible Anthropic providers have no proxy configured at all. Their
   * callbacks must execute in submission order through the common
   * direct-connection queue used by this scheduler.
   */
  it('Anthropic 直连（无代理）的提供商归为同一队列', async () => {
    const order: number[] = []
    /**
     * makeFn creates a callback that records execution in the current test
     * collection.
     */
    const makeFn = (n: number) => async () => {
      order.push(n)
      return n
    }

    const acc1 = makeProvider('anthropic', 'oauth')
    const acc2 = makeProvider('anthropic', 'setup-token')

    const p1 = enqueueUsageRequest(acc1, makeFn(1))
    const p2 = enqueueUsageRequest(acc2, makeFn(2))

    await Promise.all([p1, p2])

    expect(order).toEqual([1, 2])
  })

  /**
   * The first queued Anthropic callback rejects while a second callback waits.
   * The first caller must receive its error and the second callback must still
   * execute.
   */
  it('Anthropic 请求失败时 reject，后续任务继续执行', async () => {
    const results: string[] = []
    const acc = makeProvider('anthropic', 'oauth', { host: '99.99.99.99', port: 1234 })

    const p1 = enqueueUsageRequest(acc, async () => {
      throw new Error('fail')
    })
    const p2 = enqueueUsageRequest(acc, async () => {
      results.push('second')
      return 'ok'
    })

    await expect(p1).rejects.toThrow('fail')
    await p2
    expect(results).toEqual(['second'])
  })

  /**
   * Gemini providers share a proxy connection that would group eligible
   * Anthropic requests. Both callbacks must execute immediately without
   * introducing the Anthropic queue delay between them.
   */
  it('非 Anthropic 平台直接执行，不排队', async () => {
    const timestamps: number[] = []
    /**
     * makeFn creates a callback that records execution in the current test
     * collection.
     */
    const makeFn = () => async () => {
      timestamps.push(Date.now())
      return 'ok'
    }

    const acc1 = makeProvider('gemini', 'oauth', { host: '1.2.3.4', port: 8080 })
    const acc2 = makeProvider('gemini', 'oauth', { host: '1.2.3.4', port: 8080 })

    const p1 = enqueueUsageRequest(acc1, makeFn())
    const p2 = enqueueUsageRequest(acc2, makeFn())

    await Promise.all([p1, p2])

    expect(timestamps).toHaveLength(2)

    expect(Math.abs(timestamps[1] - timestamps[0])).toBeLessThan(50)
  })

  /**
   * OpenAI OAuth providers share the same proxy connection information. Both
   * callbacks must execute immediately, confirming that scheduling depends on
   * platform as well as credential type.
   */
  it('OpenAI 平台直接执行，不排队', async () => {
    const timestamps: number[] = []
    /**
     * makeFn creates a callback that records execution in the current test
     * collection.
     */
    const makeFn = () => async () => {
      timestamps.push(Date.now())
      return 'ok'
    }

    const acc1 = makeProvider('openai', 'oauth', { host: '1.2.3.4', port: 8080 })
    const acc2 = makeProvider('openai', 'oauth', { host: '1.2.3.4', port: 8080 })

    const p1 = enqueueUsageRequest(acc1, makeFn())
    const p2 = enqueueUsageRequest(acc2, makeFn())

    await Promise.all([p1, p2])

    expect(timestamps).toHaveLength(2)
    expect(Math.abs(timestamps[1] - timestamps[0])).toBeLessThan(50)
  })

  /**
   * Anthropic providers use API-key credentials instead of OAuth or setup
   * tokens. Their callbacks must bypass queuing despite sharing the same proxy
   * connection identity.
   */
  it('Anthropic apikey 类型直接执行，不排队', async () => {
    const timestamps: number[] = []
    /**
     * makeFn creates a callback that records execution in the current test
     * collection.
     */
    const makeFn = () => async () => {
      timestamps.push(Date.now())
      return 'ok'
    }

    const acc1 = makeProvider('anthropic', 'apikey', { host: '1.2.3.4', port: 8080 })
    const acc2 = makeProvider('anthropic', 'apikey', { host: '1.2.3.4', port: 8080 })

    const p1 = enqueueUsageRequest(acc1, makeFn())
    const p2 = enqueueUsageRequest(acc2, makeFn())

    await Promise.all([p1, p2])

    expect(timestamps).toHaveLength(2)
    expect(Math.abs(timestamps[1] - timestamps[0])).toBeLessThan(50)
  })

  /**
   * An eligible Anthropic callback returns a structured usage object. The queued
   * promise must resolve with that object so scheduling does not replace or
   * discard the callback result.
   */
  it('返回值正确透传', async () => {
    const acc = makeProvider('anthropic', 'oauth')
    const result = await enqueueUsageRequest(acc, async () => {
      return { usage: 42 }
    })
    expect(result).toEqual({ usage: 42 })
  })

  /**
   * A Gemini callback returns a structured quota object through the bypass path.
   * The returned promise must preserve that value just as the queued path
   * preserves its result.
   */
  it('非 Anthropic 返回值正确透传', async () => {
    const acc = makeProvider('gemini', 'oauth')
    const result = await enqueueUsageRequest(acc, async () => {
      return { quota: 100 }
    })
    expect(result).toEqual({ quota: 100 })
  })
})
