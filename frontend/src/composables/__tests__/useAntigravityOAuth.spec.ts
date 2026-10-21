import { describe, expect, it, vi } from 'vitest'

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
  }),
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    antigravity: {
      generateAuthUrl: vi.fn(),
      exchangeCode: vi.fn(),
      refreshAntigravityToken: vi.fn(),
    },
  },
}))

import { useAntigravityOAuth } from '@/composables/useAntigravityOAuth'

// useAntigravityOAuth.spec.ts checks refresh-token selection in Antigravity
// credential records. API, translation, and notification dependencies are
// replaced by fixtures so each case isolates missing, blank, or rotated
// refresh-token values returned to the credential builder.

describe('useAntigravityOAuth.buildCredentials', () => {
  // A refresh response omits a replacement refresh token. Credential
  // construction keeps the submitted token so the provider record still
  // contains a refresh credential.
  it('falls back to the submitted refresh token when the response omits it', () => {
    const oauth = useAntigravityOAuth()

    const credentials = oauth.buildCredentials(
      {
        access_token: 'access-token',
        expires_at: 1_900_000_000,
      },
      'submitted-refresh-token',
    )

    expect(credentials.refresh_token).toBe('submitted-refresh-token')
  })

  // A refresh response supplies whitespace for its refresh token. Credential
  // construction treats that response as absent and carries the submitted
  // token into the record.
  it('falls back to the submitted refresh token when the response returns whitespace', () => {
    const oauth = useAntigravityOAuth()

    const credentials = oauth.buildCredentials(
      {
        access_token: 'access-token',
        refresh_token: '   ',
        expires_at: 1_900_000_000,
      },
      'submitted-refresh-token',
    )

    expect(credentials.refresh_token).toBe('submitted-refresh-token')
  })

  // A refresh response supplies a rotated refresh token alongside the
  // submitted token. Credential construction selects the returned
  // replacement for the resulting provider record.
  it('prefers a new refresh token returned by the response', () => {
    const oauth = useAntigravityOAuth()

    const credentials = oauth.buildCredentials(
      {
        access_token: 'access-token',
        refresh_token: 'rotated-refresh-token',
        expires_at: 1_900_000_000,
      },
      'submitted-refresh-token',
    )

    expect(credentials.refresh_token).toBe('rotated-refresh-token')
  })
})
