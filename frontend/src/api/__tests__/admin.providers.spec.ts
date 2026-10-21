import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get, post } provide the controlled request fixtures used by admin.providers
 * tests to inspect their observable results.
 */
const { get, post } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post,
  },
}))

import {
  consumeCodexInviteReset,
  getAdvancedSchedulerScore,
  previewAdvancedSchedulerScore,
  queryBatchUpstreamUsage,
  queryUpstreamUsage,
  syncFromCrs,
} from '@/api/admin/providers'

/**
 * admin.providers.spec.ts checks provider diagnostic, synchronization, reset and
 * upstream-usage requests. Mocked methods expose submitted payloads and
 * timeouts, allowing the cases to distinguish omitted optional selection from a
 * chosen group or reset credit.
 */

describe('admin providers API', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
  })

  /**
   * Scheduler diagnostics are requested with and without a selected group. The
   * dedicated endpoint receives an omitted query or the group identifier and
   * returns the supplied diagnostic response.
   */
  it('loads an overview or a single advanced scheduler score group through the dedicated endpoint', async () => {
    const response = { groups: [] }
    get.mockResolvedValue({ data: response })

    await expect(getAdvancedSchedulerScore(42)).resolves.toEqual(response)
    expect(get).toHaveBeenLastCalledWith('/admin/providers/42/advanced-scheduler-score', {
      params: undefined,
    })

    await expect(getAdvancedSchedulerScore(42, 9)).resolves.toEqual(response)
    expect(get).toHaveBeenLastCalledWith('/admin/providers/42/advanced-scheduler-score', {
      params: { group_id: 9 },
    })
  })

  /**
   * A scheduler score preview receives a prepared request scenario. The wrapper
   * forwards that exact payload to the provider preview endpoint, protecting the
   * client's request field selection.
   */
  it('sends only the safe score-preview context fields', async () => {
    const payload = {
      group_id: 9,
      requested_model: 'gpt-5',
      sticky_provider_id: 42,
      previous_response_provider_id: 43,
    }
    post.mockResolvedValue({ data: { detail: {} } })

    await previewAdvancedSchedulerScore(42, payload)

    expect(post).toHaveBeenCalledWith(
      '/admin/providers/42/advanced-scheduler-score/preview',
      payload,
    )
  })

  /**
   * CRS synchronization receives selected provider IDs and connection settings.
   * The request forwards them with a three-minute timeout and returns the batch
   * outcome unchanged.
   */
  it('uses a dedicated 180 second timeout for CRS synchronization', async () => {
    const payload = {
      base_url: 'https://crs.example.com',
      username: 'admin',
      password: 'secret',
      sync_proxies: true,
      selected_provider_ids: ['crs-1'],
    }
    const response = {
      created: 1,
      updated: 0,
      skipped: 0,
      failed: 0,
      items: [],
    }
    post.mockResolvedValue({ data: response })

    const result = await syncFromCrs(payload)

    expect(post).toHaveBeenCalledWith('/admin/providers/sync/crs', payload, {
      timeout: 180_000,
    })
    expect(result).toEqual(response)
  })

  /**
   * A Codex reset call includes a padded credit identifier. The outgoing payload
   * contains the trimmed selected credit and the reset result is returned to the
   * caller.
   */
  it('Codex 重置有明细选择时发送 credit_id', async () => {
    const response = {
      code: 'reset',
      credit_id: 'credit-1',
      redeem_request_id: 'redeem-1',
      windows_reset: 1,
    }
    post.mockResolvedValue({ data: response })

    const result = await consumeCodexInviteReset(42, ' credit-1 ')

    expect(post).toHaveBeenCalledWith('/admin/providers/42/codex/invite-reset/consume', {
      credit_id: 'credit-1',
    })
    expect(result).toEqual(response)
  })

  /**
   * A Codex reset call omits a selected credit identifier. The outgoing payload
   * is empty, allowing the upstream selection path while retaining the returned
   * reset result.
   */
  it('Codex 重置没有明细选择时省略 credit_id', async () => {
    const response = {
      code: 'reset',
      redeem_request_id: 'redeem-2',
      windows_reset: 1,
    }
    post.mockResolvedValue({ data: response })

    const result = await consumeCodexInviteReset(42)

    expect(post).toHaveBeenCalledWith('/admin/providers/42/codex/invite-reset/consume', {})
    expect(result).toEqual(response)
  })

  /**
   * Single and batch upstream usage queries are exercised together. Their
   * dedicated POST routes, provider-ID body and sixty-five-second request
   * budgets match the expected requests.
   */
  it('使用固定的 API Key 上游用量查询端点和批量请求体', async () => {
    const single = { provider_id: 42, adapter: 'sub2api', observed_at: '2026-08-20T00:00:00Z' }
    const batch = { usage: { '42': single }, errors: {} }
    post.mockResolvedValueOnce({ data: single }).mockResolvedValueOnce({ data: batch })

    await expect(queryUpstreamUsage(42)).resolves.toEqual(single)
    expect(post).toHaveBeenNthCalledWith(1, '/admin/providers/42/upstream-usage/query', undefined, {
      timeout: 65_000,
    })
    await expect(queryBatchUpstreamUsage([42, 43])).resolves.toEqual(batch)
    expect(post).toHaveBeenNthCalledWith(
      2,
      '/admin/providers/upstream-usage/query/batch',
      {
        provider_ids: [42, 43],
      },
      {
        timeout: 65_000,
      },
    )
  })
})
