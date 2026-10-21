import axios from 'axios'
import type { ApiResponse } from '@/api/common.types'
import { buildApiUrl } from './url'

/**
 * tokenRefresh.ts rotates browser authentication tokens and reconciles refreshes
 * shared by calls and tabs. Each attempt captures the refresh token and user
 * identity before waiting, and stored replacement tokens are usable only while
 * their expiry and identity still match the attempt. The shared promise
 * coalesces calls within this document.
 *
 * Web Locks serialize refresh requests across participating tabs. Browsers
 * without that coordination can race on a refresh token, so a rejected request
 * waits for the winning tab to publish its pair. Publication writes access token
 * and expiry before refresh token, which acts as the completion marker. A logout
 * or changed user prevents an old response from restoring that session.
 */

/**
 * AUTH_TOKEN_KEY names the access-token storage entry read by browser request
 * interceptors before dispatch.
 */
const AUTH_TOKEN_KEY = 'auth_token'

/**
 * AUTH_USER_KEY names the serialized user entry parsed to associate refresh
 * attempts with their starting identity.
 */
const AUTH_USER_KEY = 'auth_user'

/**
 * REFRESH_TOKEN_KEY names the rotation credential and final publication marker
 * observed by peer refresh attempts.
 */
const REFRESH_TOKEN_KEY = 'refresh_token'

/**
 * TOKEN_EXPIRES_AT_KEY names the absolute millisecond expiry checked before
 * accepting a peer-published token pair.
 */
const TOKEN_EXPIRES_AT_KEY = 'token_expires_at'

/**
 * TOKEN_REFRESH_LOCK_NAME identifies the origin-wide Web Lock used to serialize
 * participating tabs' refresh requests.
 */
const TOKEN_REFRESH_LOCK_NAME = 'tokenrouter-auth-token-refresh'

/**
 * TOKEN_REFRESH_TIMEOUT_MS bounds the refresh HTTP request and contributes to
 * the deadline for uncoordinated peer reconciliation.
 */
const TOKEN_REFRESH_TIMEOUT_MS = 30_000

/**
 * PEER_REFRESH_WAIT_MS bounds the short reconciliation period used when an
 * ordinary refresh failure may coincide with peer publication.
 */
const PEER_REFRESH_WAIT_MS = 1_000

/**
 * PEER_REFRESH_GRACE_MS extends the winning request's timeout budget to allow
 * publication before an uncoordinated loser stops waiting.
 */
const PEER_REFRESH_GRACE_MS = 1_000

/**
 * PEER_REFRESH_POLL_MS spaces storage observations while a rejected refresh
 * attempt waits for another tab to publish credentials.
 */
const PEER_REFRESH_POLL_MS = 25

/**
 * RefreshTokenResponse contains the rotated credentials and lifetime returned by
 * the refresh endpoint.
 */
export interface RefreshTokenResponse {
  access_token: string // Bearer access token.
  refresh_token: string // Session rotation token.
  expires_in: number // Lifetime in seconds.
  token_type: string // Authorization scheme name.
}

/**
 * RefreshAuthTokensOptions carries the failed request's token for detecting a
 * peer's already-published replacement.
 */
export interface RefreshAuthTokensOptions {
  /**
   * Access token carried by the request that received 401; a different stored
   * token can indicate peer refresh completion.
   */
  failedAccessToken?: string | null // Failed Access Token.
}

/**
 * AuthSnapshot captures storage values used to associate a refresh attempt with
 * its starting session.
 */
interface AuthSnapshot {
  accessToken: string | null // Captured access token.
  /**
   * The captured refresh token identifies the starting session and is compared
   * before publication to detect another rotation or logout.
   */
  refreshToken: string // Captured refresh token.
  /**
   * This stores the captured numeric expiry; current token validation rereads
   * storage rather than relying on this earlier value.
   */
  expiresAt: number // Expiry timestamp milliseconds.
  /**
   * Parsed user identity is compared during peer adoption and response
   * publication; null represents missing or invalid stored user data.
   */
  userID: number | null // Captured user identifier.
}

/**
 * inFlightRefresh holds the current document's shared rotation promise until
 * that exact attempt settles.
 */
let inFlightRefresh: Promise<RefreshTokenResponse> | null = null

/**
 * getStoredUserID reads the numeric identity from the serialized browser user.
 * Missing, malformed, nonfinite and nonpositive identifiers yield null, so
 * refresh comparisons share one representation for unavailable user identity.
 */
function getStoredUserID(): number | null {
  const rawUser = localStorage.getItem(AUTH_USER_KEY)
  if (!rawUser) {
    return null
  }

  try {
    /**
     * Parse the stored user defensively because malformed identity JSON must
     * disable matching by a positive ID rather than break reconciliation.
     */
    const id = Number(
      (
        JSON.parse(rawUser) as {
          id?: unknown // Record identifier value.
        }
      ).id,
    )
    return Number.isFinite(id) && id > 0 ? id : null
  } catch {
    return null
  }
}

