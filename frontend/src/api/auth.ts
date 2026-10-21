import { apiClient } from './client'
import { refreshAuthTokens, type RefreshTokenResponse } from './tokenRefresh'
export type { RefreshTokenResponse } from './tokenRefresh'
import type {
  LoginRequest,
  RegisterRequest,
  AuthResponse,
  CurrentUserResponse,
  SendVerifyCodeRequest,
  SendVerifyCodeResponse,
  ActionCaptchaRequestProof,
} from '@/api/auth.types'
import type { PublicSettings } from '@/api/settings.types'
import type { TotpLoginResponse, TotpLogin2FARequest } from '@/api/totp.types'

/**
 * auth.ts sends login, registration and OAuth completion requests and exposes
 * browser-session storage helpers. Login responses can represent a completed
 * session or a TOTP challenge; only completed password login publishes
 * credentials and the serialized user. OAuth completion helpers keep
 * profile-adoption choices distinct from omitted decisions and return server
 * completion data for callers to adopt.
 *
 * WeChat entry selection combines advertised capabilities with the current
 * browser. Compatibility selection may derive web modes from the legacy flag,
 * while strict selection requires both web capability flags before choosing an
 * entry. Binding callers can therefore distinguish an unavailable capability
 * from a usable redirect. Token refresh delegates to the shared coordinator that
 * owns cross-tab publication and identity checks.
 */

/**
 * LoginResponse represents either completed authentication data or a challenge
 * requiring TOTP verification before session adoption.
 */
export type LoginResponse = AuthResponse | TotpLoginResponse

/**
 * OAuthLoginProvider enumerates providers accepted by the browser OAuth
 * login-start endpoints.
 */
export type OAuthLoginProvider = 'github' | 'google' | 'linuxdo' | 'dingtalk' | 'wechat' | 'oidc'

/**
 * OAuthLoginStart pairs the selected provider with query parameters retained
 * through authorization start.
 */
export interface OAuthLoginStart {
  provider: OAuthLoginProvider // Provider identity details.
  params: Record<string, string> // Authorization query parameters.
}

/**
 * OAuthLoginStartResponse returns the authorization URL selected by the
 * captcha-protected OAuth start endpoint.
 */
export interface OAuthLoginStartResponse {
  authorize_url: string // Recorded authorize url.
}

/**
 * GoogleOneTapRequest supplies a Google credential with optional return,
 * affiliate and promotion context.
 */
export interface GoogleOneTapRequest {
  credential: string // Provider credential proof.
  redirect?: string // Post-completion redirect path.
  aff_code?: string // Referral invitation code.
  promo_code?: string // Requested promo code.
}

/**
 * GoogleOneTapResponse distinguishes authenticated completion from a
 * registration-required redirect after credential exchange.
 */
export type GoogleOneTapResponse =
  | (OAuthTokenResponse & {
      status: 'authenticated' // Current server-reported status.
    })
  | {
      status: 'registration_required' // Current server-reported status.
      redirect: string // Post-completion redirect path.
    }

/**
 * buildOAuthLoginStartURL constructs an OAuth start URL using the configured API
 * base. It removes one trailing slash and encodes the supplied query parameters;
 * the default base applies when the environment value is absent.
 */
export function buildOAuthLoginStartURL(request: OAuthLoginStart): string {
  const apiBase = (import.meta.env.VITE_API_BASE_URL as string | undefined) || '/api/v1'
  const normalized = apiBase.replace(/\/$/, '')
  const query = new URLSearchParams(request.params).toString()
  const path = `${normalized}/auth/oauth/${request.provider}/start`
  return query ? `${path}?${query}` : path
}

/**
 * startOAuthLogin submits start o auth login and returns the server response
 * data.
 */
export async function startOAuthLogin(
  request: OAuthLoginStart,
  proof: ActionCaptchaRequestProof,
): Promise<OAuthLoginStartResponse> {
  const { data } = await apiClient.post<OAuthLoginStartResponse>(
    `/auth/oauth/${request.provider}/start`,
    proof,
    { params: request.params },
  )
  return data
}

/**
 * exchangeGoogleOneTap submits exchange google one tap and returns the server
 * response data.
 */
