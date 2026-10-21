import { apiClient, buildApiUrl } from './client'
import {
  prepareOAuthBindAccessTokenCookie,
  resolveWeChatOAuthStartStrict,
  type WeChatOAuthPublicSettings,
} from './auth'
import type { User, UserAuthProvider } from '@/api/identity.types'
import type {
  ChangePasswordRequest,
  UserAffiliateDetail,
  AffiliateTransferResponse,
} from '@/api/user.types'
import type { NotifyEmailEntry } from '@/api/notifications.types'

/**
 * user.ts sends profile, notification-address and identity-binding requests for
 * the signed-in user. OAuth binding URLs include the bind-current-user intent
 * and a return location, with WeChat mode selected from supplied capabilities or
 * the browser when capabilities are absent. Binding prepares the session cookie
 * before navigation.
 */

/**
 * getProfile retrieves current user profile and returns the server result.
 */
export async function getProfile(): Promise<User> {
  const { data } = await apiClient.get<User>('/user/profile')
  return data
}

/**
 * updateProfile updates current user profile and returns the server result.
 */
export async function updateProfile(profile: {
  username?: string // Account display username.
  avatar_url?: string | null // Recorded avatar url.
  balance_notify_enabled?: boolean // Balance notify enabled.
  balance_notify_threshold?: number | null // Balance notify threshold.
  balance_notify_extra_emails?: NotifyEmailEntry[] // Balance notify extra emails.
}): Promise<User> {
  const { data } = await apiClient.put<User>('/user', profile)
  return data
}

/**
 * changePassword updates change password and returns the server response data.
 */
export async function changePassword(
  oldPassword: string,
  newPassword: string,
): Promise<{
  message: string // Server-provided result message.
}> {
  const payload: ChangePasswordRequest = {
    old_password: oldPassword,
    new_password: newPassword,
  }

  const { data } = await apiClient.put<{
    message: string // Server-provided result message.
  }>('/user/password', payload)
  return data
}

/**
 * sendNotifyEmailCode sends verification code for adding a notify email.
 */
export async function sendNotifyEmailCode(email: string): Promise<void> {
  await apiClient.post('/user/notify-email/send-code', { email })
}

/**
 * verifyNotifyEmail submits notification-email verification and resolves without
 * returning response data.
 */
export async function verifyNotifyEmail(email: string, code: string): Promise<void> {
  await apiClient.post('/user/notify-email/verify', { email, code })
}

/**
 * removeNotifyEmail awaits notification-email removal without returning the response
 * body to its caller.
 */
export async function removeNotifyEmail(email: string): Promise<void> {
  await apiClient.delete('/user/notify-email', { data: { email } })
}

/**
 * toggleNotifyEmail sets a notify email's disabled state and returns the server
 * result.
 */
export async function toggleNotifyEmail(email: string, disabled: boolean): Promise<User> {
  const { data } = await apiClient.put<User>('/user/notify-email/toggle', { email, disabled })
  return data
}

/**
 * sendEmailBindingCode submits send email binding code and returns after the
 * request completes.
 */
export async function sendEmailBindingCode(email: string): Promise<void> {
  await apiClient.post('/user/provider-bindings/email/send-code', { email })
}

/**
 * bindEmailIdentity submits bind email identity and returns the server response
 * data.
 */
export async function bindEmailIdentity(payload: {
  email: string // Account email address.
  verify_code: string // Recorded verify code.
  password: string // Submitted password text.
}): Promise<User> {
  const { data } = await apiClient.post<User>('/user/provider-bindings/email', payload)
  return data
}

/**
 * BindableOAuthProvider excludes email identities from the providers accepted by
 * OAuth binding navigation.
 */
export type BindableOAuthProvider = Exclude<UserAuthProvider, 'email'>

/**
 * unbindAuthIdentity removes unbind auth identity and returns the server
 * response data.
 */
export async function unbindAuthIdentity(provider: BindableOAuthProvider): Promise<User> {
  const { data } = await apiClient.delete<User>(`/user/provider-bindings/${provider}`)
  return data
}