/**
 * readAuthSnapshot captures the current credentials, expiry and parsed user
 * identifier. A missing refresh token rejects the attempt immediately; other
 * fields retain their stored or parsed values for subsequent checks.
 */
function readAuthSnapshot(): AuthSnapshot {
  const refreshToken = localStorage.getItem(REFRESH_TOKEN_KEY)
  if (!refreshToken) {
    throw new Error('No refresh token available')
  }

  return {
    accessToken: localStorage.getItem(AUTH_TOKEN_KEY),
    refreshToken,
    expiresAt: Number(localStorage.getItem(TOKEN_EXPIRES_AT_KEY)),
    userID: getStoredUserID(),
  }
}

/**
 * readStoredTokenPair returns currently stored credentials that belong to the
 * snapshot's user and have a future finite expiry. It returns null when either
 * token is missing, the expiry is invalid or expired, or the parsed user
 * identity differs. Remaining lifetime is rounded upward to seconds and bounded
 * at one, allowing a peer result to use the same response shape as a refresh
 * response.
 */
function readStoredTokenPair(snapshot: AuthSnapshot): RefreshTokenResponse | null {
  const accessToken = localStorage.getItem(AUTH_TOKEN_KEY)
  const refreshToken = localStorage.getItem(REFRESH_TOKEN_KEY)
  const expiresAt = Number(localStorage.getItem(TOKEN_EXPIRES_AT_KEY))

  if (
    !accessToken ||
    !refreshToken ||
    !Number.isFinite(expiresAt) ||
    expiresAt <= Date.now() ||
    getStoredUserID() !== snapshot.userID
  ) {
    return null
  }

  return {
    access_token: accessToken,
    refresh_token: refreshToken,
    /**
     * Convert the remaining millisecond lifetime to whole seconds, rounding
     * upward so a still-valid peer token retains a positive lifetime.
     */
    expires_in: Math.max(1, Math.ceil((expiresAt - Date.now()) / 1000)),
    token_type: 'Bearer',
  }
}

/**
 * readPeerRefreshResult identifies a usable replacement published for the
 * captured session. A valid pair with a changed refresh token proves publication
 * completed; alternatively, a failed request may have carried an access token
 * older than the snapshot's current access token. Mere remaining lifetime is
 * insufficient evidence of rotation, so an unchanged pair returns null and the
 * caller continues its refresh or reconciliation attempt.
 */
function readPeerRefreshResult(
  snapshot: AuthSnapshot,
  failedAccessToken?: string | null,
): RefreshTokenResponse | null {
  const storedPair = readStoredTokenPair(snapshot)
  if (!storedPair) {
    return null
  }

  if (storedPair.refresh_token !== snapshot.refreshToken) {
    return storedPair
  }

  if (
    failedAccessToken &&
    snapshot.accessToken !== failedAccessToken &&
    storedPair.access_token === snapshot.accessToken
  ) {
    return storedPair
  }

  return null
}

/**
 * waitForPeerRefresh polls for a usable peer result until the supplied
 * millisecond deadline. It defaults to a short reconciliation window, yields
 * between observations, and performs a final read after the loop.
 */
async function waitForPeerRefresh(
  snapshot: AuthSnapshot,
  failedAccessToken?: string | null,
  deadline = Date.now() + PEER_REFRESH_WAIT_MS,
): Promise<RefreshTokenResponse | null> {
  while (Date.now() < deadline) {
    const peerResult = readPeerRefreshResult(snapshot, failedAccessToken)
    if (peerResult) {
      return peerResult
    }
    /**
     * Yield between storage observations so a peer response can publish
     * credentials while this rejected request waits for reconciliation.
     */
    await new Promise((resolve) => window.setTimeout(resolve, PEER_REFRESH_POLL_MS))
  }

  return readPeerRefreshResult(snapshot, failedAccessToken)
}

/**
 * persistTokenPair publishes rotated credentials through three ordered
 * localStorage writes. The access token is written first and its absolute
 * millisecond expiry second, before the refresh token changes. Peer
 * reconciliation treats that final change as evidence that the pair is
 * available, so reversing this order could expose a replacement refresh token
 * alongside stale companion values. Storage failures propagate and can leave
 * preceding writes visible to other readers.
 */
function persistTokenPair(tokens: RefreshTokenResponse): void {
  localStorage.setItem(AUTH_TOKEN_KEY, tokens.access_token)
  localStorage.setItem(TOKEN_EXPIRES_AT_KEY, String(Date.now() + tokens.expires_in * 1000))
  /**
   * Publish the refresh token last so peer tabs can interpret its change as
   * completion of the access-token and expiry writes.
   */
  localStorage.setItem(REFRESH_TOKEN_KEY, tokens.refresh_token)
}

/**
 * requestTokenPair exchanges the snapshot's refresh token and returns either its
 * validated response or a replacement published by a peer. Before persistence it
 * compares both the captured refresh token and parsed user ID, preventing an old
 * response from repopulating a logged-out or changed session. An uncoordinated
 * 4xx loser waits through the original request timeout plus grace; other
 * failures receive a shorter reconciliation window before the original error is
 * rethrown.
 */
