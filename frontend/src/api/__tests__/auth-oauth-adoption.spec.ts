import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * auth-oauth-adoption.spec.ts checks profile-adoption decisions across pending
 * OAuth completion paths. Explicit false values remain visible in submitted
 * payloads, while classification helpers distinguish login, binding, invitation
 * requirements and suggested profile data before cookie preparation.
 */

/**
 * post provides the controlled request fixtures used by auth-oauth-adoption
 * tests to inspect their observable results.
 */
const post = vi.fn()

vi.mock('@/api/client', () => ({
  apiClient: {
    post,
  },
}))

describe('oauth adoption auth api', () => {
  beforeEach(() => {
    post.mockReset()
    post.mockResolvedValue({ data: {} })
    localStorage.clear()
    document.cookie = 'oauth_bind_access_token=; Max-Age=0; path=/'
  })

  /**
   * Pending OAuth completion receives distinct display-name and avatar choices.
   * Serialization retains explicit false alongside true under the server's
   * adoption field names.
   */
  it('posts adoption decisions when exchanging pending oauth completion', async () => {
    const { exchangePendingOAuthCompletion } = await import('@/api/auth')

    await exchangePendingOAuthCompletion({
      adoptDisplayName: false,
      adoptAvatar: true,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/pending/exchange', {
      adopt_display_name: false,
      adopt_avatar: true,
    })
  })

  /**
   * Pending bind-login completion receives explicit choices for both profile
   * fields. The exchange endpoint receives those booleans independently,
   * protecting a rejected avatar choice from being omitted.
   */
  it('posts bind-login decisions when finalizing pending oauth bind flow', async () => {
    const { completePendingOAuthBindLogin } = await import('@/api/auth')

    await completePendingOAuthBindLogin({
      adoptDisplayName: true,
      adoptAvatar: false,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/pending/exchange', {
      adopt_display_name: true,
      adopt_avatar: false,
    })
  })

  /**
   * LinuxDo invitation completion includes a name decision and rejected avatar
   * decision. The provider-specific registration endpoint receives the
   * invitation code and both explicit adoption values.
   */
  it('posts linuxdo invitation completion with adoption decisions', async () => {
    const { completeLinuxDoOAuthRegistration } = await import('@/api/auth')

    await completeLinuxDoOAuthRegistration('invite-code', {
      adoptDisplayName: true,
      adoptAvatar: false,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/linuxdo/complete-registration', {
      invitation_code: 'invite-code',
      adopt_display_name: true,
      adopt_avatar: false,
    })
  })

  /**
   * LinuxDo account creation receives a rejected name decision and accepted
   * avatar decision. The request retains their independent boolean values
   * alongside the invitation code.
   */
  it('posts linuxdo create-account completion with adoption decisions', async () => {
    const { createPendingLinuxDoOAuthAccount } = await import('@/api/auth')

    await createPendingLinuxDoOAuthAccount('invite-code', {
      adoptDisplayName: false,
      adoptAvatar: true,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/linuxdo/complete-registration', {
      invitation_code: 'invite-code',
      adopt_display_name: false,
      adopt_avatar: true,
    })
  })

  /**
   * OIDC invitation completion includes two different profile-adoption
   * decisions. The provider-specific registration endpoint receives the
   * invitation code and preserves both boolean values in its payload.
   */
  it('posts oidc invitation completion with adoption decisions', async () => {
    const { completeOIDCOAuthRegistration } = await import('@/api/auth')

    await completeOIDCOAuthRegistration('invite-code', {
      adoptDisplayName: false,
      adoptAvatar: true,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/oidc/complete-registration', {
      invitation_code: 'invite-code',
      adopt_display_name: false,
      adopt_avatar: true,
    })
  })

  /**
   * OIDC account creation receives explicit name and avatar choices. Its request
   * keeps each choice separate and includes the invitation code at the OIDC
   * completion endpoint.
   */
  it('posts oidc create-account completion with adoption decisions', async () => {
    const { createPendingOIDCOAuthAccount } = await import('@/api/auth')

    await createPendingOIDCOAuthAccount('invite-code', {
      adoptDisplayName: true,
      adoptAvatar: false,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/oidc/complete-registration', {
      invitation_code: 'invite-code',
      adopt_display_name: true,
      adopt_avatar: false,
    })
  })

  /**
   * WeChat invitation completion accepts both suggested profile fields. The
   * provider-specific completion request includes the invitation code together
   * with both true adoption flags.
   */
  it('posts wechat invitation completion with adoption decisions', async () => {
    const { completeWeChatOAuthRegistration } = await import('@/api/auth')

    await completeWeChatOAuthRegistration('invite-code', {
      adoptDisplayName: true,
      adoptAvatar: true,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/wechat/complete-registration', {
      invitation_code: 'invite-code',
      adopt_display_name: true,
      adopt_avatar: true,
    })
  })

  /**
   * WeChat account creation rejects both suggested profile fields. The request
   * includes both false adoption flags so the server can distinguish rejection
   * from an omitted decision.
   */
  it('posts wechat create-account completion with adoption decisions', async () => {
    const { createPendingWeChatOAuthAccount } = await import('@/api/auth')

    await createPendingWeChatOAuthAccount('invite-code', {
      adoptDisplayName: false,
      adoptAvatar: false,
    })

    expect(post).toHaveBeenCalledWith('/auth/oauth/wechat/complete-registration', {
      invitation_code: 'invite-code',
      adopt_display_name: false,
      adopt_avatar: false,
    })
  })

  /**
   * OAuth completion is classified with and without an access token. A token
   * identifies login completion, while a redirect-only result identifies binding
   * completion for callers.
   */
  it('classifies oauth completion results as login or bind', async () => {
    const { getOAuthCompletionKind } = await import('@/api/auth')

    expect(getOAuthCompletionKind({ access_token: 'access-token' })).toBe('login')
    expect(getOAuthCompletionKind({ redirect: '/profile' })).toBe('bind')
  })

  /**
   * Pending binding responses contain invitation errors and optional suggested
   * profile fields. The predicates distinguish invitation-required state and
   * detect either profile suggestion while rejecting empty suggestion data.
   */
  it('provides bind-login utility helpers for invitation and suggested profile states', async () => {
    const {
      getPendingOAuthBindLoginKind,
      hasPendingOAuthSuggestedProfile,
      isPendingOAuthCreateAccountRequired,
    } = await import('@/api/auth')

    expect(getPendingOAuthBindLoginKind({ access_token: 'access-token' })).toBe('login')
    expect(getPendingOAuthBindLoginKind({ redirect: '/profile' })).toBe('bind')
    expect(
      isPendingOAuthCreateAccountRequired({
        error: 'invitation_required',
      }),
    ).toBe(true)
    expect(
      isPendingOAuthCreateAccountRequired({
        error: 'other',
      }),
    ).toBe(false)
    expect(
      hasPendingOAuthSuggestedProfile({
        suggested_display_name: 'OAuth Nick',
      }),
    ).toBe(true)
    expect(
      hasPendingOAuthSuggestedProfile({
        suggested_avatar_url: 'https://cdn.example/avatar.png',
      }),
    ).toBe(true)
    expect(hasPendingOAuthSuggestedProfile({})).toBe(false)
  })

  /**
   * A stored access token is available before OAuth binding starts. Cookie
   * preparation calls the binding-token endpoint so the later redirect can use
   * server-established browser authentication.
   */
  it('requests an HttpOnly oauth bind cookie before redirect binding', async () => {
    localStorage.setItem('auth_token', 'access-token-value')
    const { prepareOAuthBindAccessTokenCookie } = await import('@/api/auth')

    await prepareOAuthBindAccessTokenCookie()

    expect(post).toHaveBeenCalledWith('/auth/oauth/bind-token')
  })
})