export async function exchangeGoogleOneTap(
  request: GoogleOneTapRequest,
): Promise<GoogleOneTapResponse> {
  const { data } = await apiClient.post<GoogleOneTapResponse>('/auth/oauth/google/one-tap', request)
  return data
}

/**
 * isTotp2FARequired reports whether a login response explicitly requests a TOTP
 * challenge.
 */
export function isTotp2FARequired(response: LoginResponse): response is TotpLoginResponse {
  return 'requires_2fa' in response && response.requires_2fa === true
}

/**
 * setAuthToken stores the access token used by subsequent shared-client
 * requests. Storage exceptions propagate to the caller, and publishing companion
 * refresh, expiry and user values remains the caller's responsibility.
 */
export function setAuthToken(token: string): void {
  localStorage.setItem('auth_token', token)
}

/**
 * setRefreshToken stores the refresh token used for later session rotation.
 * Storage exceptions propagate to the caller, which chooses its ordering
 * relative to access-token and expiry publication.
 */
export function setRefreshToken(token: string): void {
  localStorage.setItem('refresh_token', token)
}

/**
 * setTokenExpiresAt converts a relative lifetime in seconds to a millisecond
 * expiration timestamp and stores it. Storage exceptions propagate; callers pass
 * the server's expires_in value when adopting authentication data.
 */
export function setTokenExpiresAt(expiresIn: number): void {
  /**
   * Convert server lifetime seconds into the millisecond timestamp used by
   * browser expiry comparisons.
   */
  const expiresAt = Date.now() + expiresIn * 1000
  localStorage.setItem('token_expires_at', String(expiresAt))
}

/**
 * getAuthToken reads the access token used by the shared request interceptor.
 * Missing storage returns null, and localStorage access failures propagate to
 * the caller that is inspecting the browser session.
 */
export function getAuthToken(): string | null {
  return localStorage.getItem('auth_token')
}

/**
 * getRefreshToken reads the rotation credential from browser storage. A missing
 * entry returns null, letting logout skip server revocation and refresh callers
 * detect that session rotation is unavailable.
 */
export function getRefreshToken(): string | null {
  return localStorage.getItem('refresh_token')
}

/**
 * clearAuthToken removes access token, refresh token, serialized user and expiry
 * from localStorage. The removals occur in that order and storage failures
 * propagate, so this helper does not roll back partial cleanup.
 */
export function clearAuthToken(): void {
  localStorage.removeItem('auth_token')
  localStorage.removeItem('refresh_token')
  localStorage.removeItem('auth_user')
  localStorage.removeItem('token_expires_at')
}

/**
 * login submits credentials and returns either session data or a TOTP challenge.
 * A challenge leaves browser storage untouched so the second-factor screen can
 * complete login before adopting a session. Completed authentication writes
 * access token, optional refresh token, optional expiry and serialized user in
 * that order. These writes are sequential and storage failures propagate after
 * any preceding write, so callers cannot treat publication as one atomic storage
 * operation.
 */
export async function login(credentials: LoginRequest): Promise<LoginResponse> {
  const { data } = await apiClient.post<LoginResponse>('/auth/login', credentials)

  // A challenge response is not a completed login; publish credentials and user data only after the response passes the TOTP discriminator.
  if (!isTotp2FARequired(data)) {
    setAuthToken(data.access_token)
    if (data.refresh_token) {
      setRefreshToken(data.refresh_token)
    }
    if (data.expires_in) {
      setTokenExpiresAt(data.expires_in)
    }
    localStorage.setItem('auth_user', JSON.stringify(data.user))
  }

  return data
}

/**
 * login2FA submits the temporary challenge token and TOTP code, then adopts the
 * completed browser session. It writes access token first, then truthy refresh
 * and expiry values, followed by the serialized user before returning
 * authentication data. Missing companion values leave their existing storage
 * entries untouched. Storage exceptions propagate after any preceding write, so
 * this sequence provides ordered publication rather than rollback of a partially
 * written session.
 */
