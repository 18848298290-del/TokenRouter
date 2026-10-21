import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { extractApiErrorMessage, extractI18nErrorMessage } from '@/utils/apiError'

// useOpenAIOAuth.ts manages OpenAI authorization sessions and converts token
// responses into provider records. Batch authorization can retain several
// URLs at once, so each stored entry keeps its backend session identifier
// beside the state extracted from that URL. The current fields mirror the
// most recently selected entry for forms that display one URL.
//
// Removing the current session selects the last remaining entry, or clears
// the current fields when the list is empty. Token exchange takes the
// caller's chosen session and state, which allows batch callbacks to retain
// their own identity. Request methods share loading and error refs and
// callers control invocation order.

// OpenAITokenInfo represents token and account metadata returned by OpenAI
// authorization.
export interface OpenAITokenInfo {
  access_token?: string // API access credential.
  refresh_token?: string // Token refresh credential.
  client_id?: string // Issuing OAuth client.
  id_token?: string // Encoded identity token.
  token_type?: string // Access token scheme.
  expires_in?: number // Lifetime in seconds.
  expires_at?: number // Unix seconds.
  scope?: string // Granted permission scopes.
  email?: string // Account email address.
  name?: string // Account display name.
  plan_type?: string // Account subscription plan.
  subscription_expires_at?: string // Subscription expiration text.
  privacy_mode?: string // Account privacy setting.
  chatgpt_account_id?: string // ChatGPT account identifier.
  chatgpt_user_id?: string // ChatGPT user identifier.
  organization_id?: string // Account organization identifier.
  [key: string]: unknown // Additional response fields.
}

// OpenAIOAuthSession pairs an authorization URL with its backend session
// identity.
export interface OpenAIOAuthSession {
  authUrl: string // Browser authorization URL.
  sessionId: string // Backend session identifier.
  state: string // Callback correlation state.
}

