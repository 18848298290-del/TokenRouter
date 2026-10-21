import { apiClient } from './client'
import type { ActionCaptchaRequestProof, AuthResponse } from '@/api/auth.types'

/**
 * passkey.ts connects browser WebAuthn ceremonies to the server's session-based
 * begin and finish endpoints. Server JSON encodes binary fields as unpadded
 * base64url, while navigator.credentials consumes and returns ArrayBuffers.
 * Registration converts challenge, user identity and excluded credential IDs;
 * login converts challenge and permitted credential IDs.
 *
 * The finish request carries the session token returned by its matching begin
 * response together with the serialized browser credential. A missing or
 * incompatible credential result rejects before finish is sent. Optional browser
 * transport reporting becomes an empty list when unavailable, and nullable
 * assertion user handles remain null during encoding. Credential listing, naming
 * and deletion use ordinary authenticated requests.
 */

/**
 * PasskeyCredentialSummary describes a registered credential's name, usage
 * timestamps and authenticator backup state.
 */
export interface PasskeyCredentialSummary {
  id: number // Record identifier value.
  name: string // Human-readable display name.
  created_at: string // Record creation timestamp.
  last_used_at?: string // Last used at.
  backup: boolean // Authenticator backup flag.
}

/**
 * CeremonyOptionsResponse pairs a server ceremony token with WebAuthn options
 * for the matching finish request.
 */
interface CeremonyOptionsResponse {
  session_token: string // Ceremony correlation token.
  options: {
    publicKey: Record<string, unknown> // WebAuthn public-key options.
  } // Browser ceremony options.
}

/**
 * requirePasskeySupport rejects when this browser lacks credential or public-key
 * support. Call it before starting a ceremony so unsupported environments fail
 * before a begin request is sent.
 */
function requirePasskeySupport(): void {
  if (!window.PublicKeyCredential || !navigator.credentials) {
    throw new Error('Passkeys are not supported by this browser')
  }
}

/**
 * base64URLToBuffer decodes an unpadded URL-safe base64 value into an
 * ArrayBuffer. It restores the standard alphabet and required padding before
 * decoding; malformed input propagates the browser decoder's error.
 */
function base64URLToBuffer(value: string): ArrayBuffer {
  /**
   * Restore the standard base64 alphabet and padding before decoding because
   * HTTP credential fields use the unpadded URL-safe representation.
   */
  const normalized = value.replace(/-/g, '+').replace(/_/g, '/')
  const padded = normalized + '='.repeat((4 - (normalized.length % 4)) % 4)
  const binary = atob(padded)
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0))
  return bytes.buffer
}

/**
 * bufferToBase64URL encodes browser credential bytes as unpadded URL-safe
 * base64. A null user handle remains null, allowing assertion serialization to
 * retain the browser's absence marker in the JSON response.
 */
function bufferToBase64URL(value: ArrayBuffer | null): string | null {
  if (value === null) return null
  const bytes = new Uint8Array(value)
  let binary = ''
  for (const byte of bytes) binary += String.fromCharCode(byte)
  /**
   * Encode binary bytes using the URL-safe alphabet and remove padding so the
   * finish payload matches the server WebAuthn JSON representation.
   */
  return btoa(binary).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '')
}

/**
 * creationOptionsFromJSON prepares server registration options for
 * navigator.credentials.create. It copies the outer options and user object,
 * converts the challenge and user ID, and copies each excluded credential
 * descriptor while decoding its ID. Those fields must remain binary when passed
 * to the browser even though the HTTP payload encodes them as strings. Other
 * option values retain their references, so this is a targeted conversion rather
 * than a deep copy.
 */
function creationOptionsFromJSON(
  value: Record<string, unknown>,
): PublicKeyCredentialCreationOptions {
  /**
   * Copy the option containers and decode challenge, user ID, and excluded
   * credential IDs together because browser registration requires binary fields
   * while the original server JSON must retain its encoded string values.
   */
  const options = { ...value } as Record<string, unknown>
  options.challenge = base64URLToBuffer(String(options.challenge))

  const user = { ...(options.user as Record<string, unknown>) }
  user.id = base64URLToBuffer(String(user.id))
  options.user = user

  if (Array.isArray(options.excludeCredentials)) {
    options.excludeCredentials = options.excludeCredentials.map((descriptor) => ({
      ...(descriptor as Record<string, unknown>),
      id: base64URLToBuffer(String((descriptor as Record<string, unknown>).id)),
    }))
  }
  return options as unknown as PublicKeyCredentialCreationOptions
}

/**
 * requestOptionsFromJSON prepares server login options for
 * navigator.credentials.get. It copies the outer options, decodes the challenge
 * and copies each permitted credential descriptor with a decoded ID when that
 * list exists. The copied descriptors keep their non-ID properties so transport
 * hints still reach the browser with the correct credential. Other nested option
 * values retain their references, and malformed encoded fields propagate
 * decoding errors to the ceremony caller.
 */
