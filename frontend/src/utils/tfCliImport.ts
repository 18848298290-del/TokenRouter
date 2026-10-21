/**
 * tfCliImport.ts captures tf CLI session fragments, discovers loopback
 * services, and sends an approved API key import. Fragment secrets are removed
 * from browser history before validation and retained only in module memory
 * for ten minutes. Discovery checks the linked port first, then scans the
 * fixed port range without proof if necessary.
 *
 * Verified targets retain their session through object identity in a WeakMap.
 * Import signs the exact serialized body and checks that session again after
 * asynchronous signing, so expiry or replacement stops a previously verified
 * send. Unverified targets remain compatible with callers that obtain explicit
 * warning confirmation. HTTP 202 acknowledges terminal receipt, not gateway
 * validation or final key persistence.
 */

/**
 * The CLI protocol version, fixed port range, discovery budget, session
 * lifetime, proof header, and fragment grammar define the browser side of
 * local import.
 */
const TF_WEB_IMPORT_PROTOCOL = 1
const TF_PORT_FIRST = 43110
const TF_PORT_LAST = 43119
const TF_DISCOVERY_TIMEOUT_MS = 30_000
const TF_SESSION_MAX_AGE_MS = 10 * 60_000
const TF_SESSION_PROOF_HEADER = 'X-TF-Session-Proof'
const TF_SESSION_FRAGMENT = /^#tfcli=1\.(4311[0-9])\.([A-Za-z0-9_-]{22})$/

/**
 * LoopbackRequestInit adds the browser loopback address-space hint to ordinary
 * fetch options.
 */
interface LoopbackRequestInit extends RequestInit {
  targetAddressSpace: 'loopback' // browser address-space hint
}

/**
 * TfCliImportSession pairs the linked local port with a mutable secret and
 * absolute expiry. Clearing session ownership overwrites the shared secret
 * buffer, and target associations are valid only while this session remains
 * current.
 */
interface TfCliImportSession {
  port: number // local service port
  /**
   * The session owns these mutable bytes; cleanup overwrites them before
   * clearing pending ownership, including when targets retain references.
   */
  secret: ArrayBuffer
  expiresAt: number // expiry epoch milliseconds
}

/**
 * TfCliTarget describes a discovered local service and its proof result.
 * Verified proof ownership belongs to this exact object in a WeakMap, so
 * copying its fields does not copy the session association.
 */
export interface TfCliTarget {
  port: number // local service port
  /**
   * Reports discovery proof success for this target object; import still checks
   * its live session association before sending credentials.
   */
  verified: boolean
}

/**
 * TfCliImportPayload carries the approved key and its source metadata to the
 * local CLI.
 */
export interface TfCliImportPayload {
  key: string // approved API key
  host: string // source site origin
  key_name?: string // source key label
  /**
   * Optional source group identifier; callers omit single-group metadata when
   * exporting a composite API key.
   */
  group_id?: number
  /**
   * Optional source group name; callers supply it with group_id only for an
   * ordinary single-group key.
   */
  group_name?: string
}

/**
 * TfCliImportResult distinguishes terminal receipt from transport, proof, and
 * service errors. A successful 202 response acknowledges receipt only; callers
 * use the terminal for final validation and persistence status.
 */
export type TfCliImportResult =
  | {
      ok: true // service acceptance flag
      status: 202 // HTTP response status
    }
  | {
      ok: false // service acceptance flag
      status?: number // HTTP response status
      error: string // reported error description
    }

/**
 * TfPingResponse represents the local service identity and optional session
 * challenge proof.
 */
interface TfPingResponse {
  service?: string // local service name
  protocol?: number // CLI protocol version
  proof?: string // base64url HMAC proof
}

/**
 * pendingSession owns retained secret bytes and sessionExpiryTimer bounds
 * their lifetime. verifiedSessions associates discovered target objects with
 * that same session so imports can reject expired or replaced ownership.
 */
let pendingSession: TfCliImportSession | null = null
let sessionExpiryTimer: number | null = null
const verifiedSessions = new WeakMap<TfCliTarget, TfCliImportSession>()

/**
 * encodeBase64Url encodes bytes with the unpadded URL-safe base64 alphabet.
 */
