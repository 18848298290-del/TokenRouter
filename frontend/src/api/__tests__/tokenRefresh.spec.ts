import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'

/**
 * tokenRefresh.spec.ts checks browser token rotation under concurrent calls,
 * peer publication and session changes. Mocked HTTP responses, Web Locks and
 * timers reproduce delayed winners, rejection reconciliation, account
 * replacement and logout before a response can publish credentials.
 */

vi.mock('axios', () => ({
  default: {
    post: vi.fn(),
  },
}))

/**
 * mockedPost provides the controlled request fixtures used by tokenRefresh tests
 * to inspect their observable results.
 */
const mockedPost = vi.mocked(axios.post)

/**
 * seedSession installs an expiring user session for refresh tests. Nonempty
 * credential or expiry overrides replace fixture values; omitted or empty
 * strings select the defaults. The serialized user identity remains fixed.
 */
function seedSession(overrides: Partial<Record<string, string>> = {}): void {
  localStorage.setItem('auth_token', overrides.auth_token || 'old-access')
  localStorage.setItem('refresh_token', overrides.refresh_token || 'old-refresh')
  localStorage.setItem('token_expires_at', overrides.token_expires_at || String(Date.now() - 1))
  localStorage.setItem('auth_user', JSON.stringify({ id: 7, email: 'admin@example.com' }))
}

/**
 * refreshedResponse builds a successful refresh response with deterministic
 * replacement credentials for concurrency tests.
 */
function refreshedResponse() {
  return {
    data: {
      code: 0,
      message: 'ok',
      data: {
        access_token: 'new-access',
        refresh_token: 'new-refresh',
        expires_in: 3600,
        token_type: 'Bearer',
      },
    },
  }
}

