import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { GrokTokenInfo } from '@/api/admin/grok'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'

// useGrokOAuth.ts supports Grok code exchange, refresh validation, SSO
// authentication, and password authorization. Each flow exposes request
// status and translated failures, and credential construction selects token
// and account fields suitable for saving a provider.

// useGrokOAuth exposes reactive authorization fields and request helpers for
// the provider form. Call it during component setup with an active
// application store and i18n context. The caller saves returned token
// information.
export function useGrokOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  // Authorization refs belong to this form instance. Request helpers update
  // loading and error for the form, and URL generation publishes the session
  // fields used for exchange.
  const authUrl = ref('') // Displayed authorization URL.
  const sessionId = ref('') // Current backend session.
  const state = ref('') // Current callback state.
  const loading = ref(false) // Request activity indicator.
  const error = ref('') // Current form feedback.

  // resetState clears authorization fields and the current request feedback.
  // Callers use it when resetting the form; pending HTTP requests continue
  // and may subsequently publish their results.
  const resetState = () => {
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    loading.value = false
    error.value = ''
  }

  // generateAuthUrl requests a fresh authorization URL and replaces the
  // displayed session. A truthy proxy identifier is forwarded; request
  // failures update local feedback, show an error toast, and return false.
  const generateAuthUrl = async (proxyId: number | null | undefined): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    error.value = ''

    try {
      const payload: Record<string, unknown> = {}
      if (proxyId) payload.proxy_id = proxyId

      const response = await adminAPI.grok.generateAuthUrl(payload)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      state.value = response.state
      return true
    } catch (err: any) {
      // URL-generation errors may use different API response shapes. The
      // shared extractor selects their message before the localized
      // generation text is used.
      error.value = extractApiErrorMessage(err, t('admin.providers.oauth.grok.failedToGenerateUrl'))
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  // exchangeAuthCode submits the supplied code, session identifier, and state
  // for tokens. Missing required input returns null with local feedback;
  // request failures also show a toast, and completion clears loading.
  const exchangeAuthCode = async (params: {
    code: string // Pasted authorization code.
    sessionId: string // Backend session identifier.
    state: string // Callback correlation state.
    proxyId?: number | null // Selected proxy identifier.
  }): Promise<GrokTokenInfo | null> => {
    const code = params.code?.trim()
    if (!code || !params.sessionId || !params.state) {
      error.value = t('admin.providers.oauth.grok.missingExchangeParams')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = {
        session_id: params.sessionId,
        state: params.state,
        code,
      }
      if (params.proxyId) payload.proxy_id = params.proxyId

      return await adminAPI.grok.exchangeCode(payload as any)
    } catch (err: any) {
      // Structured Grok error reasons select recovery translations under the
      // OAuth error namespace. The exchange message covers failures without
      // a matching translation.
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.providers.oauth.grok.errors',
        t('admin.providers.oauth.grok.failedToExchangeCode'),
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // validateRefreshToken exchanges a trimmed refresh token for token
  // information. Blank input returns null with local feedback; request
  // failures return null and leave the failure message available to the
  // caller.
  const validateRefreshToken = async (
    refreshToken: string,
    proxyId?: number | null,
  ): Promise<GrokTokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = t('admin.providers.oauth.grok.pleaseEnterRefreshToken')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      return await adminAPI.grok.refreshGrokToken(refreshToken.trim(), proxyId)
    } catch (err: any) {
      // Refresh failures use the same structured Grok translations as
      // exchange. Keep this feedback local so batch validation can report
      // its own results.
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.providers.oauth.grok.errors',
        t('admin.providers.oauth.grok.failedToValidateRT'),
      )
      return null
    } finally {
      loading.value = false
    }
  }

  // buildCredentials selects Grok token and account fields for provider
  // persistence. Empty strings and undefined values are removed, optional
  // refresh and ID tokens require truthy values, and one-time authentication
  // inputs are excluded.
  const buildCredentials = (tokenInfo: GrokTokenInfo): Record<string, unknown> => {
    const credentials: Record<string, unknown> = {
      access_token: tokenInfo.access_token,
      token_type: tokenInfo.token_type,
      expires_at: tokenInfo.expires_at,
      client_id: tokenInfo.client_id,
      scope: tokenInfo.scope,
      email: tokenInfo.email,
      sub: tokenInfo.sub,
      team_id: tokenInfo.team_id,
      subscription_tier: tokenInfo.subscription_tier,
      entitlement_status: tokenInfo.entitlement_status,
    }
    if (tokenInfo.refresh_token) credentials.refresh_token = tokenInfo.refresh_token
    if (tokenInfo.id_token) credentials.id_token = tokenInfo.id_token
    const blocked = new Set(['sso_token', 'password', 'sso', 'sso-rw'])
    return Object.fromEntries(
      Object.entries(credentials).filter(
        ([key, value]) => !blocked.has(key) && value !== undefined && value !== '',
      ),
    )
  }

  // buildExtraInfo copies available Grok account labels into a fresh metadata
  // record. Fields require truthy response values, and a response with no
  // labels produces an empty object for the caller.
  const buildExtraInfo = (tokenInfo: GrokTokenInfo): Record<string, unknown> => {
    const extra: Record<string, unknown> = {}
    if (tokenInfo.email) extra.email = tokenInfo.email
    if (tokenInfo.subscription_tier) extra.subscription_tier = tokenInfo.subscription_tier
    if (tokenInfo.entitlement_status) extra.entitlement_status = tokenInfo.entitlement_status
    return extra
  }

  // validateSSOToken exchanges a trimmed SSO cookie through the dedicated
  // authentication API. Blank input returns null with local feedback; request
  // failures also show a translated error toast before returning null.
  const validateSSOToken = async (
    ssoToken: string,
    proxyId?: number | null,
  ): Promise<GrokTokenInfo | null> => {
    if (!ssoToken.trim()) {
      error.value = t('admin.providers.oauth.grok.pleaseEnterSSOToken', 'Please enter an SSO token')
      return null
    }
    loading.value = true
    error.value = ''
    try {
      return await adminAPI.grok.validateSSOToken(ssoToken.trim(), proxyId)
    } catch (err: any) {
      // SSO authentication errors use structured reason translations when
      // available. Publish the selected message through both the form and
      // application notification.
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.providers.oauth.grok.errors',
        t('admin.providers.oauth.grok.failedToValidateSSO', 'Failed to validate SSO token'),
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // authorizePassword submits the supplied email-and-password input to Grok
  // authentication. Whitespace-only input is rejected locally; other input is
  // sent as supplied, and request failures show a toast and return null.
  const authorizePassword = async (
    emailAndPassword: string,
    proxyId?: number | null,
  ): Promise<GrokTokenInfo | null> => {
    if (!emailAndPassword.trim()) {
      error.value = t(
        'admin.providers.oauth.grok.pleaseEnterPassword',
        'Please enter email----password',
      )
      return null
    }
    loading.value = true
    error.value = ''
    try {
      return await adminAPI.grok.authorizePassword(emailAndPassword, proxyId)
    } catch (err: any) {
      // Password authentication errors use the Grok reason namespace for
      // recovery text. Publish the selected message so the administrator can
      // correct the submitted credentials.
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.providers.oauth.grok.errors',
        t('admin.providers.oauth.grok.failedToAuthorizePassword', 'Password authorization failed'),
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  return {
    authUrl,
    sessionId,
    state,
    loading,
    error,
    resetState,
    generateAuthUrl,
    exchangeAuthCode,
    validateRefreshToken,
    validateSSOToken,
    authorizePassword,
    buildCredentials,
    buildExtraInfo,
  }
}