function encodeBase64Url(bytes: Uint8Array): string {
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  /**
   * Use the protocol URL-safe alphabet and omit padding so challenges and
   * signatures have the same canonical textual form as the local CLI.
   */
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * decodeBase64Url decodes a URL-safe base64 string into a new byte buffer.
 * Decoding errors propagate; callers perform length and canonical re-encoding
 * checks when accepting a session fragment.
 */
function decodeBase64Url(value: string): ArrayBuffer {
  const base64 = value.replace(/-/g, '+').replace(/_/g, '/')
  /**
   * Restore standard base64 padding for browser decoding, then let session
   * initialization verify byte length and canonical re-encoding before accepting
   * the secret.
   */
  const padded = base64 + '='.repeat((4 - (base64.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = new Uint8Array(binary.length)
  for (let index = 0; index < binary.length; index++) bytes[index] = binary.charCodeAt(index)
  return bytes.buffer
}

/**
 * clearSecret overwrites the supplied session buffer with zero bytes.
 */
function clearSecret(secret: ArrayBuffer): void {
  new Uint8Array(secret).fill(0)
}

/**
 * isKeysPath recognizes paths ending in keys after removing trailing slashes.
 */
function isKeysPath(pathname: string): boolean {
  return pathname.replace(/\/+$/, '').endsWith('/keys')
}

// @project-doc docs/interfaces/tf_cli_web_import.md#session_fragment
/**
 * initializeTfCliImportSession captures a valid CLI fragment for later
 * discovery and removes any tfcli fragment immediately. Call it before router
 * guards so authentication navigation cannot retain the secret in the URL.
 *
 * The history replacement precedes route and format validation, and any old
 * session is cleared before accepting a replacement. Only canonical
 * sixteen-byte secrets on Keys paths and the fixed port range are accepted. A
 * timer bounds memory retention, while later reads also enforce expiry against
 * the wall clock.
 */
export function initializeTfCliImportSession(): boolean {
  if (typeof window === 'undefined') return false

  const fragment = window.location.hash
  if (!fragment.startsWith('#tfcli=')) return getTfCliImportSession() !== null

  /**
   * Strip the fragment before route, format, or decoding checks so rejected
   * session material does not remain in browser history or subsequent
   * navigation.
   */
  window.history.replaceState(
    window.history.state,
    '',
    window.location.pathname + window.location.search,
  )
  clearTfCliImportSession()

  if (!isKeysPath(window.location.pathname)) return false

  const match = TF_SESSION_FRAGMENT.exec(fragment)
  if (!match) return false

  try {
    const port = Number(match[1])
    const secret = decodeBase64Url(match[2])
    /**
     * Re-encode decoded bytes to reject noncanonical base64url spellings; the
     * session format requires exactly sixteen bytes represented without padding.
     */
    const canonical = encodeBase64Url(new Uint8Array(secret))
    if (secret.byteLength !== 16 || canonical !== match[2]) return false

    pendingSession = { port, secret, expiresAt: Date.now() + TF_SESSION_MAX_AGE_MS }
    /**
     * The timer bounds retained secret lifetime without another request, while
     * session reads also check wall-clock expiry if timer execution is delayed.
     */
    sessionExpiryTimer = window.setTimeout(clearTfCliImportSession, TF_SESSION_MAX_AGE_MS)
    return true
  } catch {
    /**
     * Decoding failure rejects the candidate session after its fragment was
     * already removed, so malformed secret text does not remain visible in the
     * URL.
     */
    return false
  }
}

/**
 * getTfCliImportSession returns the currently retained session while it
 * remains unexpired. Expired sessions are cleared, including their secret
 * bytes, so callers cannot revive them by bypassing the expiry timer.
 */
function getTfCliImportSession(): TfCliImportSession | null {
  if (!pendingSession) return null
  if (Date.now() >= pendingSession.expiresAt) {
    clearTfCliImportSession()
    return null
  }
  return pendingSession
}

/**
 * clearTfCliImportSession cancels the expiry timer and overwrites the pending
 * secret before dropping its reference. Repeated cleanup is harmless; verified
 * target associations may remain but fail subsequent session identity checks.
 */
export function clearTfCliImportSession(): void {
  if (sessionExpiryTimer !== null && typeof window !== 'undefined') {
    window.clearTimeout(sessionExpiryTimer)
  }
  sessionExpiryTimer = null
  /**
   * Overwrite the pending buffer before releasing session ownership; retained
   * target associations then refer to a cleared session that fails identity
   * validation.
   */
  if (pendingSession) clearSecret(pendingSession.secret)
  pendingSession = null
}

/**
 * createChallenge returns a fresh URL-safe challenge from eighteen random
 * bytes.
 */
function createChallenge(): string {
  const bytes = new Uint8Array(18)
  crypto.getRandomValues(bytes)
  return encodeBase64Url(bytes)
}

/**
 * signSessionMessage signs UTF-8 message bytes with the supplied HMAC SHA-256
 * secret. It returns unpadded base64url text and propagates Web Crypto
 * failures so discovery or import can apply their own failure policy.
 */
async function signSessionMessage(secret: ArrayBuffer, message: string): Promise<string> {
  const key = await crypto.subtle.importKey(
    'raw',
    secret,
    { name: 'HMAC', hash: 'SHA-256' },
    false,
    ['sign'],
  )
  /**
   * Sign the UTF-8 message bytes and return the protocol base64url encoding so
   * the local service compares the same serialized message representation.
   */
  const signature = await crypto.subtle.sign('HMAC', key, new TextEncoder().encode(message))
  return encodeBase64Url(new Uint8Array(signature))
}

/**
 * equalProof compares proof characters after rejecting unequal string lengths.
 */
function equalProof(left: string, right: string): boolean {
  if (left.length !== right.length) return false
  let difference = 0
  for (let index = 0; index < left.length; index++) {
    difference |= left.charCodeAt(index) ^ right.charCodeAt(index)
  }
  return difference === 0
}

/**
 * loopbackRequest applies the fixed browser policies used for local CLI
 * requests. Supplied mode, credentials, caching, redirect, referrer, and
 * signal values are overridden; method, body, and headers remain caller
 * controlled.
 */
function loopbackRequest(init: RequestInit, signal?: AbortSignal): LoopbackRequestInit {
  return {
    ...init,
    mode: 'cors',
    cache: 'no-store',
    credentials: 'omit',
    redirect: 'error',
    referrerPolicy: 'no-referrer',
    signal,
    targetAddressSpace: 'loopback',
  }
}

/**
 * pingTfCli probes one port and returns a compatible service target,
 * optionally verified against a supplied session. The service name and
 * protocol version are checked before any target is accepted.
 *
 * A session adds a fresh challenge, whose returned proof is compared with the
 * locally signed message. Successful proof associates the exact returned
 * target object with that session for import. Mismatched proof leaves the
 * target unverified, while thrown transport, decoding, or cryptographic errors
 * return null. Discovery owns fallback selection and cancellation of this
 * probe.
 */
async function pingTfCli(
  port: number,
  session: TfCliImportSession | null,
  signal: AbortSignal,
): Promise<TfCliTarget | null> {
  try {
    const challenge = session ? createChallenge() : ''
    const query = challenge ? `?challenge=${encodeURIComponent(challenge)}` : ''
    const response = await fetch(
      `http://127.0.0.1:${port}/ping${query}`,
      loopbackRequest({ method: 'GET' }, signal),
    )
    if (!response.ok) return null

    const data = (await response.json()) as TfPingResponse
    if (data.service !== 'tf' || data.protocol !== TF_WEB_IMPORT_PROTOCOL) return null

    const target: TfCliTarget = { port, verified: false }
    if (session && challenge && typeof data.proof === 'string') {
      const expected = await signSessionMessage(
        session.secret,
        `tf-web-import-v1\n${port}\n${challenge}`,
      )
      target.verified = equalProof(data.proof, expected)
      if (target.verified) verifiedSessions.set(target, session)
    }
    return target
  } catch {
    /**
     * A failed ping, JSON decode, or proof operation makes this probe unavailable;
     * discovery may still try the unverified fixed-port fallback scan.
     */
    return null
  }
}

// @project-doc docs/interfaces/tf_cli_web_import.md#session_proof
/**
 * findTfCli discovers a compatible local service within a shared timeout and
 * optional caller cancellation. A live session gives its linked port the first
 * attempt, including proof verification.
 *
 * If that attempt returns no target and has not been aborted, all fixed ports
 * are probed without session proof. Results are selected in port order after
 * all probes settle, rather than first-response order. The owned timer and
 * external abort listener are removed on every exit. Cancellation normally
 * yields null through failed probes, without cancelling any separate import.
 */
export async function findTfCli(
  signal?: AbortSignal,
  timeoutMs = TF_DISCOVERY_TIMEOUT_MS,
): Promise<TfCliTarget | null> {
  if (signal?.aborted) return null

  /**
   * Use one owned abort controller for the linked probe and fallback scan so
   * timeout and caller cancellation share the same discovery budget.
   */
  const controller = new AbortController()
  /**
   * abort forwards discovery timeout or caller cancellation to the owned
   * controller.
   */
  const abort = () => controller.abort()
  signal?.addEventListener('abort', abort, { once: true })
  const timeout = window.setTimeout(abort, timeoutMs)

  try {
    const session = getTfCliImportSession()
    if (session) {
      const linked = await pingTfCli(session.port, session, controller.signal)
      if (linked || controller.signal.aborted) return linked
    }

    const ports = Array.from(
      { length: TF_PORT_LAST - TF_PORT_FIRST + 1 },
      (_, index) => TF_PORT_FIRST + index,
    )
    /**
     * Wait for all fallback probes and select by port order; these unlinked probes
     * deliberately omit session challenges and therefore produce unverified
     * targets.
     */
    const results = await Promise.all(ports.map((port) => pingTfCli(port, null, controller.signal)))
    return results.find((target): target is TfCliTarget => target !== null) ?? null
  } finally {
    /**
     * Remove both timeout and caller listener on every exit so completed discovery
     * does not retain an abort callback or timer resource.
     */
    window.clearTimeout(timeout)
    signal?.removeEventListener('abort', abort)
  }
}

/**
 * sessionProofExpired reports a missing target association or a session that
 * differs from the current live session. A missing association returns true
 * immediately. Otherwise, when the pending session has expired, the live-session
 * lookup cancels its expiry timer, overwrites its secret bytes, and drops that
 * session before comparing identities.
 */
function sessionProofExpired(session: TfCliImportSession | undefined): boolean {
  return !session || session !== getTfCliImportSession()
}

/**
 * importKeyToTf submits a caller-approved payload to a discovered target and
 * reports terminal acknowledgement. Callers retain the discovered target
 * object because verified session ownership is stored by object identity.
 *
 * The body is serialized once for both signing and sending. A verified target
 * with an expired session, failed signing, or session replacement during
 * signing returns an error before sending the key. Unverified imports omit the
 * proof header. Accepted or terminal-cancelled imports clear pending session
 * state; network failures return classified errors and leave retry policy to
 * the caller.
 */
export async function importKeyToTf(
  target: TfCliTarget,
  payload: TfCliImportPayload,
  signal?: AbortSignal,
): Promise<TfCliImportResult> {
  /**
   * Serialize the payload once before signing and reuse that exact body for
   * fetch, preventing signature disagreement from a second serialization or
   * field-order change.
   */
  const body = JSON.stringify({ ...payload, version: TF_WEB_IMPORT_PROTOCOL })
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const verifiedSession = verifiedSessions.get(target)
  if (target.verified && sessionProofExpired(verifiedSession)) {
    verifiedSessions.delete(target)
    return { ok: false, error: 'session_expired' }
  }

  try {
    if (verifiedSession) {
      let proof: string
      try {
        proof = await signSessionMessage(
          verifiedSession.secret,
          `tf-web-import-v1\n${target.port}\nimport\n${body}`,
        )
      } catch {
        /**
         * A previously verified target cannot silently downgrade during a send;
         * discard its proof association and return an error before transmitting key
         * bytes.
         */
        verifiedSessions.delete(target)
        return { ok: false, error: 'session_proof_unavailable' }
      }
      /**
       * Signing can suspend while the session expires or is replaced; recheck
       * ownership before adding proof or sending any key bytes.
       */
      if (sessionProofExpired(verifiedSession)) {
        verifiedSessions.delete(target)
        return { ok: false, error: 'session_expired' }
      }
      headers[TF_SESSION_PROOF_HEADER] = proof
    }

    const response = await fetch(
      `http://127.0.0.1:${target.port}/import`,
      loopbackRequest({ method: 'POST', headers, body }, signal),
    )
    let data: {
      ok?: boolean // service acceptance flag
      error?: string // reported error description
    } = {}
    try {
      data = (await response.json()) as {
        ok?: boolean // service acceptance flag
        error?: string // reported error description
      }
    } catch {
      /**
       * A non-JSON service response leaves the response data empty, so status
       * handling reports an unexpected response instead of treating acknowledgement
       * as success.
       */
    }

    if (response.status === 202 && data.ok === true) {
      /**
       * A terminal acknowledgement consumes the target proof association and pending
       * secret before reporting receipt; final gateway validation remains the
       * terminal responsibility.
       */
      verifiedSessions.delete(target)
      clearTfCliImportSession()
      return { ok: true, status: 202 }
    }

    if (data.error === 'cancelled') clearTfCliImportSession()
    return {
      ok: false,
      status: response.status,
      error: typeof data.error === 'string' ? data.error : 'unexpected_response',
    }
  } catch (error) {
    /**
     * Distinguish caller abort from other transport failures without clearing the
     * pending session automatically, leaving subsequent retry or cancellation
     * decisions with the import UI.
     */
    if (signal?.aborted || (error instanceof DOMException && error.name === 'AbortError')) {
      return { ok: false, error: 'cancelled' }
    }
    return { ok: false, error: 'network_error' }
  }
}
