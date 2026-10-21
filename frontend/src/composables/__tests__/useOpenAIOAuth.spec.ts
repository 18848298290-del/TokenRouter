import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => {
      const messages: Record<string, string> = {
        'admin.providers.oauth.openai.failedToExchangeCode': 'OpenAI 授权码兑换失败',
        'admin.providers.oauth.openai.errors.OPENAI_OAUTH_PROXY_REQUIRED':
          '未设置代理，当前服务器无法直连 OpenAI，导致 OpenAI OAuth 请求失败。请先选择可访问 OpenAI 的代理后重试；如果授权码已失效，请重新生成授权链接。',
      }
      return messages[key] ?? key
    },
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshOpenAIToken: vi.fn(),
    },
  },
}))

import { useOpenAIOAuth } from '@/composables/useOpenAIOAuth'
import { adminAPI } from '@/api/admin'

// useOpenAIOAuth.spec.ts checks OpenAI credential metadata and authorization
// request parameters. Mock API calls expose proxy and TLS router forwarding,
// while structured errors verify the recovery text returned to the
// authorization form.

beforeEach(() => {
  vi.mocked(adminAPI.providers.generateAuthUrl).mockReset()
  vi.mocked(adminAPI.providers.exchangeCode).mockReset()
  vi.mocked(adminAPI.providers.refreshOpenAIToken).mockReset()
})

describe('useOpenAIOAuth.buildCredentials', () => {
  // The token response identifies the OAuth client that issued its
  // credentials. Credential construction carries that client identifier
  // beside the access and refresh tokens.
  it('should keep client_id when token response contains it', () => {
    const oauth = useOpenAIOAuth()
    const creds = oauth.buildCredentials({
      access_token: 'at',
      refresh_token: 'rt',
      client_id: 'app_test_client',
      expires_at: 1700000000,
    })

    expect(creds.client_id).toBe('app_test_client')
    expect(creds.access_token).toBe('at')
    expect(creds.refresh_token).toBe('rt')
  })

  // A token response lacks an OAuth client identifier. Credential
  // construction omits that property while retaining both tokens, allowing
  // responses with fewer metadata fields.
  it('should keep legacy behavior when client_id is missing', () => {
    const oauth = useOpenAIOAuth()
    const creds = oauth.buildCredentials({
      access_token: 'at',
      refresh_token: 'rt',
      expires_at: 1700000000,
    })

    expect(Object.prototype.hasOwnProperty.call(creds, 'client_id')).toBe(false)
    expect(creds.access_token).toBe('at')
    expect(creds.refresh_token).toBe('rt')
  })

  // A ChatGPT team token response contains subscription expiration metadata.
  // Credential construction keeps the plan and expiration text available for
  // the provider persistence request.
  it('should keep ChatGPT subscription expiration from token response', () => {
    const oauth = useOpenAIOAuth()
    const creds = oauth.buildCredentials({
      access_token: 'at',
      refresh_token: 'rt',
      expires_at: 1700000000,
      plan_type: 'team',
      subscription_expires_at: '2026-07-20T19:22:48+00:00',
    })

    expect(creds.plan_type).toBe('team')
    expect(creds.subscription_expires_at).toBe('2026-07-20T19:22:48+00:00')
  })
})

describe('useOpenAIOAuth.exchangeAuthCode', () => {
  // Code exchange receives padded code and state plus proxy and TLS router
  // selections. The API receives trimmed callback fields and both selected
  // identifiers.
  it('passes selected TLS router id to token exchange', async () => {
    vi.mocked(adminAPI.providers.exchangeCode).mockResolvedValueOnce({
      access_token: 'at',
      refresh_token: 'rt',
    })
    const oauth = useOpenAIOAuth()

    const tokenInfo = await oauth.exchangeAuthCode(' code ', 'session-id', ' state ', 3, 9)

    expect(tokenInfo?.access_token).toBe('at')
    expect(adminAPI.providers.exchangeCode).toHaveBeenCalledWith('/admin/openai/exchange-code', {
      session_id: 'session-id',
      code: 'code',
      state: 'state',
      proxy_id: 3,
      tls_fingerprint_router_id: 9,
    })
  })

  // Code exchange rejects with the structured proxy-required reason. The
  // composable returns null and exposes a translated recovery message that
  // asks the administrator to select a proxy.
  it('shows a clear proxy hint when code exchange fails without a proxy', async () => {
    vi.mocked(adminAPI.providers.exchangeCode).mockRejectedValueOnce({
      status: 502,
      reason: 'OPENAI_OAUTH_PROXY_REQUIRED',
      message: 'OpenAI OAuth token exchange failed: no proxy is configured.',
    })
    const oauth = useOpenAIOAuth()

    const tokenInfo = await oauth.exchangeAuthCode('code', 'session-id', 'state')

    expect(tokenInfo).toBeNull()
    expect(oauth.error.value).toBe(
      '未设置代理，当前服务器无法直连 OpenAI，导致 OpenAI OAuth 请求失败。请先选择可访问 OpenAI 的代理后重试；如果授权码已失效，请重新生成授权链接。',
    )
  })
})

describe('useOpenAIOAuth.validateRefreshToken', () => {
  // Refresh-token validation receives padded token text and selected client,
  // proxy, and TLS router identifiers. The dedicated refresh endpoint
  // receives those values with the token trimmed.
  it('passes selected TLS router id to refresh-token validation', async () => {
    vi.mocked(adminAPI.providers.refreshOpenAIToken).mockResolvedValueOnce({
      access_token: 'at',
      refresh_token: 'rt',
    })
    const oauth = useOpenAIOAuth()

    const tokenInfo = await oauth.validateRefreshToken(' rt ', 3, 'client-id', 9)

    expect(tokenInfo?.access_token).toBe('at')
    expect(adminAPI.providers.refreshOpenAIToken).toHaveBeenCalledWith(
      'rt',
      3,
      '/admin/openai/refresh-token',
      'client-id',
      9,
    )
  })
})
