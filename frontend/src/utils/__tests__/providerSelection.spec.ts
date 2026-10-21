import { describe, expect, it, vi } from 'vitest'

import { fetchAllProviderIds } from '../providerSelection'

/**
 * providerSelection.spec.ts tests asynchronous collection of all requested
 * provider pages. Page fixtures check shared filters and lightweight flags,
 * while duplicate identifiers and a later page failure exercise rejection of
 * incomplete selection results.
 */

describe('fetchAllProviderIds', () => {
  /**
   * A filtered provider result spans three pages and 2,505 items. Selection requests
   * every page with lightweight flags and returns IDs in the complete encounter order.
   */
  it('loads every page with the same filter snapshot and returns unique provider IDs', async () => {
    const fetchPage = vi.fn(
      async (page: number, pageSize: number, _filters: Record<string, unknown>) => {
        const start = (page - 1) * pageSize + 1
        const end = Math.min(page * pageSize, 2505)
        return {
          items: Array.from({ length: end - start + 1 }, (_, index) => ({ id: start + index })),
          total: 2505,
          page,
          page_size: pageSize,
          pages: 3,
        }
      },
    )

    const filters = {
      platform: 'grok',
      status: 'active',
      search: 'example',
    }

    const ids = await fetchAllProviderIds(fetchPage, filters)

    expect(ids).toHaveLength(2505)
    expect(ids[0]).toBe(1)
    expect(ids[2504]).toBe(2505)
    expect(fetchPage).toHaveBeenCalledTimes(3)
    expect(fetchPage).toHaveBeenNthCalledWith(1, 1, 1000, {
      ...filters,
      lite: '1',
      include_scheduler_score: '0',
    })
    expect(fetchPage).toHaveBeenNthCalledWith(3, 3, 1000, {
      ...filters,
      lite: '1',
      include_scheduler_score: '0',
    })
  })

  /**
   * A result repeats one ID while claiming two matching providers. Deduplication
   * exposes the missing unique provider and rejects the selection instead of returning
   * an incomplete ID list.
   */
  it('rejects an incomplete or duplicated result instead of returning a partial selection', async () => {
    const fetchPage = vi.fn().mockResolvedValue({
      items: [{ id: 1 }, { id: 1 }],
      total: 2,
      page: 1,
      page_size: 1000,
      pages: 1,
    })

    await expect(fetchAllProviderIds(fetchPage, {})).rejects.toThrow('提供商列表结果不完整')
  })

  /**
   * The first page succeeds and the next required page rejects. The selection promise
   * propagates that failure so callers can retain their previously established selected
   * providers.
   */
  it('propagates a later page failure', async () => {
    const fetchPage = vi
      .fn()
      .mockResolvedValueOnce({
        items: Array.from({ length: 1000 }, (_, index) => ({ id: index + 1 })),
        total: 1001,
        page: 1,
        page_size: 1000,
        pages: 2,
      })
      .mockRejectedValueOnce(new Error('page 2 failed'))

    await expect(fetchAllProviderIds(fetchPage, { group: '7' })).rejects.toThrow('page 2 failed')
  })
})