async function requestTokenPair(
  snapshot: AuthSnapshot,
  failedAccessToken?: string | null,
  mayHaveUncoordinatedPeer = false,
): Promise<RefreshTokenResponse> {
  /**
   * An uncoordinated loser may receive rejection while the winning HTTP request is
   * still running, so its reconciliation deadline includes that request's timeout.
   */
  const peerRefreshDeadline = Date.now() + TOKEN_REFRESH_TIMEOUT_MS + PEER_REFRESH_GRACE_MS

  try {
    const response = await axios.post<ApiResponse<RefreshTokenResponse>>(
      buildApiUrl('/auth/refresh'),
      { refresh_token: snapshot.refreshToken },
      { headers: { 'Content-Type': 'application/json' }, timeout: TOKEN_REFRESH_TIMEOUT_MS },
    )
    const payload = response.data
    if (payload.code !== 0 || !payload.data) {
      throw new Error(payload.message || 'Token refresh failed')
    }

    if (
      localStorage.getItem(REFRESH_TOKEN_KEY) !== snapshot.refreshToken ||
      getStoredUserID() !== snapshot.userID
    ) {
      const peerResult = readPeerRefreshResult(snapshot, failedAccessToken)
      if (peerResult) {
        return peerResult
      }
      throw new Error('Session changed during token refresh')
    }

    /**
     * Publish only after token and user comparisons pass, preventing this
     * delayed response from replacing credentials belonging to a newer session.
     */
    persistTokenPair(payload.data)
    return payload.data
  } catch (error) {
    /**
     * Uncoordinated 4xx rejection can precede the peer's successful response; allow
     * the shared deadline for publication, while other failures get a short
     * reconciliation window.
     */
    const responseStatus = (
      error as {
        response?: {
          status?: unknown // Current server-reported status.
        } // HTTP refresh error response.
      }
    ).response?.status
    const isTokenRejection =
      typeof responseStatus === 'number' && responseStatus >= 400 && responseStatus < 500
    const peerResult = await waitForPeerRefresh(
      snapshot,
      failedAccessToken,
      isTokenRejection && mayHaveUncoordinatedPeer
        ? peerRefreshDeadline
        : Date.now() + PEER_REFRESH_WAIT_MS,
    )
    if (peerResult) {
      return peerResult
    }
    throw error
  }
}

/**
 * runRefresh captures session identity and selects the cross-tab coordination
 * path for one refresh attempt. When Web Locks are available, the callback
 * checks storage after admission because another tab may have completed rotation
 * while this caller waited. Otherwise it permits the request helper's longer
 * reconciliation for concurrent token rejection. Both paths reuse a peer's
 * qualifying pair before sending another request, while retaining the starting
 * snapshot for identity checks.
 */
async function runRefresh(options: RefreshAuthTokensOptions): Promise<RefreshTokenResponse> {
  const snapshot = readAuthSnapshot()
  /**
   * refresh checks for a peer-published pair before issuing the token exchange.
   * The flag tells requestTokenPair whether an uncoordinated competing tab may
   * require a longer rejection-reconciliation window.
   */
  const refresh = async (mayHaveUncoordinatedPeer = false): Promise<RefreshTokenResponse> => {
    const peerResult = readPeerRefreshResult(snapshot, options.failedAccessToken)
    if (peerResult) {
      return peerResult
    }
    return requestTokenPair(snapshot, options.failedAccessToken, mayHaveUncoordinatedPeer)
  }

  if (typeof navigator !== 'undefined' && navigator.locks) {
    /**
     * Recheck peer publication inside the lock callback because another tab may
     * rotate the shared session before this attempt acquires admission.
     */
    return navigator.locks.request(TOKEN_REFRESH_LOCK_NAME, () => refresh(false))
  }

  return refresh(true)
}

/**
 * refreshAuthTokens returns the document's pending refresh or starts one through
 * runRefresh. Concurrent callers share the first attempt, including its captured
 * session and failed-token context, until that promise settles. Settlement
 * clears the slot only when it still points to that promise, so cleanup cannot
 * erase a later pending attempt. Cross-tab ordering and publication checks
 * remain owned by runRefresh and requestTokenPair, and failures are delivered
 * through the shared promise.
 */
export function refreshAuthTokens(
  options: RefreshAuthTokensOptions = {},
): Promise<RefreshTokenResponse> {
  if (inFlightRefresh) {
    return inFlightRefresh
  }

  const pending = runRefresh(options)
  /**
   * Publish the attempt promise before returning it so concurrent callers join
   * the same refresh and observe the same outcome.
   */
  inFlightRefresh = pending
  /**
   * clearPending releases the shared slot when it still belongs to this settled
   * attempt. Both fulfillment and rejection use it so a subsequent refresh can
   * start after either outcome.
   */
  const clearPending = (): void => {
    if (inFlightRefresh === pending) {
      inFlightRefresh = null
    }
  }
  void pending.then(clearPending, clearPending)
  return pending
}