export async function login2FA(request: TotpLogin2FARequest): Promise<AuthResponse> {
  const { data } = await apiClient.post<AuthResponse>('/auth/login/2fa', request)

  // Publish the access token and available companion values before the user snapshot so subsequent browser reads can observe the completed authentication data.
  setAuthToken(data.access_token)
  if (data.refresh_token) {
    setRefreshToken(data.refresh_token)
  }
  if (data.expires_in) {
    setTokenExpiresAt(data.expires_in)
  }
  localStorage.setItem('auth_user', JSON.stringify(data.user))

  return data
}

/**
 * register submits account-registration data and adopts the session returned for
 * that account. It writes the access token before optional refresh and expiry
 * values, then stores the serialized user and returns the authentication
 * response. Missing companion values leave existing entries untouched, and a
 * storage exception interrupts the sequence after any earlier writes. Consumers
 * therefore receive a completed response only after these storage operations
 * finish successfully.
 */
export async function register(userData: RegisterRequest): Promise<AuthResponse> {
  const { data } = await apiClient.post<AuthResponse>('/auth/register', userData)

  // Publish the access token and available companion values before the user snapshot so subsequent browser reads can observe the completed authentication data.
  setAuthToken(data.access_token)
  if (data.refresh_token) {
    setRefreshToken(data.refresh_token)
  }
  if (data.expires_in) {
    setTokenExpiresAt(data.expires_in)
  }
  localStorage.setItem('auth_user', JSON.stringify(data.user))

  return data
}

/**
 * getCurrentUser retrieves the signed-in user through the shared authentication
 * endpoint. It returns the Axios response object, so callers read its data
 * property after the shared interceptor unwraps the API envelope.
 */
export async function getCurrentUser() {
  return apiClient.get<CurrentUserResponse>('/auth/me')
}

/**
 * logout attempts to revoke the stored refresh token before clearing local
 * authentication values. Revocation errors are ignored so local logout can still
 * complete; localStorage access errors continue to propagate.
 */
export async function logout(): Promise<void> {
  const refreshToken = getRefreshToken()

  // Try to revoke the refresh token on the server
  if (refreshToken) {
    try {
      await apiClient.post('/auth/logout', { refresh_token: refreshToken })
    } catch {
      // A failed server revocation should still allow this browser to log out, so continue to clear local session values.
    }
  }

  clearAuthToken()
}

/**
 * OAuthTokenResponse contains an access token and optional companion credentials
 * or lifetime from OAuth completion.
 */
export interface OAuthTokenResponse {
  access_token: string // Bearer access token.
  refresh_token?: string // Session rotation token.
  expires_in?: number // Lifetime in seconds.
  token_type?: string // Authorization scheme name.
}

/**
 * PendingOAuthBindLoginResponse carries intermediate OAuth completion state and
 * optional authentication data. Callers classify login by a nonempty access
 * token and use invitation or profile-suggestion fields to continue incomplete
 * flows.
 */
export interface PendingOAuthBindLoginResponse extends Partial<OAuthTokenResponse> {
  auth_result?: string // Recorded auth result.
  redirect?: string // Post-completion redirect path.
  error?: string // Operation failure detail.
  requires_2fa?: boolean // Recorded requires 2fa.
  temp_token?: string // Recorded temp token.
  user_email_masked?: string // User email masked.
  adoption_required?: boolean // Recorded adoption required.
  suggested_display_name?: string // Suggested display name.
  suggested_avatar_url?: string // Suggested avatar URL.
}

/**
 * PendingOAuthExchangeResponse names the pending completion response shared with
 * the OAuth bind-login exchange flow.
 */
export type PendingOAuthExchangeResponse = PendingOAuthBindLoginResponse

/**
 * PendingOAuthCreateAccountResponse adds optional completion metadata to
 * credentials returned after creating an OAuth account.
 */
export interface PendingOAuthCreateAccountResponse extends OAuthTokenResponse {
  auth_result?: string // Recorded auth result.
}

/**
 * PendingOAuthSendVerifyCodeResponse adds pending OAuth completion context to
 * email verification delivery feedback.
 */
export interface PendingOAuthSendVerifyCodeResponse extends SendVerifyCodeResponse {
  auth_result?: string // Recorded auth result.
  provider?: string // Provider identity details.
  redirect?: string // Post-completion redirect path.
}

/**
 * OAuthCompletionKind distinguishes a credential-bearing login result from a
 * binding-only completion result.
 */
