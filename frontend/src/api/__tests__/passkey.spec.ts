import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * { get, post, patch, remove, credentialGet } provide the controlled request
 * fixtures used by passkey tests to inspect their observable results.
 */
const { get, post, patch, remove, credentialGet } = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  patch: vi.fn(),
  remove: vi.fn(),
  credentialGet: vi.fn(),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get,
    post,
    patch,
    delete: remove,
  },
}))

import { passkeyAPI } from '@/api/passkey'

/**
 * passkey.spec.ts checks WebAuthn assertion conversion with deterministic
 * browser credential bytes. Mocked begin and finish requests protect
 * ceremony-token pairing and base64url serialization, while a separate case
 * limits captcha proof to the begin request.
 */

/**
 * FakePublicKeyCredential supplies deterministic assertion bytes for browser
 * credential serialization tests.
 */
class FakePublicKeyCredential {
  id = 'credential-id' // Record identifier value.
  rawId = Uint8Array.from([1, 2, 3]).buffer // Binary credential identifier.
  type = 'public-key' // Represented record category.
  authenticatorAttachment = 'platform' // Authenticator attachment category.
  response = {
    authenticatorData: Uint8Array.from([4, 5]).buffer,
    clientDataJSON: Uint8Array.from([6, 7]).buffer,
    signature: Uint8Array.from([8, 9]).buffer,
    userHandle: Uint8Array.from([10, 11]).buffer,
  } // Browser credential response.

  /**
   * getClientExtensionResults returns an empty extension result for the fake
   * browser credential.
   */
  getClientExtensionResults(): AuthenticationExtensionsClientOutputs {
    return {}
  }
}

describe('passkey api', () => {
  beforeEach(() => {
    get.mockReset()
    post.mockReset()
    patch.mockReset()
    remove.mockReset()
    credentialGet.mockReset()

    vi.stubGlobal('PublicKeyCredential', FakePublicKeyCredential)
    Object.defineProperty(window, 'PublicKeyCredential', {
      configurable: true,
      value: FakePublicKeyCredential,
    })
    Object.defineProperty(navigator, 'credentials', {
      configurable: true,
      value: { get: credentialGet },
    })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  /**
   * A server assertion challenge and fake browser credential contain known byte
   * sequences. Login decodes the challenge and sends base64url credential fields
   * with the matching ceremony token.
   */
  it('converts assertion options and response bytes to WebAuthn JSON', async () => {
    post
      .mockResolvedValueOnce({
        data: {
          session_token: 'one-time-session',
          options: {
            publicKey: {
              challenge: 'AQID',
              rpId: 'tokenrouter.example.com',
              userVerification: 'required',
            },
          },
        },
      })
      .mockResolvedValueOnce({
        data: {
          access_token: 'access',
          token_type: 'Bearer',
          user: { id: 1 },
        },
      })
    credentialGet.mockResolvedValue(new FakePublicKeyCredential())

    await passkeyAPI.login()

    expect(post).toHaveBeenNthCalledWith(1, '/auth/passkey/login/begin')
    const request = credentialGet.mock.calls[0][0] as CredentialRequestOptions
    expect(Array.from(new Uint8Array(request.publicKey!.challenge))).toEqual([1, 2, 3])
    expect(request.publicKey!.userVerification).toBe('required')

    expect(post).toHaveBeenNthCalledWith(2, '/auth/passkey/login/finish', {
      session_token: 'one-time-session',
      credential: {
        id: 'credential-id',
        rawId: 'AQID',
        type: 'public-key',
        authenticatorAttachment: 'platform',
        clientExtensionResults: {},
        response: {
          authenticatorData: 'BAU',
          clientDataJSON: 'Bgc',
          signature: 'CAk',
          userHandle: 'Cgs',
        },
      },
    })
  })
  /**
   * Passkey login receives Tencent captcha proof and a browser credential. The
   * proof is sent with begin and excluded from the credential-bearing finish
   * payload.
   */
  it('sends Tencent captcha proof only with the passkey begin request', async () => {
    post
      .mockResolvedValueOnce({
        data: {
          session_token: 'one-time-session',
          options: {
            publicKey: {
              challenge: 'AQID',
              rpId: 'tokenrouter.example.com',
              userVerification: 'required',
            },
          },
        },
      })
      .mockResolvedValueOnce({
        data: { access_token: 'access', token_type: 'Bearer', user: { id: 1 } },
      })
    credentialGet.mockResolvedValue(new FakePublicKeyCredential())

    await passkeyAPI.login({
      tencent_captcha_ticket: 'ticket-value',
      tencent_captcha_randstr: '@rand-value',
    })

    expect(post).toHaveBeenNthCalledWith(1, '/auth/passkey/login/begin', {
      tencent_captcha_ticket: 'ticket-value',
      tencent_captcha_randstr: '@rand-value',
    })
    expect(post.mock.calls[1][1]).not.toEqual(
      expect.objectContaining({
        tencent_captcha_ticket: expect.anything(),
        tencent_captcha_randstr: expect.anything(),
      }),
    )
  })
})
