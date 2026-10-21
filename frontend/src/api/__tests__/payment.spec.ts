import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get, post } provide the controlled request fixtures used by payment tests to
 * inspect their observable results.
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

import { paymentAPI } from '@/api/payment'

/**
 * payment.spec.ts checks public payment lookup payloads used by return flows.
 * Separate cases retain legacy trade-number verification and signed resume-token
 * resolution, with mocked POST calls inspecting the endpoint and field used for
 * each lookup.
 */

describe('payment api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    get.mockResolvedValue({ data: {} })
    post.mockResolvedValue({ data: {} })
  })

  /**
   * Public verification is requested with a legacy trade number. The wrapper
   * sends out_trade_no to the public verification endpoint used by compatibility
   * payment return flows.
   */
  it('keeps legacy public out_trade_no verification for upgrade compatibility', async () => {
    await paymentAPI.verifyOrderPublic('legacy-order-no')

    expect(post).toHaveBeenCalledWith('/payment/public/orders/verify', {
      out_trade_no: 'legacy-order-no',
    })
  })

  /**
   * Public order recovery is requested with a signed resume token. The wrapper
   * sends resume_token to the public resolve endpoint used to retrieve the
   * payment result.
   */
  it('keeps signed public resume-token resolve endpoint', async () => {
    await paymentAPI.resolveOrderPublicByResumeToken('resume-token-123')

    expect(post).toHaveBeenCalledWith('/payment/public/orders/resolve', {
      resume_token: 'resume-token-123',
    })
  })
})
