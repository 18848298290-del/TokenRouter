import { Buffer } from 'node:buffer'
import { webcrypto } from 'node:crypto'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  clearTfCliImportSession,
  findTfCli,
  importKeyToTf,
  initializeTfCliImportSession,
} from '../tfCliImport'

/**
 * tfCliImport.spec.ts checks CLI session capture, proof signing, and loopback
 * import acknowledgements using fixed cryptographic vectors. Synthetic browser
 * history and fetch responses exercise fragment removal, expiry, verification
 * downgrade, and refusal to send a key after proof failure.
 *
 * The crypto fixture replaces randomness with deterministic bytes while
 * retaining Node Web Crypto for the actual HMAC operations. Its buffer adapter
 * preserves typed-array offsets and lengths so fixed signatures cover the same
 * bytes that browser code supplies. Expected JSON and signature strings bind
 * serialization order to the transmitted body. These tests use local fixtures
 * and do not contact a running CLI service.
 */

/**
 * These fixed test vectors connect a canonical session secret, deterministic
 * challenge, and exact ping/import signatures so serialization changes become
 * observable.
 */
const sessionSecret = 'AAECAwQFBgcICQoLDA0ODw'
const deterministicChallenge = 'AAECAwQFBgcICQoLDA0ODxAR'
const deterministicPingProof = 'CTCnT8q258NPygW9q6J1TMhisl3PBhbk7FCQ-YXgfEk'
const deterministicImportProof = 'f5CZvZfgAVJoIiTFPt2SWirCyvzoKJvdK9qTdLRSbWU'

/**
 * setURL replaces the test browser history entry with a controlled path.
 */
function setURL(path: string): void {
  window.history.replaceState({}, '', path)
}

/**
 * toNodeBuffer copies a browser BufferSource into a Node Buffer for the
 * cryptographic test adapter. An ArrayBuffer uses its entire extent, while a
 * view selects only its byteOffset and byteLength.
 *
 * That slice distinction matters because a view can share a larger backing
 * buffer containing bytes outside the signed message or secret. Copying the
 * selected bytes through an ordinary array produces a Node-owned buffer for
 * Web Crypto without widening the source range. The original browser buffer
 * remains unchanged by this conversion.
 */
function toNodeBuffer(source: BufferSource): Buffer {
  const bytes = ArrayBuffer.isView(source)
    ? new Uint8Array(source.buffer, source.byteOffset, source.byteLength)
    : new Uint8Array(source)
  return Buffer.from(Array.from(bytes))
}

/**
 * deterministicCrypto returns a browser-shaped crypto fixture with
 * deterministic random bytes. HMAC operations delegate to Node Web Crypto
 * after byte conversion, allowing fixed expected proof strings to test the
 * real signing algorithm.
 */
function deterministicCrypto(): Crypto {
  const subtle = {
    /**
     * importKey adapts raw browser key bytes to Node Web Crypto for tests.
     * Unsupported key formats throw immediately, while the requested algorithm and
     * usage parameters pass through to the underlying import.
     */
    importKey(
      format: string,
      keyData: BufferSource,
      algorithm: AlgorithmIdentifier,
      extractable: boolean,
      keyUsages: KeyUsage[],
    ) {
      if (format !== 'raw') throw new Error(`Unexpected key format: ${format}`)
      return webcrypto.subtle.importKey(
        'raw',
        toNodeBuffer(keyData),
        algorithm as HmacImportParams,
        extractable,
        keyUsages,
      )
    },
    /**
     * sign adapts message bytes and delegates HMAC generation to Node Web Crypto.
     */
    sign(algorithm: AlgorithmIdentifier, key: CryptoKey, data: BufferSource) {
      return webcrypto.subtle.sign(algorithm, key, toNodeBuffer(data))
    },
  } as unknown as SubtleCrypto

  return {
    subtle,
    /**
     * getRandomValues fills the supplied view with deterministic ascending bytes
     * for test challenges. It returns the same view and leaves null unchanged, so
     * callers can assert reproducible challenge and proof values.
     */
    getRandomValues<T extends ArrayBufferView | null>(array: T): T {
      if (!array) return array
      const bytes = new Uint8Array(array.buffer, array.byteOffset, array.byteLength)
      bytes.forEach((_, index) => {
        bytes[index] = index
      })
      return array
    },
    randomUUID: webcrypto.randomUUID.bind(webcrypto),
  } as Crypto
}

