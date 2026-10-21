import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * user.spec.ts checks OAuth binding-start URLs against an absolute API base.
 * LinuxDo and capability-approved WeChat cases protect the encoded return path,
 * binding intent and selected browser mode included before navigation begins.
 */

describe('user api oauth binding urls', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.stubEnv('VITE_API_BASE_URL', 'https://api.example.com/api/v1')
  })

  afterEach(() => {
    vi.unstubAllEnvs()
  })

  /**
   * Binding URLs use an absolute API base, encoded return path and explicit
   * binding intent. LinuxDo and capability-approved WeChat requests select their
   * bind-start endpoints with the expected parameters.
   */
  it('builds third-party bind urls against the bind start endpoint', async () => {
    const { buildOAuthBindingStartURL } = await import('@/api/user')

    expect(buildOAuthBindingStartURL('linuxdo', { redirectTo: '/settings/profile' })).toBe(
      'https://api.example.com/api/v1/auth/oauth/linuxdo/bind/start?redirect=%2Fsettings%2Fprofile&intent=bind_current_user',
    )
    expect(
      buildOAuthBindingStartURL('wechat', {
        redirectTo: '/settings/profile',
        wechatOAuthSettings: {
          wechat_oauth_open_enabled: true,
          wechat_oauth_mp_enabled: false,
          wechat_oauth_mobile_enabled: false,
        },
      }),
    ).toBe(
      'https://api.example.com/api/v1/auth/oauth/wechat/bind/start?redirect=%2Fsettings%2Fprofile&intent=bind_current_user&mode=open',
    )
  })
})
