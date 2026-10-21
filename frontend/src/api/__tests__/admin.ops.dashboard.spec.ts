import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get } provide the controlled request fixtures used by admin.ops.dashboard
 * tests to inspect their observable results.
 */
const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get },
  buildGatewayUrl: vi.fn(),
}))

import { getLatencyHistogram, getTokenStats } from '@/api/admin/ops'

/**
 * admin.ops.dashboard.spec.ts checks operations dashboard request serialization.
 * Histogram boundaries are joined into the server query format, and token
 * statistics retain the selected interval and ownership filters at their
 * dedicated dashboard endpoint.
 */

describe('admin ops dashboard API', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: {} })
  })

  /**
   * Histogram queries include numeric duration bucket boundaries and ordinary
   * filters. The wrapper joins boundaries into one comma-separated parameter
   * while retaining the other query fields.
   */
  it('将时长边界序列化为后端约定的逗号参数', async () => {
    await getLatencyHistogram({
      time_range: '1h',
      group_id: 7,
      bucket_boundaries_ms: [1000, 2000, 5000, 10000, 20000],
    })

    expect(get).toHaveBeenCalledWith('/admin/ops/dashboard/latency-histogram', {
      params: {
        time_range: '1h',
        group_id: 7,
        bucket_boundaries_ms: '1000,2000,5000,10000,20000',
      },
      signal: undefined,
    })
  })

  /**
   * Token statistics include time range, platform, group and ranking size
   * filters. The wrapper sends them to the common token-statistics route with
   * its cancellation option slot.
   */
  it('Token 统计使用新的通用路由', async () => {
    await getTokenStats({ time_range: '30d', platform: 'anthropic', group_id: 7, top_n: 20 })

    expect(get).toHaveBeenCalledWith('/admin/ops/dashboard/token-stats', {
      params: {
        time_range: '30d',
        platform: 'anthropic',
        group_id: 7,
        top_n: 20,
      },
      signal: undefined,
    })
  })
})