export type OAuthCompletionKind = 'login' | 'bind'

/**
 * OAuthAdoptionDecision records independent choices about adopting suggested
 * profile fields. Undefined choices are omitted during serialization, while
 * explicit false is retained as a declined suggestion.
 */
export interface OAuthAdoptionDecision {
  /**
   * A boolean is serialized even when false; absence leaves the display-name
   * decision out of the OAuth completion request.
   */
  adoptDisplayName?: boolean // Display-name adoption decision.
  /**
   * A boolean is serialized even when false; absence leaves the avatar decision
   * out of the OAuth completion request.
   */
  adoptAvatar?: boolean // Avatar adoption decision.
}

/**
 * serializeOAuthAdoptionDecision maps profile-adoption flags to server field
 * names. Only boolean decisions are included, preserving false as an explicit
 * rejection while leaving missing choices absent for server completion handling.
 * For example, adoptAvatar set to false produces adopt_avatar: false, while
 * an absent adoptAvatar property produces no avatar field in the result.
 * Each decision is tested independently, so declining one suggestion does not
 * invent a choice for the other. A fresh payload leaves the caller's original
 * decision object unchanged and gives each endpoint the same presence information.
 */
function serializeOAuthAdoptionDecision(decision?: OAuthAdoptionDecision): Record<string, boolean> {
  const payload: Record<string, boolean> = {}

  /**
   * Serialize explicit booleans, including false, so declining suggested profile
   * data remains distinct from omitting a decision during OAuth completion.
   */
  if (typeof decision?.adoptDisplayName === 'boolean') {
    payload.adopt_display_name = decision.adoptDisplayName
  }
  if (typeof decision?.adoptAvatar === 'boolean') {
    payload.adopt_avatar = decision.adoptAvatar
  }

  return payload
}

/**
 * isOAuthLoginCompletion reports whether completion contains a nonempty string
 * access token.
 */
export function isOAuthLoginCompletion(
  completion: Partial<OAuthTokenResponse>,
): completion is OAuthTokenResponse {
  return typeof completion.access_token === 'string' && completion.access_token.trim().length > 0
}

/**
 * getOAuthCompletionKind classifies completion as login when it contains a
 * nonempty access token. Other responses are classified as bind, allowing OAuth
 * callers to distinguish session adoption from identity-binding completion.
 */
export function getOAuthCompletionKind(
  completion: Partial<OAuthTokenResponse>,
): OAuthCompletionKind {
  return isOAuthLoginCompletion(completion) ? 'login' : 'bind'
}

/**
 * getPendingOAuthBindLoginKind classifies pending binding completion through the
 * shared OAuth completion helper.
 */
export function getPendingOAuthBindLoginKind(
  completion: PendingOAuthBindLoginResponse,
): OAuthCompletionKind {
  return getOAuthCompletionKind(completion)
}

/**
 * isPendingOAuthCreateAccountRequired reports whether completion requires an
 * invitation to create an account.
 */
export function isPendingOAuthCreateAccountRequired(
  completion: Pick<PendingOAuthBindLoginResponse, 'error'>,
): boolean {
  return completion.error === 'invitation_required'
}

/**
 * hasPendingOAuthSuggestedProfile reports whether completion supplies a truthy
 * display-name or avatar suggestion.
 */
export function hasPendingOAuthSuggestedProfile(
  completion: Pick<
    PendingOAuthBindLoginResponse,
    'suggested_display_name' | 'suggested_avatar_url'
  >,
): boolean {
  return Boolean(completion.suggested_display_name || completion.suggested_avatar_url)
}

/**
 * persistOAuthTokenContext stores truthy refresh-token and expiry values from an
 * OAuth completion. It leaves access-token and user adoption to the caller, and
 * missing companion values leave existing storage entries unchanged.
 */
export function persistOAuthTokenContext(tokens: Partial<OAuthTokenResponse>): void {
  if (tokens.refresh_token) {
    setRefreshToken(tokens.refresh_token)
  }
  if (tokens.expires_in) {
    setTokenExpiresAt(tokens.expires_in)
  }
}

/**
 * prepareOAuthBindAccessTokenCookie asks the server to establish the binding
 * cookie when an access token is stored. An anonymous browser returns
 * immediately; callers await completion before navigating to a binding URL.
 */
