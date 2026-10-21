import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * post provides the controlled request fixtures used by auth-captcha-oauth-start
 * tests to inspect their observable results.
 */
const post = vi.hoisted(() => vi.fn())

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import {
  buildOAuthLoginStartURL,
  exchangeGoogleOneTap,
  startOAuthLogin,
  type OAuthLoginStart,
} from '@/api/auth'

/**
 * auth-captcha-oauth-start.spec.ts checks captcha-protected OAuth start and
 * Google One Tap exchange requests. Cases inspect proof placement, query
 * encoding and credential context, including a nested return URL that must stay
 * one redirect parameter.
 */

describe('OAuth captcha start API', () => {
  beforeEach(() => {
    post.mockReset()
  })

  /**
   * An OAuth start request combines Tencent captcha proof with redirect and
   * affiliate parameters. The POST keeps proof in the body and OAuth parameters
   * in the query.
   */
  it('posts Tencent captcha proof and preserves OAuth query parameters', async () => {
    const request: OAuthLoginStart = {
      provider: 'github',
      params: { redirect: '/dashboard', aff_code: 'AFF123' },
    }
    const proof = {
      tencent_captcha_ticket: 'ticket',
      tencent_captcha_randstr: '@rand',
    }
    post.mockResolvedValue({ data: { authorize_url: 'https://github.com/login/oauth/authorize' } })

    await expect(startOAuthLogin(request, proof)).resolves.toEqual({
      authorize_url: 'https://github.com/login/oauth/authorize',
    })
    expect(post).toHaveBeenCalledWith('/auth/oauth/github/start', proof, {
      params: request.params,
    })
  })

  /**
   * A WeChat OAuth start URL includes a return path with its own query. URL
   * encoding retains that nested path as one redirect parameter in the generated
   * GET URL.
   */
  it('builds the legacy GET start URL when Tencent captcha is disabled', () => {
    expect(
      buildOAuthLoginStartURL({
        provider: 'wechat',
        params: { mode: 'open', redirect: '/billing?plan=pro' },
      }),
    ).toBe('/api/v1/auth/oauth/wechat/start?mode=open&redirect=%2Fbilling%3Fplan%3Dpro')
  })

  /**
   * A Google One Tap credential includes redirect, affiliate and promotion data.
   * The dedicated exchange endpoint receives every supplied field and returns
   * the registration-required response.
   */
  it('exchanges a Google One Tap credential through the dedicated endpoint', async () => {
    const response = {
      status: 'registration_required',
      redirect: '/dashboard',
    }
    post.mockResolvedValue({ data: response })

    await expect(
      exchangeGoogleOneTap({
        credential: 'google-id-token',
        redirect: '/dashboard',
        aff_code: 'AFF123',
        promo_code: 'PROMO123',
      }),
    ).resolves.toEqual(response)
    expect(post).toHaveBeenCalledWith('/auth/oauth/google/one-tap', {
      credential: 'google-id-token',
      redirect: '/dashboard',
      aff_code: 'AFF123',
      promo_code: 'PROMO123',
    })
  })
})