describe('refreshAuthTokens', () => {
  beforeEach(() => {
    localStorage.clear()
    mockedPost.mockReset()
    vi.resetModules()
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: undefined,
    })
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  /**
   * Two calls begin while one refresh request is unresolved. Both resolve from
   * that single HTTP request and observe its replacement credentials in browser
   * storage.
   */
  it('同一文档内的并发调用共享一次刷新请求', async () => {
    seedSession()
    /**
     * Hold the response until both callers have started so the second call must
     * reuse an unresolved request before either receives replacement tokens.
     */
    let resolveRequest!: (value: ReturnType<typeof refreshedResponse>) => void
    mockedPost.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveRequest = resolve
        }),
    )
    const { refreshAuthTokens } = await import('@/api/tokenRefresh')

    const first = refreshAuthTokens({ failedAccessToken: 'old-access' })
    const second = refreshAuthTokens({ failedAccessToken: 'old-access' })

    expect(mockedPost).toHaveBeenCalledTimes(1)
    expect(mockedPost).toHaveBeenCalledWith(
      '/api/v1/auth/refresh',
      { refresh_token: 'old-refresh' },
      expect.objectContaining({ timeout: 30_000 }),
    )
    resolveRequest(refreshedResponse())

    await expect(first).resolves.toMatchObject({ access_token: 'new-access' })
    await expect(second).resolves.toMatchObject({ refresh_token: 'new-refresh' })
    expect(localStorage.getItem('refresh_token')).toBe('new-refresh')
  })

  /**
   * A peer publishes replacement credentials before the Web Lock callback runs.
   * The waiting caller adopts those tokens and completes without issuing another
   * refresh request.
   */
  it('取得 Web Lock 后采用其他标签页已轮换的 token', async () => {
    seedSession()
    /**
     * Publish peer credentials before invoking the lock callback to reproduce
     * another tab completing rotation while this caller waits for lock access.
     */
    const request = vi.fn(async (_name: string, callback: () => Promise<unknown>) => {
      localStorage.setItem('auth_token', 'peer-access')
      localStorage.setItem('token_expires_at', String(Date.now() + 3600_000))
      localStorage.setItem('refresh_token', 'peer-refresh')
      return callback()
    })
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: { request },
    })
    const { refreshAuthTokens } = await import('@/api/tokenRefresh')

    const result = await refreshAuthTokens({ failedAccessToken: 'old-access' })

    expect(request).toHaveBeenCalledTimes(1)
    expect(request).toHaveBeenCalledWith('tokenrouter-auth-token-refresh', expect.any(Function))
    expect(mockedPost).not.toHaveBeenCalled()
    expect(result).toMatchObject({
      access_token: 'peer-access',
      refresh_token: 'peer-refresh',
    })
  })

  /**
   * A refresh request fails just before a peer publishes a replacement pair.
   * Polling accepts the shortly delayed credentials and returns the peer's
   * access and refresh tokens.
   */
  it('请求失败后采用其他标签页紧接着发布的轮换 token', async () => {
    seedSession()
    mockedPost.mockRejectedValueOnce(new Error('refresh token already used'))
    const { refreshAuthTokens } = await import('@/api/tokenRefresh')

    /**
     * Delay peer publication beyond the immediate rejection so recovery must
     * poll storage after the failed request to discover the replacement pair.
     */
    window.setTimeout(() => {
      localStorage.setItem('auth_token', 'peer-access')
      localStorage.setItem('token_expires_at', String(Date.now() + 3600_000))
      localStorage.setItem('refresh_token', 'peer-refresh')
    }, 10)

    await expect(refreshAuthTokens({ failedAccessToken: 'old-access' })).resolves.toMatchObject({
      access_token: 'peer-access',
      refresh_token: 'peer-refresh',
    })
  })

  /**
   * An unchanged session has a remaining lifetime close to a timer threshold.
   * The coordinator still performs a refresh, protecting against treating expiry
   * timing as proof of peer publication.
   */
  it('does not mistake boundary timer jitter for a completed peer refresh', async () => {
    vi.useFakeTimers()
    seedSession({ token_expires_at: String(Date.now() + 120_001) })
    mockedPost.mockResolvedValueOnce(refreshedResponse())
    const request = vi.fn(async (_name: string, callback: () => Promise<unknown>) => callback())
    Object.defineProperty(navigator, 'locks', {
      configurable: true,
      value: { request },
    })
    const { refreshAuthTokens } = await import('@/api/tokenRefresh')

    await expect(refreshAuthTokens()).resolves.toMatchObject({ access_token: 'new-access' })

    expect(request).toHaveBeenCalledTimes(1)
    expect(mockedPost).toHaveBeenCalledTimes(1)
  })

  /**
   * Two isolated module instances race without Web Locks and one receives 401.
   * The losing attempt waits for the slower winner and adopts its published
   * replacement pair.
   */
  it('无 Web Lock 时为较慢的竞争胜方保留发布窗口', async () => {
    vi.useFakeTimers()
    seedSession()
    /**
     * Hold the winner's response while separate module instances start their
     * own attempts, so the loser reconciles a rejection independently of the
     * winner's pending promise.
     */
    let resolveWinningRequest!: (value: ReturnType<typeof refreshedResponse>) => void
    mockedPost.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveWinningRequest = resolve
        }),
    )
    const firstTab = await import('@/api/tokenRefresh')
    vi.resetModules()
    const secondTab = await import('@/api/tokenRefresh')

    const winner = firstTab.refreshAuthTokens({ failedAccessToken: 'old-access' })
    mockedPost.mockRejectedValueOnce({ response: { status: 401 } })
    const loser = secondTab.refreshAuthTokens({ failedAccessToken: 'old-access' })

    /**
     * Publish the slow winner after 1500 ms, then advance to 1600 ms so the
     * loser's polling timers can observe the published credentials and settle.
     */
    window.setTimeout(() => resolveWinningRequest(refreshedResponse()), 1_500)
    await vi.advanceTimersByTimeAsync(1_600)

    await expect(winner).resolves.toMatchObject({ access_token: 'new-access' })
    await expect(loser).resolves.toMatchObject({ refresh_token: 'new-refresh' })
    expect(mockedPost).toHaveBeenCalledTimes(2)
    expect(localStorage.getItem('refresh_token')).toBe('new-refresh')
  })

  /**
   * A peer publishes credentials belonging to another user during
   * reconciliation. The original failure is rethrown, protecting identity checks
   * from accepting the unrelated session.
   */
  it('不会采用另一个登录用户的 token', async () => {
    vi.useFakeTimers()
    seedSession()
    mockedPost.mockRejectedValueOnce(new Error('refresh token already used'))
    const { refreshAuthTokens } = await import('@/api/tokenRefresh')

    /**
     * Publish the other identity after rejection starts reconciliation so the
     * waiting caller encounters credentials that cannot belong to its captured
     * session.
     */
    window.setTimeout(() => {
      localStorage.setItem('auth_user', JSON.stringify({ id: 8, email: 'other@example.com' }))
      localStorage.setItem('auth_token', 'other-access')
      localStorage.setItem('token_expires_at', String(Date.now() + 3600_000))
      localStorage.setItem('refresh_token', 'other-refresh')
    }, 10)

    const rejection = expect(
      refreshAuthTokens({ failedAccessToken: 'old-access' }),
    ).rejects.toThrow('refresh token already used')
    await vi.advanceTimersByTimeAsync(1_100)
    await rejection
  })

  /**
   * Local storage is cleared while a successful refresh response is pending. The
   * response rejects as a session change and cannot repopulate the cleared
   * access or refresh token.
   */
  it('刷新进行中登出时不会恢复原会话', async () => {
    vi.useFakeTimers()
    seedSession()
    /**
     * Keep the response pending until logout clears storage so completion must
     * compare the captured session with the cleared state before publishing
     * credentials.
     */
    let resolveRequest!: (value: ReturnType<typeof refreshedResponse>) => void
    mockedPost.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveRequest = resolve
        }),
    )
    const { refreshAuthTokens } = await import('@/api/tokenRefresh')

    const pending = refreshAuthTokens({ failedAccessToken: 'old-access' })
    localStorage.clear()
    resolveRequest(refreshedResponse())

    const rejection = expect(pending).rejects.toThrow('Session changed during token refresh')
    await vi.advanceTimersByTimeAsync(1_100)
    await rejection
    expect(localStorage.getItem('auth_token')).toBeNull()
    expect(localStorage.getItem('refresh_token')).toBeNull()
  })
})
