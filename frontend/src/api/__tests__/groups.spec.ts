import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get } provide the controlled request fixtures used by groups tests to
 * inspect their observable results.
 */
const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { get } }))

import { getAvailable } from '@/api/groups'

/**
 * groups.spec.ts checks available-group query options and display-name mapping.
 * Mocked responses retain the canonical group name while exposing its display
 * name, and request assertions distinguish default model expansion from an
 * explicit request to skip it.
 */

describe('available groups', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: [{ id: 1, name: 'internal', display_name: 'Display' }] })
  })

  /**
   * Available groups are queried with a subscription and then with model
   * expansion disabled. Display names retain canonical names, while the optional
   * include_models flag follows the caller selection.
   */
  it('默认保留模型目录，筛选查询可以跳过模型解析', async () => {
    const groups = await getAvailable('personal', 7)
    expect(get).toHaveBeenLastCalledWith('/groups/available', {
      params: { scope: 'personal', subscription_id: 7 },
    })
    expect(groups[0]).toMatchObject({ name: 'Display', canonical_name: 'internal' })

    await getAvailable('personal', undefined, false)
    expect(get).toHaveBeenLastCalledWith('/groups/available', {
      params: { scope: 'personal', include_models: false },
    })
  })
})
