import { describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const messages: Record<string, string> = {
        'admin.providers.oauth.grok.failedToExchangeCode': 'Grok 授权码兑换失败',
        'admin.providers.oauth.grok.errors.GROK_OAUTH_INVALID_STATE':
          'Grok OAuth state 与当前会话不匹配。请粘贴同一次生成的授权链接返回的回调 URL。',
      }
      return messages[key] ?? key
    },
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    grok: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshGrokToken: vi.fn(),
    },
  },
}))

import { useGrokOAuth } from '@/composables/useGrokOAuth'
import { adminAPI } from '@/api/admin'

// useGrokOAuth.spec.ts checks Grok exchange feedback and persisted credential
// fields. Mocked API failures exercise translated recovery text, and
// credential fixtures include one-time authentication inputs to verify the
// record selected for provider storage.

describe('useGrokOAuth.exchangeAuthCode', () => {
  // Code exchange rejects a callback with a structured invalid-state reason.
  // The composable returns null and exposes the translated hint to retry with
  // a matching session.
  it('shows a state mismatch recovery hint from structured backend errors', async () => {
    vi.mocked(adminAPI.grok.exchangeCode).mockRejectedValueOnce({
      status: 400,
      reason: 'GROK_OAUTH_INVALID_STATE',
      message: 'invalid oauth state',
    })
    const oauth = useGrokOAuth()

    const tokenInfo = await oauth.exchangeAuthCode({
      code: 'code',
      sessionId: 'session-id',
      state: 'wrong-state',
    })

    expect(tokenInfo).toBeNull()
    expect(oauth.error.value).toBe(
      'Grok OAuth state 与当前会话不匹配。请粘贴同一次生成的授权链接返回的回调 URL。',
    )
  })
})

describe('useGrokOAuth.buildCredentials', () => {
  // A token response includes account details and one-time password and SSO
  // inputs. Stored credentials retain token metadata and exclude those inputs
  // and an imposed host override.
  it('builds OAuth credentials without forcing base_url or leaking sso/password', () => {
    const oauth = useGrokOAuth()

    const credentials = oauth.buildCredentials({
      access_token: 'access-token',
      token_type: 'Bearer',
      expires_at: 1_900_000_000,
      client_id: 'client-id',
      scope: 'openid grok-cli:access',
      email: 'grok@example.com',
      password: 'super-secret',
      sso_token: 'sso-cookie',
      sso: 'sso-cookie',
      'sso-rw': 'sso-cookie',
    } as any)

    expect(credentials.access_token).toBe('access-token')
    expect(credentials.email).toBe('grok@example.com')
    expect(credentials.base_url).toBeUndefined()
    expect(credentials).not.toHaveProperty('password')
    expect(credentials).not.toHaveProperty('sso_token')
    expect(credentials).not.toHaveProperty('sso')
    expect(credentials).not.toHaveProperty('sso-rw')
  })
})