export async function prepareOAuthBindAccessTokenCookie(): Promise<void> {
  if (!getAuthToken()) {
    return
  }
  await apiClient.post('/auth/oauth/bind-token')
}

/**
 * refreshToken delegates browser session rotation to the shared refresh
 * coordinator and returns the resulting access and refresh tokens.
 */
export async function refreshToken(): Promise<RefreshTokenResponse> {
  return refreshAuthTokens()
}

/**
 * getPublicSettings retrieves public settings (no auth required) and returns the
 * server result.
 */
export async function getPublicSettings(): Promise<PublicSettings> {
  const { data } = await apiClient.get<PublicSettings>('/settings/public')
  return data
}

/**
 * WeChatOAuthMode selects open-platform or public-account mode for a browser
 * OAuth entry.
 */
export type WeChatOAuthMode = 'open' | 'mp'

/**
 * WeChatOAuthUnavailableReason identifies absent configuration, unknown
 * capability or the browser environment needed to start authorization.
 */
export type WeChatOAuthUnavailableReason =
  | 'not_configured'
  | 'capability_unknown'
  | 'external_browser_required'
  | 'wechat_browser_required'
  | 'native_app_required'

/**
 * ResolvedWeChatOAuthStart records selected mode, advertised capabilities and
 * browser classification. A null mode pairs with unavailableReason so callers
 * can explain why navigation cannot start.
 */
export interface ResolvedWeChatOAuthStart {
  /**
   * Null means the caller cannot begin a selected web flow; unavailableReason
   * supplies the browser or capability explanation.
   */
  mode: WeChatOAuthMode | null // Selected operating mode.
  openEnabled: boolean // Recorded open enabled.
  mpEnabled: boolean // Recorded mp enabled.
  /**
   * Mobile capability is reported separately from the chosen web mode and does
   * not itself permit open or mp browser navigation.
   */
  mobileEnabled: boolean // Recorded mobile enabled.
  isWeChatBrowser: boolean // Is We Chat Browser.
  unavailableReason: WeChatOAuthUnavailableReason | null // Recorded unavailable reason.
}

/**
 * WeChatOAuthPublicSettings carries optional legacy and per-mode capability
 * flags read by browser OAuth selectors.
 */
export type WeChatOAuthPublicSettings = {
  wechat_oauth_enabled?: boolean // Wechat OAuth enabled.
  wechat_oauth_open_enabled?: boolean // Wechat OAuth open enabled.
  wechat_oauth_mp_enabled?: boolean // Wechat OAuth mp enabled.
  wechat_oauth_mobile_enabled?: boolean // Wechat OAuth mobile enabled.
}

/**
 * isWeChatWebOAuthEnabled reports whether public settings advertise a usable web
 * OAuth capability. If neither web capability is boolean it uses the legacy
 * enabled flag; otherwise either explicit true web flag enables entry.
 */
export function isWeChatWebOAuthEnabled(
  settings: WeChatOAuthPublicSettings | null | undefined,
): boolean {
  const legacyEnabled = settings?.wechat_oauth_enabled ?? false
  const hasExplicitCapabilities =
    typeof settings?.wechat_oauth_open_enabled === 'boolean' ||
    typeof settings?.wechat_oauth_mp_enabled === 'boolean'

  /**
   * Older public settings expose one enabled flag; use it only when neither
   * explicit web capability is present to preserve disabled mode choices.
   */
  if (!hasExplicitCapabilities) {
    return legacyEnabled
  }

  return settings?.wechat_oauth_open_enabled === true || settings?.wechat_oauth_mp_enabled === true
}

/**
 * hasExplicitWeChatOAuthCapabilities reports whether both browser OAuth
 * capability flags are booleans in the supplied settings.
 */
export function hasExplicitWeChatOAuthCapabilities(
  settings: WeChatOAuthPublicSettings | null | undefined,
): settings is WeChatOAuthPublicSettings & {
  wechat_oauth_open_enabled: boolean // Wechat OAuth open enabled.
  wechat_oauth_mp_enabled: boolean // Wechat OAuth mp enabled.
} {
  return (
    typeof settings?.wechat_oauth_open_enabled === 'boolean' &&
    typeof settings?.wechat_oauth_mp_enabled === 'boolean'
  )
}

