import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import apiClient from '@/api/client'
import {
  loadProtocolCatalog,
  protocolCatalog,
  protocolCatalogError,
  protocolCatalogLoading,
} from '../protocolCapabilities'
import fixture from '@/__tests__/fixtures/protocol-catalog.json'

/**
 * protocolCapabilities.spec.ts checks shared protocol-catalog loading from an
 * empty cache. Controlled request settlement exposes concurrent promise reuse,
 * loading and error transitions, cached success and recovery after a failed
 * request releases the pending slot.
 */

/**
 * Clear the exported catalog, loading and error refs so a cached fixture cannot
 * bypass the request sharing and failure recovery exercised by each case.
 */
beforeEach(() => {
  protocolCatalog.value = null
  protocolCatalogError.value = false
  protocolCatalogLoading.value = false
})
afterEach(() => vi.restoreAllMocks())

describe('协议目录加载', () => {
  /**
   * Two loads begin before the catalog request completes. They share one
   * promise, publish success state together and reuse the cached catalog on a
   * later load.
   */
  it('并发调用共享请求，加载成功后复用缓存', async () => {
    /**
     * Hold catalog settlement until both loads have started so their promise
     * identity and request count expose reuse of the pending request.
     */
    let resolve!: (value: {
      data: typeof fixture // Returned response data.
    }) => void
    const request = vi.spyOn(apiClient, 'get').mockImplementation(
      () =>
        new Promise((done) => {
          resolve = done
        }),
    )
    const first = loadProtocolCatalog()
    const second = loadProtocolCatalog()
    expect(first).toBe(second)
    expect(protocolCatalogLoading.value).toBe(true)
    expect(request).toHaveBeenCalledTimes(1)
    resolve({ data: structuredClone(fixture) })
    await first
    expect(protocolCatalogLoading.value).toBe(false)
    expect(protocolCatalogError.value).toBe(false)
    expect(await loadProtocolCatalog()).toEqual(fixture)
    expect(request).toHaveBeenCalledTimes(1)
  })

  /**
   * The first catalog request fails and the next one succeeds. Error state
   * clears when retry starts, loading tracks settlement and the successful
   * catalog becomes available.
   */
  it('失败不缓存空目录，重试清除错误并恢复数据', async () => {
    const request = vi
      .spyOn(apiClient, 'get')
      .mockRejectedValueOnce(new Error('offline'))
      .mockResolvedValueOnce({ data: structuredClone(fixture) })
    await expect(loadProtocolCatalog()).rejects.toThrow('offline')
    expect(protocolCatalog.value).toBeNull()
    expect(protocolCatalogError.value).toBe(true)
    expect(protocolCatalogLoading.value).toBe(false)
    const retry = loadProtocolCatalog()
    expect(protocolCatalogError.value).toBe(false)
    expect(protocolCatalogLoading.value).toBe(true)
    await retry
    expect(protocolCatalog.value).toEqual(fixture)
    expect(request).toHaveBeenCalledTimes(2)
  })
})