function requestOptionsFromJSON(value: Record<string, unknown>): PublicKeyCredentialRequestOptions {
  // Decode the challenge and permitted credential identifiers on copied containers
  // so browser login receives binary values without mutating the server's encoded options.
  const options = { ...value } as Record<string, unknown>
  options.challenge = base64URLToBuffer(String(options.challenge))
  if (Array.isArray(options.allowCredentials)) {
    options.allowCredentials = options.allowCredentials.map((descriptor) => ({
      ...(descriptor as Record<string, unknown>),
      id: base64URLToBuffer(String((descriptor as Record<string, unknown>).id)),
    }))
  }
  return options as unknown as PublicKeyCredentialRequestOptions
}

/**
 * serializeRegistrationCredential returns the JSON credential payload for
 * registration finish. Binary attestation fields use unpadded base64url, and
 * browsers without getTransports contribute an empty transport list while
 * retaining extension results.
 */
function serializeRegistrationCredential(credential: PublicKeyCredential): Record<string, unknown> {
  const response = credential.response as AuthenticatorAttestationResponse
  return {
    id: credential.id,
    rawId: bufferToBase64URL(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      attestationObject: bufferToBase64URL(response.attestationObject),
      clientDataJSON: bufferToBase64URL(response.clientDataJSON),
      transports: typeof response.getTransports === 'function' ? response.getTransports() : [],
    },
  }
}

/**
 * serializeAssertionCredential returns the JSON credential payload for login
 * finish. Authenticator data, client data and signature are encoded as unpadded
 * base64url; a missing user handle remains null in the response.
 */
function serializeAssertionCredential(credential: PublicKeyCredential): Record<string, unknown> {
  const response = credential.response as AuthenticatorAssertionResponse
  return {
    id: credential.id,
    rawId: bufferToBase64URL(credential.rawId),
    type: credential.type,
    authenticatorAttachment: credential.authenticatorAttachment,
    clientExtensionResults: credential.getClientExtensionResults(),
    response: {
      authenticatorData: bufferToBase64URL(response.authenticatorData),
      clientDataJSON: bufferToBase64URL(response.clientDataJSON),
      signature: bufferToBase64URL(response.signature),
      userHandle: bufferToBase64URL(response.userHandle),
    },
  }
}

/**
 * login performs a server challenge, browser assertion and matching finish
 * request. The optional captcha proof is sent with begin, whose returned session
 * token stays paired with the resulting credential through finish. Browser
 * rejection propagates, and a result that is not a PublicKeyCredential becomes a
 * cancellation error before finish. Successful authentication data is returned
 * to the caller, which owns adopting the resulting browser session.
 */
async function login(proof?: ActionCaptchaRequestProof): Promise<AuthResponse> {
  requirePasskeySupport()
  const { data: begin } = proof
    ? await apiClient.post<CeremonyOptionsResponse>('/auth/passkey/login/begin', proof)
    : await apiClient.post<CeremonyOptionsResponse>('/auth/passkey/login/begin')
  const credential = await navigator.credentials.get({
    publicKey: requestOptionsFromJSON(begin.options.publicKey),
  })
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error('Passkey sign-in was cancelled')
  }
  const { data } = await apiClient.post<AuthResponse>('/auth/passkey/login/finish', {
    session_token: begin.session_token,
    credential: serializeAssertionCredential(credential),
  })
  return data
}

/**
 * register performs a registration challenge and submits the browser's
 * attestation with the supplied credential name. The begin response's session
 * token is retained while navigator.credentials.create runs and accompanies that
 * credential in finish, preserving ceremony identity across both requests.
 * Browser errors propagate, and a non-PublicKeyCredential result rejects as
 * cancellation before finish. The returned credential summary is used by the
 * account-security view to refresh its list.
 */
async function register(name: string): Promise<PasskeyCredentialSummary> {
  requirePasskeySupport()
  const { data: begin } = await apiClient.post<CeremonyOptionsResponse>(
    '/user/passkeys/register/begin',
  )
  const credential = await navigator.credentials.create({
    publicKey: creationOptionsFromJSON(begin.options.publicKey),
  })
  if (!(credential instanceof PublicKeyCredential)) {
    throw new Error('Passkey creation was cancelled')
  }
  const { data } = await apiClient.post<PasskeyCredentialSummary>(
    '/user/passkeys/register/finish',
    {
      session_token: begin.session_token,
      name,
      credential: serializeRegistrationCredential(credential),
    },
  )
  return data
}

/**
 * list retrieves user passkeys and returns the server response data.
 */
async function list(): Promise<PasskeyCredentialSummary[]> {
  const { data } = await apiClient.get<PasskeyCredentialSummary[]>('/user/passkeys')
  return data
}

/**
 * rename updates rename and returns after the request completes.
 */
async function rename(id: number, name: string): Promise<void> {
  await apiClient.patch(`/user/passkeys/${id}`, { name })
}

/**
 * remove removes user passkeys and returns after the request completes.
 */
async function remove(id: number): Promise<void> {
  await apiClient.delete(`/user/passkeys/${id}`)
}

/**
 * passkeyAPI groups this module's HTTP operations for callers using the module's
 * named or default API object.
 */
export const passkeyAPI = {
  /**
   * isSupported reports whether browser credential and public-key APIs are
   * available.
   */
  isSupported: () => Boolean(window.PublicKeyCredential && navigator.credentials),
  login,
  register,
  list,
  rename,
  remove,
}
