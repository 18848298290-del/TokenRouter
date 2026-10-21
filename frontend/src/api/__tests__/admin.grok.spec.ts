import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { post } provide the controlled request fixtures used by admin.grok tests to
 * inspect their observable results.
 */
const { post } = vi.hoisted(() => ({
  post: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: { post },
}))

import { authorizePassword, createFromSSO, getGrokSSOImportTimeout } from '@/api/admin/grok'

/**
 * admin.grok.spec.ts checks xAI SSO timeout sizing and password parsing.
 * Parameterized batches protect the import budget, and the password case
 * verifies trimmed email with password whitespace retained in the submitted
 * authorization payload.
 */

describe('admin Grok SSO import API', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: { created: [], failed: [] } })
  })

  /**
   * SSO imports span different numbers of concurrent batches. Each case protects
   * the calculated timeout and verifies that createFromSSO passes that same
   * budget to the request.
   */
  it.each([
    [1, 180_000],
    [3, 180_000],
    [4, 270_000],
    [7, 360_000],
  ])('uses a timeout sized for %i keys', async (keyCount, expectedTimeout) => {
    expect(getGrokSSOImportTimeout(keyCount)).toBe(expectedTimeout)

    await createFromSSO({
      sso_tokens: Array.from({ length: keyCount }, (_, index) => `sso-${index + 1}`),
    })

    expect(post).toHaveBeenCalledWith(
      '/admin/grok/sso-to-oauth',
      expect.objectContaining({ sso_tokens: expect.any(Array) }),
      { timeout: expectedTimeout },
    )
  })

  /**
   * A password submission contains surrounding email and password whitespace.
   * The email is trimmed while password spaces remain intact, and authorization
   * uses its two-minute request budget.
   */
  it('preserves password whitespace and applies the authorization timeout', async () => {
    post.mockResolvedValueOnce({ data: { access_token: 'access-token' } })

    await authorizePassword(' user@example.com ----  password with spaces  ', 7)

    expect(post).toHaveBeenCalledWith(
      '/admin/grok/oauth/password',
      {
        email: 'user@example.com',
        password: '  password with spaces  ',
        proxy_id: 7,
      },
      { timeout: 120_000 },
    )
  })
})