/**
 * resolveWeChatOAuthStart chooses a WeChat web entry from capability settings
 * and the browser user agent. Missing web flags inherit the legacy enabled flag,
 * while mobile support defaults false and remains reported separately. A WeChat
 * browser selects mp when enabled; an external browser selects open when
 * enabled. A capability available only in the other browser yields a null mode
 * with a reason that the caller can display before redirecting.
 */
export function resolveWeChatOAuthStart(
  settings: WeChatOAuthPublicSettings | null | undefined,
  userAgent?: string,
): ResolvedWeChatOAuthStart {
  const normalizedUserAgent = (
    userAgent ??
    (typeof navigator !== 'undefined' ? navigator.userAgent : '') ??
    ''
  ).trim()
  const isWeChatBrowser = /MicroMessenger/i.test(normalizedUserAgent)
  const legacyEnabled = settings?.wechat_oauth_enabled ?? false
  const openEnabled =
    typeof settings?.wechat_oauth_open_enabled === 'boolean'
      ? settings.wechat_oauth_open_enabled
      : legacyEnabled
  const mpEnabled =
    typeof settings?.wechat_oauth_mp_enabled === 'boolean'
      ? settings.wechat_oauth_mp_enabled
      : legacyEnabled
  const mobileEnabled =
    typeof settings?.wechat_oauth_mobile_enabled === 'boolean'
      ? settings.wechat_oauth_mobile_enabled
      : false

  if (isWeChatBrowser) {
    if (mpEnabled) {
      return {
        mode: 'mp',
        openEnabled,
        mpEnabled,
        mobileEnabled,
        isWeChatBrowser,
        unavailableReason: null,
      }
    }
    if (openEnabled) {
      return {
        mode: null,
        openEnabled,
        mpEnabled,
        mobileEnabled,
        isWeChatBrowser,
        unavailableReason: 'external_browser_required',
      }
    }
    return {
      mode: null,
      openEnabled,
      mpEnabled,
      mobileEnabled,
      isWeChatBrowser,
      unavailableReason: 'not_configured',
    }
  }

  if (openEnabled) {
    return {
      mode: 'open',
      openEnabled,
      mpEnabled,
      mobileEnabled,
      isWeChatBrowser,
      unavailableReason: null,
    }
  }
  if (mpEnabled) {
    return {
      mode: null,
      openEnabled,
      mpEnabled,
      mobileEnabled,
      isWeChatBrowser,
      unavailableReason: 'wechat_browser_required',
    }
  }
  return {
    mode: null,
    openEnabled,
    mpEnabled,
    mobileEnabled,
    isWeChatBrowser,
    unavailableReason: 'not_configured',
  }
}

/**
 * resolveWeChatOAuthStartStrict requires explicit boolean capability flags
 * before selecting a WeChat web entry. When either web flag is unknown, it
 * returns capability_unknown with a null mode, preventing a binding caller from
 * inferring support from the legacy flag alone. With complete capability
 * information it delegates to the browser-aware selector, retaining its browser
 * mismatch reasons. The optional user agent provides a deterministic browser
 * classification for callers and tests.
 */
export function resolveWeChatOAuthStartStrict(
  settings: WeChatOAuthPublicSettings | null | undefined,
  userAgent?: string,
): ResolvedWeChatOAuthStart {
  const normalizedUserAgent = (
    userAgent ??
    (typeof navigator !== 'undefined' ? navigator.userAgent : '') ??
    ''
  ).trim()
  const isWeChatBrowser = /MicroMessenger/i.test(normalizedUserAgent)

  /**
   * Strict callers need both web capability flags before navigation, because the
   * legacy enabled flag alone cannot identify a supported browser entry.
   */
  if (!hasExplicitWeChatOAuthCapabilities(settings)) {
    return {
      mode: null,
      openEnabled: false,
      mpEnabled: false,
      mobileEnabled: false,
      isWeChatBrowser,
      unavailableReason: 'capability_unknown',
    }
  }

  return resolveWeChatOAuthStart(settings, normalizedUserAgent)
}