// useOpenAIOAuth exposes OpenAI session state and token helpers for a
// provider form. Call it during component setup with the application store
// and i18n context available. Batch callers retain each generated session
// and match a callback to that entry before exchange. Current display fields
// follow the selected entry, while the session collection retains other
// pending entries. Request helpers share activity and error refs, so callers
// control overlapping invocations.
export function useOpenAIOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()
  const endpointPrefix = '/admin/openai'
  // authUrl, sessionId, and oauthState describe the displayed entry in
  // authSessions. Batch callers retain the list to match callbacks, while
  // loading and error describe shared request feedback.
  const authUrl = ref('') // Displayed authorization URL.
  const sessionId = ref('') // Current backend session.
  const oauthState = ref('') // Displayed callback state.
  const authSessions = ref<OpenAIOAuthSession[]>([]) // Retained authorization sessions.
  const loading = ref(false) // Request activity indicator.
  const error = ref('') // Current form feedback.

  // extractStateFromAuthUrl reads the state query parameter from an absolute
  // authorization URL. A missing parameter or invalid URL returns an empty
  // string for the session record used by the form.
  const extractStateFromAuthUrl = (urlValue: string): string => {
    try {
      const parsed = new URL(urlValue)
      return parsed.searchParams.get('state') || ''
    } catch {
      // Malformed authorization URLs have no usable state parameter. Store
      // the empty sentinel so exchange validation can report missing input.
      return ''
    }
  }

  // setCurrentAuthSession copies one session into the displayed authorization
  // fields. Passing null clears those fields; callers maintain the
  // authSessions collection separately before choosing the entry to display.
  const setCurrentAuthSession = (session: OpenAIOAuthSession | null) => {
    authUrl.value = session?.authUrl || ''
    sessionId.value = session?.sessionId || ''
    oauthState.value = session?.state || ''
  }

  // requestAuthSession requests one OpenAI authorization session with
  // optional proxy and redirect settings. It derives state from the returned
  // URL and propagates API errors to the calling operation for feedback.
  const requestAuthSession = async (
    proxyId?: number | null,
    redirectUri?: string,
  ): Promise<OpenAIOAuthSession | null> => {
    const payload: Record<string, unknown> = {}
    if (proxyId) {
      payload.proxy_id = proxyId
    }
    if (redirectUri) {
      payload.redirect_uri = redirectUri
    }

    const response = await adminAPI.providers.generateAuthUrl(
      `${endpointPrefix}/generate-auth-url`,
      payload,
    )
    return {
      authUrl: response.auth_url,
      sessionId: response.session_id,
      state: extractStateFromAuthUrl(response.auth_url),
    }
  }

  // resetState clears authorization fields and the current request feedback.
  // Callers use it when resetting the form; pending HTTP requests continue
  // and may subsequently publish their results.
  const resetState = () => {
    authUrl.value = ''
    sessionId.value = ''
    oauthState.value = ''
    authSessions.value = []
    loading.value = false
    error.value = ''
  }

  // generateAuthUrl requests a fresh authorization URL and replaces the
  // displayed session. A truthy proxy identifier is forwarded; request
  // failures update local feedback, show an error toast, and return false.
  const generateAuthUrl = async (
    proxyId?: number | null,
    redirectUri?: string,
  ): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    oauthState.value = ''
    authSessions.value = []
    error.value = ''

    try {
      const session = await requestAuthSession(proxyId, redirectUri)
      if (!session) return false
      authSessions.value = [session]
      setCurrentAuthSession(session)
      return true
    } catch (err: any) {
      // The API error extractor handles generation failures with varying
      // response shapes. A localized generation message supplies feedback
      // when the error has no usable text.
      error.value = extractApiErrorMessage(
        err,
        t('admin.providers.oauth.openai.failedToGenerateUrl'),
      )
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  // appendAuthUrl appends a generated session and makes it the displayed
  // session. Existing entries survive generation failures; failures show an
  // error toast and return null for the batch caller.
  const appendAuthUrl = async (
    proxyId?: number | null,
    redirectUri?: string,
  ): Promise<OpenAIOAuthSession | null> => {
    loading.value = true
    error.value = ''

    try {
      const session = await requestAuthSession(proxyId, redirectUri)
      if (!session) return null
      authSessions.value = [...authSessions.value, session]
      setCurrentAuthSession(session)
      return session
    } catch (err: any) {
      // Failed URL creation leaves the accumulated batch sessions available.
      // The error extractor chooses feedback for the failed addition before
      // the operation returns null.
      error.value = extractApiErrorMessage(
        err,
        t('admin.providers.oauth.openai.failedToGenerateUrl'),
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // removeAuthSession removes entries matching the supplied backend session
  // identifier. When that identifier is currently displayed, it selects the
  // last remaining entry or clears the display if the list is empty.
  const removeAuthSession = (targetSessionId: string) => {
    authSessions.value = authSessions.value.filter(
      (session) => session.sessionId !== targetSessionId,
    )
    if (sessionId.value === targetSessionId) {
      setCurrentAuthSession(authSessions.value[authSessions.value.length - 1] || null)
    }
  }

  // exchangeAuthCode submits one selected OpenAI session with trimmed code
  // and state. Missing values fail locally; optional proxy and TLS router
  // identifiers accompany the request, and API failures show feedback and
  // return null.
  const exchangeAuthCode = async (
    code: string,
    currentSessionId: string,
    state: string,
    proxyId?: number | null,
    tlsFingerprintRouterId?: number | null,
  ): Promise<OpenAITokenInfo | null> => {
    if (!code.trim() || !currentSessionId || !state.trim()) {
      error.value = 'Missing auth code, session ID, or state'
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const payload: {
        session_id: string // Backend session identifier.
        code: string // Pasted authorization code.
        state: string // Callback correlation state.
        proxy_id?: number // Selected proxy identifier.
        tls_fingerprint_router_id?: number // Selected TLS router.
      } = {
        session_id: currentSessionId,
        code: code.trim(),
        state: state.trim(),
      }
      if (proxyId) {
        payload.proxy_id = proxyId
      }
      if (tlsFingerprintRouterId) {
        payload.tls_fingerprint_router_id = tlsFingerprintRouterId
      }

      const tokenInfo = await adminAPI.providers.exchangeCode(
        `${endpointPrefix}/exchange-code`,
        payload,
      )
      return tokenInfo as OpenAITokenInfo
    } catch (err: any) {
      // Structured OpenAI reasons identify recovery actions such as choosing
      // a proxy. Translate those reasons before using the general exchange
      // failure message.
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.providers.oauth.openai.errors',
        t('admin.providers.oauth.openai.failedToExchangeCode'),
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // validateRefreshToken requests OpenAI token information from a trimmed
  // refresh token. The optional client identifier supports tokens issued
  // through another OAuth client; proxy and TLS router choices are forwarded,
  // and failures return null.
  const validateRefreshToken = async (
    refreshToken: string,
    proxyId?: number | null,
    clientId?: string,
    tlsFingerprintRouterId?: number | null,
  ): Promise<OpenAITokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = 'Missing refresh token'
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const tokenInfo = await adminAPI.providers.refreshOpenAIToken(
        refreshToken.trim(),
        proxyId,
        `${endpointPrefix}/refresh-token`,
        clientId,
        tlsFingerprintRouterId,
      )
      return tokenInfo as OpenAITokenInfo
    } catch (err: any) {
      // Refresh errors share the OpenAI reason translations used by code
      // exchange. A general validation message covers failures with no
      // matching recovery translation.
      error.value = extractI18nErrorMessage(
        err,
        t,
        'admin.providers.oauth.openai.errors',
        t('admin.providers.oauth.openai.failedToValidateRT'),
      )
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // buildCredentials selects OpenAI token and account fields for provider
  // storage. Access token and expiration are included directly, while
  // optional fields require truthy values so partial responses can be applied
  // as updates.
  const buildCredentials = (tokenInfo: OpenAITokenInfo): Record<string, unknown> => {
    const creds: Record<string, unknown> = {
      access_token: tokenInfo.access_token,
      expires_at: tokenInfo.expires_at,
    }
    // A response may omit refresh-token rotation. Omitting that property lets
    // provider updates keep the refresh credential already stored.
    if (tokenInfo.refresh_token) {
      creds.refresh_token = tokenInfo.refresh_token
    }
    if (tokenInfo.id_token) {
      creds.id_token = tokenInfo.id_token
    }
    if (tokenInfo.email) {
      creds.email = tokenInfo.email
    }
    if (tokenInfo.chatgpt_account_id) {
      creds.chatgpt_account_id = tokenInfo.chatgpt_account_id
    }
    if (tokenInfo.chatgpt_user_id) {
      creds.chatgpt_user_id = tokenInfo.chatgpt_user_id
    }
    if (tokenInfo.organization_id) {
      creds.organization_id = tokenInfo.organization_id
    }
    if (tokenInfo.plan_type) {
      creds.plan_type = tokenInfo.plan_type
    }
    if (tokenInfo.subscription_expires_at) {
      creds.subscription_expires_at = tokenInfo.subscription_expires_at
    }
    if (tokenInfo.client_id) {
      creds.client_id = tokenInfo.client_id
    }

    return creds
  }

  // buildExtraInfo selects optional account metadata for the provider form.
  // The returned record contains the available fields; an empty result is
  // represented by undefined so callers can omit extra metadata.
  const buildExtraInfo = (tokenInfo: OpenAITokenInfo): Record<string, string> | undefined => {
    const extra: Record<string, string> = {}
    if (tokenInfo.email) {
      extra.email = tokenInfo.email
    }
    if (tokenInfo.name) {
      extra.name = tokenInfo.name
    }
    if (tokenInfo.privacy_mode) {
      extra.privacy_mode = tokenInfo.privacy_mode
    }
    return Object.keys(extra).length > 0 ? extra : undefined
  }

  return {
    authUrl,
    sessionId,
    oauthState,
    authSessions,
    loading,
    error,
    resetState,
    generateAuthUrl,
    appendAuthUrl,
    removeAuthSession,
    exchangeAuthCode,
    validateRefreshToken,
    buildCredentials,
    buildExtraInfo,
  }
}