describe('tfCliImport session lifecycle', () => {
  beforeEach(() => {
    /**
     * Reset retained session ownership before replacing browser history, so each
     * scenario starts without a prior secret, target proof, or expiry timer.
     */
    clearTfCliImportSession()
    setURL('/keys')
  })

  afterEach(() => {
    clearTfCliImportSession()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  /**
   * The Keys URL contains a canonical session fragment and ordinary query data.
   * Initialization must capture the session, remove only the fragment, and avoid
   * persistent storage writes.
   */
  it('captures a canonical session fragment and removes it from the URL immediately', () => {
    const storageSpy = vi.spyOn(Storage.prototype, 'setItem')
    setURL(`/keys?tab=active#tfcli=1.43110.${sessionSecret}`)

    expect(initializeTfCliImportSession()).toBe(true)
    expect(window.location.pathname + window.location.search + window.location.hash).toBe(
      '/keys?tab=active',
    )
    expect(storageSpy).not.toHaveBeenCalled()
  })

  /**
   * Session fragments contain unsupported versions, ports, encodings, or
   * malformed text. Each fragment must be removed immediately while
   * initialization refuses to retain a usable session.
   */
  it.each([
    '#tfcli=2.43110.AAECAwQFBgcICQoLDA0ODw',
    '#tfcli=1.43120.AAECAwQFBgcICQoLDA0ODw',
    '#tfcli=1.43110.AAAAAAAAAAAAAAAAAAAAAB',
    '#tfcli=malformed',
  ])('clears malformed tfcli fragments without retaining a session: %s', (fragment) => {
    setURL(`/keys${fragment}`)

    expect(initializeTfCliImportSession()).toBe(false)
    expect(window.location.hash).toBe('')
  })

  /**
   * A canonical-looking session fragment appears on the login route instead of
   * Keys. Initialization must remove the fragment without accepting its secret
   * into pending session state.
   */
  it('clears a tfcli fragment outside the Keys route without accepting it', () => {
    setURL(`/login#tfcli=1.43110.${sessionSecret}`)

    expect(initializeTfCliImportSession()).toBe(false)
    expect(window.location.hash).toBe('')
  })

  /**
   * A retained session reaches its ten-minute lifetime under a controlled clock.
   * A later initialization without a new fragment must report that no live
   * session remains.
   */
  it('expires an in-memory session after ten minutes', () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-03T00:00:00Z'))
    setURL(`/keys#tfcli=1.43110.${sessionSecret}`)
    initializeTfCliImportSession()

    vi.setSystemTime(new Date('2026-09-03T00:10:00Z'))

    expect(initializeTfCliImportSession()).toBe(false)
  })
})

describe('tfCliImport localhost protocol', () => {
  beforeEach(() => {
    clearTfCliImportSession()
    setURL('/keys')
    vi.stubGlobal('crypto', deterministicCrypto())
  })

  afterEach(() => {
    clearTfCliImportSession()
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  /**
   * The linked service returns a known challenge proof and accepts an import.
   * Discovery must verify it, signing must cover the exact transmitted JSON
   * body, and acknowledgement clears session state.
   */
  it('verifies the linked tf session and signs the exact import body', async () => {
    setURL(`/keys#tfcli=1.43110.${sessionSecret}`)
    expect(initializeTfCliImportSession()).toBe(true)
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/ping')) {
        expect(new URL(url).searchParams.get('challenge')).toBe(deterministicChallenge)
        return new Response(
          JSON.stringify({
            ok: true,
            service: 'tf',
            protocol: 1,
            version: 'dev',
            proof: deterministicPingProof,
          }),
          { status: 200 },
        )
      }

      expect(url).toBe('http://127.0.0.1:43110/import')
      expect(init?.body).toBe(
        '{"key":"sk-test","host":"https://tokenflux.dev","key_name":"laptop","version":1}',
      )
      expect((init?.headers as Record<string, string>)['X-TF-Session-Proof']).toBe(
        deterministicImportProof,
      )
      expect(init).toMatchObject({
        method: 'POST',
        mode: 'cors',
        cache: 'no-store',
        credentials: 'omit',
        redirect: 'error',
        referrerPolicy: 'no-referrer',
        targetAddressSpace: 'loopback',
      })
      return new Response(JSON.stringify({ ok: true }), { status: 202 })
    })
    vi.stubGlobal('fetch', fetchMock)

    const target = await findTfCli()

    expect(target).toEqual({ port: 43110, verified: true })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const pingInit = fetchMock.mock.calls[0]?.[1] as RequestInit & {
      targetAddressSpace?: string // browser address-space hint
    }
    expect(pingInit).toMatchObject({
      method: 'GET',
      mode: 'cors',
      cache: 'no-store',
      credentials: 'omit',
      redirect: 'error',
      referrerPolicy: 'no-referrer',
      targetAddressSpace: 'loopback',
    })

    const result = await importKeyToTf(target!, {
      key: 'sk-test',
      host: 'https://tokenflux.dev',
      key_name: 'laptop',
    })

    expect(result).toEqual({ ok: true, status: 202 })
    expect(initializeTfCliImportSession()).toBe(false)
  })

  /**
   * A discovered verified target outlives its pending session expiry timer.
   * Import must return session_expired without issuing another fetch containing
   * the selected API key.
   */
  it('does not send a key after the verified session proof expires', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-09-03T00:00:00Z'))
    setURL(`/keys#tfcli=1.43110.${sessionSecret}`)
    expect(initializeTfCliImportSession()).toBe(true)
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ok: true,
          service: 'tf',
          protocol: 1,
          proof: deterministicPingProof,
        }),
        { status: 200 },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const target = await findTfCli()
    expect(target).toEqual({ port: 43110, verified: true })

    await vi.advanceTimersByTimeAsync(10 * 60_000)
    await expect(
      importKeyToTf(target!, {
        key: 'sk-test',
        host: 'https://tokenflux.dev',
      }),
    ).resolves.toEqual({ ok: false, error: 'session_expired' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  /**
   * A verified target remains live but Web Crypto rejects import signing. The
   * helper must return the proof-unavailable error and avoid sending a request
   * with the key.
   */
  it('does not send the key when import proof signing fails', async () => {
    setURL(`/keys#tfcli=1.43110.${sessionSecret}`)
    expect(initializeTfCliImportSession()).toBe(true)
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ok: true,
          service: 'tf',
          protocol: 1,
          proof: deterministicPingProof,
        }),
        { status: 200 },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const target = await findTfCli()
    expect(target).toEqual({ port: 43110, verified: true })
    vi.spyOn(webcrypto.subtle, 'sign').mockRejectedValueOnce(new Error('Web Crypto unavailable'))

    await expect(
      importKeyToTf(target!, {
        key: 'sk-test',
        host: 'https://tokenflux.dev',
      }),
    ).resolves.toEqual({ ok: false, error: 'session_proof_unavailable' })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  /**
   * The linked service identifies itself correctly but returns the wrong proof.
   * Discovery must retain that responding port as unverified without falsely
   * claiming session authentication.
   */
  it('keeps a responding linked service usable but unverified when its proof is wrong', async () => {
    setURL(`/keys#tfcli=1.43110.${sessionSecret}`)
    expect(initializeTfCliImportSession()).toBe(true)
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          ok: true,
          service: 'tf',
          protocol: 1,
          proof: 'wrong-proof',
        }),
        { status: 200 },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    await expect(findTfCli()).resolves.toEqual({ port: 43110, verified: false })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  /**
   * An unlinked service responds within the allowed port range and rejects
   * import. Discovery must return an unverified target, and import must omit the
   * session proof header.
   */
  it('discovers an unlinked tf service without sending a session proof', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith(':43113/ping')) {
        return new Response(JSON.stringify({ ok: true, service: 'tf', protocol: 1 }), {
          status: 200,
        })
      }
      if (url.endsWith(':43113/import')) {
        expect((init?.headers as Record<string, string>)['X-TF-Session-Proof']).toBeUndefined()
        return new Response(JSON.stringify({ ok: false, error: 'rejected' }), { status: 409 })
      }
      return new Response(JSON.stringify({ ok: false }), { status: 404 })
    })
    vi.stubGlobal('fetch', fetchMock)

    const target = await findTfCli()

    expect(target).toEqual({ port: 43113, verified: false })
    await expect(
      importKeyToTf(target!, {
        key: 'sk-test',
        host: 'https://tokenflux.dev',
      }),
    ).resolves.toEqual({ ok: false, status: 409, error: 'rejected' })
  })

  /**
   * Every probed response identifies itself as a different local service.
   * Discovery must return null even though those responses use a successful HTTP
   * status and the expected protocol number.
   */
  it('rejects services that do not identify as tf protocol v1', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            ok: true,
            service: 'other',
            protocol: 1,
          }),
          { status: 200 },
        ),
      ),
    )

    await expect(findTfCli(undefined, 1_000)).resolves.toBeNull()
  })
})
