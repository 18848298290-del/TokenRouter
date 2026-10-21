import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get } provide the controlled request fixtures used by
 * admin.groups.usage-summary tests to inspect their observable results.
 */
const { get } = vi.hoisted(() => ({
  get: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { get },
}))

import { getUsageSummary } from '@/api/admin/groups'

/**
 * admin.groups.usage-summary.spec.ts checks the administrator group
 * usage-summary wrapper with a mocked client. The assertion protects its
 * endpoint and absence of per-call options; shared-client interceptor behavior
 * is exercised separately in client.spec.ts.
 * The mock returns the supplied cost summary unchanged.
 */

describe('管理员分组用量汇总 API', () => {
  beforeEach(() => {
    get.mockReset()
    get.mockResolvedValue({ data: [] })
  })

  /**
   * The group usage-summary wrapper is called with a mocked shared client. It
   * supplies no per-call options and returns the summary; shared-client timezone
   * injection is outside this test.
   */
  it('不发送浏览器时区参数', async () => {
    const summary = [{ group_id: 1, today_cost: 1.25, yesterday_cost: 2.5, total_cost: 9.75 }]
    get.mockResolvedValue({ data: summary })

    await expect(getUsageSummary()).resolves.toEqual(summary)

    expect(get).toHaveBeenCalledWith('/admin/groups/usage-summary')
  })
})