/**
 * BuildOAuthBindingStartURLOptions supplies the post-binding return path and
 * optional WeChat capability settings for navigation.
 */
interface BuildOAuthBindingStartURLOptions {
  redirectTo?: string // Recorded redirect to.
  wechatOAuthSettings?: WeChatOAuthPublicSettings | null // Wechat O Auth Settings.
}

/**
 * resolveWeChatOAuthMode selects the browser-compatible WeChat mode from the
 * user agent. A WeChat browser uses mp; other browsers and environments without
 * navigator use the open-platform entry mode.
 */
export function resolveWeChatOAuthMode(): 'open' | 'mp' {
  if (typeof navigator === 'undefined') {
    return 'open'
  }
  return /MicroMessenger/i.test(navigator.userAgent) ? 'mp' : 'open'
}

/**
 * resolveWeChatOAuthBindingMode selects a binding mode using strict capability
 * validation when settings are provided. Without settings it uses browser
 * detection, while unsupported or unknown explicit capabilities return null.
 */
function resolveWeChatOAuthBindingMode(
  settings?: WeChatOAuthPublicSettings | null,
): 'open' | 'mp' | null {
  if (settings) {
    return resolveWeChatOAuthStartStrict(settings).mode
  }
  return resolveWeChatOAuthMode()
}

/**
 * buildOAuthBindingStartURL creates a provider binding URL with a return path
 * and binding intent. Empty return paths use /profile; WeChat returns null when
 * its resolved capability does not provide a usable mode.
 */
export function buildOAuthBindingStartURL(
  provider: BindableOAuthProvider,
  options: BuildOAuthBindingStartURLOptions = {},
): string | null {
  const redirectTo = options.redirectTo?.trim() || '/profile'
  const params = new URLSearchParams({
    redirect: redirectTo,
    intent: 'bind_current_user',
  })

  if (provider === 'wechat') {
    const mode = resolveWeChatOAuthBindingMode(options.wechatOAuthSettings)
    if (!mode) {
      return null
    }
    params.set('mode', mode)
  }

  return `${buildApiUrl(`/auth/oauth/${provider}/bind/start`)}?${params.toString()}`
}

/**
 * startOAuthBinding prepares authentication for provider binding and navigates
 * to its start URL. It returns outside a browser or when mode selection yields
 * no URL, and awaits cookie preparation before changing location.
 */
export async function startOAuthBinding(
  provider: BindableOAuthProvider,
  options: BuildOAuthBindingStartURLOptions = {},
): Promise<void> {
  if (typeof window === 'undefined') {
    return
  }

  const startURL = buildOAuthBindingStartURL(provider, options)
  if (!startURL) {
    return
  }

  /**
   * Prepare the binding cookie before leaving this page so the destination can
   * associate the OAuth redirect with the current browser session.
   */
  await prepareOAuthBindAccessTokenCookie()
  window.location.href = startURL
}

/**
 * getAffiliateDetail retrieves affiliate detail and returns the server response
 * data.
 */
export async function getAffiliateDetail(): Promise<UserAffiliateDetail> {
  const { data } = await apiClient.get<UserAffiliateDetail>('/user/aff')
  return data
}

/**
 * transferAffiliateQuota submits transfer affiliate quota and returns the server
 * response data.
 */
export async function transferAffiliateQuota(): Promise<AffiliateTransferResponse> {
  const { data } = await apiClient.post<AffiliateTransferResponse>('/user/aff/transfer')
  return data
}

/**
 * userAPI groups user profiles, notification addresses and identity binding for
 * callers using the module's named or default API object.
 */
export const userAPI = {
  getProfile,
  updateProfile,
  changePassword,
  sendNotifyEmailCode,
  verifyNotifyEmail,
  removeNotifyEmail,
  toggleNotifyEmail,
  sendEmailBindingCode,
  bindEmailIdentity,
  unbindAuthIdentity,
  buildOAuthBindingStartURL,
  startOAuthBinding,
  getAffiliateDetail,
  transferAffiliateQuota,
}

export default userAPI