/**
 * sendVerifyCode sends verification code to email and returns the server result.
 */
export async function sendVerifyCode(
  request: SendVerifyCodeRequest,
): Promise<SendVerifyCodeResponse> {
  const { data } = await apiClient.post<SendVerifyCodeResponse>('/auth/send-verify-code', request)
  return data
}

/**
 * sendPendingOAuthVerifyCode submits send pending o auth verify code and returns
 * the server response data.
 */
export async function sendPendingOAuthVerifyCode(
  request: SendVerifyCodeRequest,
): Promise<PendingOAuthSendVerifyCodeResponse> {
  const { data } = await apiClient.post<PendingOAuthSendVerifyCodeResponse>(
    '/auth/oauth/pending/send-verify-code',
    request,
  )
  return data
}

/**
 * ValidatePromoCodeResponse reports promotion-code validity with any bonus
 * amount or explanatory failure metadata.
 */
export interface ValidatePromoCodeResponse {
  valid: boolean // Input validity flag.
  bonus_amount?: number // Recorded bonus amount.
  error_code?: string // Recorded error code.
  message?: string // Server-provided result message.
}

/**
 * validatePromoCode validates promo code (public endpoint, no auth required).
 */
export async function validatePromoCode(code: string): Promise<ValidatePromoCodeResponse> {
  const { data } = await apiClient.post<ValidatePromoCodeResponse>('/auth/validate-promo-code', {
    code,
  })
  return data
}

/**
 * ValidateInvitationCodeResponse reports invitation-code validity and an
 * optional code identifying the failure.
 */
export interface ValidateInvitationCodeResponse {
  valid: boolean // Input validity flag.
  error_code?: string // Recorded error code.
}

/**
 * validateInvitationCode validates invitation code (public endpoint, no auth
 * required).
 */
export async function validateInvitationCode(
  code: string,
): Promise<ValidateInvitationCodeResponse> {
  const { data } = await apiClient.post<ValidateInvitationCodeResponse>(
    '/auth/validate-invitation-code',
    { code },
  )
  return data
}

/**
 * ForgotPasswordRequest supplies the account email and optional captcha proof
 * for password-reset delivery.
 */
export interface ForgotPasswordRequest {
  email: string // Account email address.
  turnstile_token?: string // Requested turnstile token.
  tencent_captcha_ticket?: string // Tencent captcha ticket.
  tencent_captcha_randstr?: string // Tencent captcha randstr.
}

/**
 * ForgotPasswordResponse contains the server message returned after requesting
 * password-reset delivery.
 */
export interface ForgotPasswordResponse {
  message: string // Server-provided result message.
}

/**
 * forgotPassword requests password reset link and returns the server result.
 */
export async function forgotPassword(
  request: ForgotPasswordRequest,
): Promise<ForgotPasswordResponse> {
  const { data } = await apiClient.post<ForgotPasswordResponse>('/auth/forgot-password', request)
  return data
}

/**
 * ResetPasswordRequest supplies the account email, reset token and replacement
 * password for reset completion.
 */
export interface ResetPasswordRequest {
  email: string // Account email address.
  token: string // Opaque operation token.
  new_password: string // Requested new password.
}

/**
 * ResetPasswordResponse contains the server message returned after
 * password-reset completion.
 */
export interface ResetPasswordResponse {
  message: string // Server-provided result message.
}

/**
 * resetPassword resets password with token and returns the server result.
 */
export async function resetPassword(request: ResetPasswordRequest): Promise<ResetPasswordResponse> {
  const { data } = await apiClient.post<ResetPasswordResponse>('/auth/reset-password', request)
  return data
}

/**
 * completeLinuxDoOAuthRegistration completes LinuxDo OAuth registration by
 * supplying an invitation code.
 */
