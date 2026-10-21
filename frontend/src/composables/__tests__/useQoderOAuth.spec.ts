import { beforeEach, describe, expect, it, vi } from 'vitest'

// showError records global notifications so each case can reset feedback
// emitted by authorization failures.
const showError = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    qoder: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      poll: vi.fn(),
    },
  },
}))

import { adminAPI } from '@/api/admin'
import { useQoderOAuth } from '@/composables/useQoderOAuth'

// useQoderOAuth.spec.ts checks Qoder authorization state and credential
// construction. Mock API responses exercise session submission, polling
// completion, and supported account fields, while a deferred URL request
// verifies that resetting the form rejects its late result.

// createDeferred returns a promise and its externally controlled resolver for
// delayed API responses. Tests resolve it after resetting the form to
// reproduce a response arriving from an obsolete request.
function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

beforeEach(() => {
  showError.mockReset()
  vi.mocked(adminAPI.qoder.generateAuthUrl).mockReset()
  vi.mocked(adminAPI.qoder.exchangeCode).mockReset()
  vi.mocked(adminAPI.qoder.poll).mockReset()
})

describe('useQoderOAuth', () => {
  // Authorization starts with a Chinese site selection and proxy. The
  // generated URL, session, state, and polling interval become available
  // after the API response succeeds.
  it('generates an authorization URL and stores session state', async () => {
    vi.mocked(adminAPI.qoder.generateAuthUrl).mockResolvedValueOnce({
      auth_url: 'https://qoder.com/device/selectAccounts?nonce=n',
      session_id: 'session-id',
      state: 'state-value',
      expires_in: 600,
      interval: 2,
    })

    const oauth = useQoderOAuth()
    const ok = await oauth.generateAuthUrl(7, 'cn')

    expect(ok).toBe(true)
    expect(adminAPI.qoder.generateAuthUrl).toHaveBeenCalledWith({ proxy_id: 7, site: 'cn' })
    expect(oauth.authUrl.value).toBe('https://qoder.com/device/selectAccounts?nonce=n')
    expect(oauth.sessionId.value).toBe('session-id')
    expect(oauth.state.value).toBe('state-value')
    expect(oauth.pollInterval.value).toBe(2)
  })

  // A URL request finishes after the authorization form is reset. The stale
  // response returns false and leaves the cleared session and activity
  // fields intact.
  it('does not restore a generated session after the flow is reset', async () => {
    const deferred = createDeferred<{
      auth_url: string // Browser authorization URL.
      session_id: string // Backend session identifier.
      state: string // Callback correlation state.
      expires_in: number // Lifetime in seconds.
      interval: number // Polling interval seconds.
    }>()
    vi.mocked(adminAPI.qoder.generateAuthUrl).mockReturnValueOnce(deferred.promise)

    const oauth = useQoderOAuth()
    const pending = oauth.generateAuthUrl(7, 'global')
    oauth.resetState()
    deferred.resolve({
      auth_url: 'https://qoder.com/old-session',
      session_id: 'old-session',
      state: 'old-state',
      expires_in: 600,
      interval: 2,
    })

    expect(await pending).toBe(false)
    expect(oauth.authUrl.value).toBe('')
    expect(oauth.sessionId.value).toBe('')
    expect(oauth.state.value).toBe('')
    expect(oauth.loading.value).toBe(false)
  })

  // The administrator supplies a padded code and callback URL for an
  // existing session. Exchange sends trimmed callback inputs beside the
  // matching session and state.
  it('exchanges a pasted callback URL with session and state', async () => {
    vi.mocked(adminAPI.qoder.exchangeCode).mockResolvedValueOnce({
      security_oauth_token: 'access-token',
      machine_id: 'machine-id',
    })

    const oauth = useQoderOAuth()
    const tokenInfo = await oauth.exchangeAuthCode({
      code: ' code ',
      callbackUrl: ' http://localhost:1455/callback?code=code&state=state-value ',
      sessionId: 'session-id',
      state: 'state-value',
    })

    expect(tokenInfo?.security_oauth_token).toBe('access-token')
    expect(adminAPI.qoder.exchangeCode).toHaveBeenCalledWith({
      session_id: 'session-id',
      state: 'state-value',
      code: 'code',
      callback_url: 'http://localhost:1455/callback?code=code&state=state-value',
    })
  })

  // Device authorization completes using an existing session without a
  // pasted callback. Exchange submits the session and state and returns the
  // machine credentials from the API.
  it('allows completing authorization without a pasted code for device polling', async () => {
    vi.mocked(adminAPI.qoder.exchangeCode).mockResolvedValueOnce({
      security_oauth_token: 'access-token',
      machine_id: 'machine-id',
    })

    const oauth = useQoderOAuth()
    const tokenInfo = await oauth.exchangeAuthCode({
      sessionId: 'session-id',
      state: 'state-value',
    })

    expect(tokenInfo?.machine_id).toBe('machine-id')
    expect(adminAPI.qoder.exchangeCode).toHaveBeenCalledWith({
      session_id: 'session-id',
      state: 'state-value',
    })
  })

  // A device session reports completed authorization and machine
  // credentials. A single poll returns that result and clears both activity
  // flags after the request settles.
  it('polls the device authorization session', async () => {
    vi.mocked(adminAPI.qoder.poll).mockResolvedValueOnce({
      status: 'completed',
      token_info: {
        security_oauth_token: 'access-token',
        machine_id: 'machine-id',
      },
    })

    const oauth = useQoderOAuth()
    const result = await oauth.pollAuthorization({
      sessionId: 'session-id',
      state: 'state-value',
    })

    expect(result?.status).toBe('completed')
    expect(result?.token_info?.machine_id).toBe('machine-id')
    expect(adminAPI.qoder.poll).toHaveBeenCalledWith({
      session_id: 'session-id',
      state: 'state-value',
    })
    expect(oauth.loading.value).toBe(false)
    expect(oauth.polling.value).toBe(false)
  })

  // A Qoder token response contains machine, account, site, and refresh
  // metadata. Credential construction retains each supported field in the
  // record sent for provider storage.
  it('builds credentials compatible with Qoder token provider', () => {
    const oauth = useQoderOAuth()
    const credentials = oauth.buildCredentials({
      security_oauth_token: 'access-token',
      refresh_token: 'refresh-token',
      machine_id: 'machine-id',
      machine_token: 'machine-token',
      machine_type: 'desktop',
      uid: 'uid',
      aid: 'aid',
      organization_id: 'org-1',
      organization_name: 'Qoder Org',
      name: 'Qoder User',
      user_type: 'personal_standard',
      site: 'cn',
      refresh_mode: 'qodercn20',
      expires_at: '2026-07-20T12:00:00Z',
      extra: { email: 'user@example.com' },
    })

    expect(credentials).toEqual({
      security_oauth_token: 'access-token',
      refresh_token: 'refresh-token',
      machine_id: 'machine-id',
      machine_token: 'machine-token',
      machine_type: 'desktop',
      uid: 'uid',
      aid: 'aid',
      organization_id: 'org-1',
      organization_name: 'Qoder Org',
      name: 'Qoder User',
      user_type: 'personal_standard',
      site: 'cn',
      refresh_mode: 'qodercn20',
      expires_at: '2026-07-20T12:00:00Z',
      extra: { email: 'user@example.com' },
    })
  })
})