export async function completeLinuxDoOAuthRegistration(
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<OAuthTokenResponse> {
  return createPendingLinuxDoOAuthAccount(invitationCode, decision, affiliateCode)
}

/**
 * completeOIDCOAuthRegistration completes OIDC OAuth registration by supplying
 * an invitation code.
 */
export async function completeOIDCOAuthRegistration(
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<OAuthTokenResponse> {
  return createPendingOIDCOAuthAccount(invitationCode, decision, affiliateCode)
}

/**
 * completeWeChatOAuthRegistration delegates complete we chat o auth registration
 * to createPendingWeChatOAuthAccount and returns its result.
 */
export async function completeWeChatOAuthRegistration(
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<OAuthTokenResponse> {
  return createPendingWeChatOAuthAccount(invitationCode, decision, affiliateCode)
}

/**
 * createPendingOAuthAccount completes registration for the selected pending
 * OAuth provider. It sends the invitation code and explicit adoption decisions,
 * including an affiliate code only when trimming leaves a nonempty value.
 */
async function createPendingOAuthAccount(
  provider: 'linuxdo' | 'oidc' | 'wechat' | 'dingtalk',
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<PendingOAuthCreateAccountResponse> {
  const { data } = await apiClient.post<PendingOAuthCreateAccountResponse>(
    `/auth/oauth/${provider}/complete-registration`,
    {
      invitation_code: invitationCode,
      ...(affiliateCode?.trim() ? { aff_code: affiliateCode.trim() } : {}),
      ...serializeOAuthAdoptionDecision(decision),
    },
  )
  return data
}

/**
 * createPendingLinuxDoOAuthAccount delegates pending linux do o auth account to
 * createPendingOAuthAccount and returns its result.
 */
export async function createPendingLinuxDoOAuthAccount(
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<PendingOAuthCreateAccountResponse> {
  return createPendingOAuthAccount('linuxdo', invitationCode, decision, affiliateCode)
}

/**
 * createPendingOIDCOAuthAccount delegates pending oidco auth account to
 * createPendingOAuthAccount and returns its result.
 */
export async function createPendingOIDCOAuthAccount(
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<PendingOAuthCreateAccountResponse> {
  return createPendingOAuthAccount('oidc', invitationCode, decision, affiliateCode)
}

/**
 * createPendingWeChatOAuthAccount delegates pending we chat o auth account to
 * createPendingOAuthAccount and returns its result.
 */
export async function createPendingWeChatOAuthAccount(
  invitationCode: string,
  decision?: OAuthAdoptionDecision,
  affiliateCode?: string,
): Promise<PendingOAuthCreateAccountResponse> {
  return createPendingOAuthAccount('wechat', invitationCode, decision, affiliateCode)
}

/**
 * completePendingOAuthBindLogin submits complete pending o auth bind login and
 * returns the server response data.
 */
export async function completePendingOAuthBindLogin(
  decision?: OAuthAdoptionDecision,
): Promise<PendingOAuthBindLoginResponse> {
  const { data } = await apiClient.post<PendingOAuthBindLoginResponse>(
    '/auth/oauth/pending/exchange',
    serializeOAuthAdoptionDecision(decision),
  )
  return data
}

/**
 * exchangePendingOAuthCompletion delegates exchange pending o auth completion to
 * completePendingOAuthBindLogin and returns its result.
 */
export async function exchangePendingOAuthCompletion(
  decision?: OAuthAdoptionDecision,
): Promise<PendingOAuthExchangeResponse> {
  return completePendingOAuthBindLogin(decision)
}

/**
 * authAPI groups this module's HTTP operations for callers using the module's
 * named or default API object.
 */
export const authAPI = {
  login,
  login2FA,
  isTotp2FARequired,
  register,
  getCurrentUser,
  logout,
  setAuthToken,
  setRefreshToken,
  setTokenExpiresAt,
  getAuthToken,
  getRefreshToken,
  clearAuthToken,
  getPublicSettings,
  sendVerifyCode,
  sendPendingOAuthVerifyCode,
  validatePromoCode,
  validateInvitationCode,
  forgotPassword,
  resetPassword,
  refreshToken,
  getPendingOAuthBindLoginKind,
  isPendingOAuthCreateAccountRequired,
  hasPendingOAuthSuggestedProfile,
  completePendingOAuthBindLogin,
  createPendingLinuxDoOAuthAccount,
  createPendingOIDCOAuthAccount,
  createPendingWeChatOAuthAccount,
  exchangePendingOAuthCompletion,
  completeLinuxDoOAuthRegistration,
  completeOIDCOAuthRegistration,
  completeWeChatOAuthRegistration,
}

export default authAPI
