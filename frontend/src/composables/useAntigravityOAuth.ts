import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { AntigravityTokenInfo } from '@/api/admin/antigravity'

// useAntigravityOAuth.ts prepares Antigravity authorization requests and
// provider credentials. The UI receives a URL, session identifier, and state
// together, then submits the session information with a pasted authorization
// code. Request failures populate local error state, while URL generation and
// code exchange also notify the application store.
//
// Refresh validation may return an access token without a replacement refresh
// token. Credential construction carries the submitted refresh token into
// that result when the returned value is absent or whitespace, so saving the
// response retains the credential needed for a later refresh. Numeric
// expiration values become integer strings for storage.

// useAntigravityOAuth exposes Antigravity authorization state and credential
// builders for a provider form. Call it during component setup with an
// active application store and i18n context. URL and exchange operations
// show global failures, while refresh validation leaves feedback local for
// batch callers. Callers retain the submitted refresh token and pass it to
// buildCredentials when saving a validation response, because the response
// may omit a replacement credential.
export function useAntigravityOAuth() {
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

      const response = await adminAPI.antigravity.generateAuthUrl(payload as any)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      state.value = response.state
      return true
    } catch (err: any) {
      // Prefer the generation response detail when available. The localized
      // message supplies readable feedback when the failure has no response
      // detail.
      error.value =
        err.response?.data?.detail || t('admin.providers.oauth.antigravity.failedToGenerateUrl')
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
  }): Promise<AntigravityTokenInfo | null> => {
    const code = params.code?.trim()
    if (!code || !params.sessionId || !params.state) {
      error.value = t('admin.providers.oauth.antigravity.missingExchangeParams')
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

      const tokenInfo = await adminAPI.antigravity.exchangeCode(payload as any)
      return tokenInfo as AntigravityTokenInfo
    } catch (err: any) {
      // A rejected exchange keeps the entered session available for
      // correction. Response detail takes precedence over the localized
      // exchange message shown in the toast.
      error.value =
        err.response?.data?.detail || t('admin.providers.oauth.antigravity.failedToExchangeCode')
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // validateRefreshToken exchanges a trimmed refresh token for Antigravity
  // token information. Blank input or request failure returns null and
  // updates local feedback; batch callers display that feedback without a
  // global toast.
  const validateRefreshToken = async (
    refreshToken: string,
    proxyId?: number | null,
  ): Promise<AntigravityTokenInfo | null> => {
    if (!refreshToken.trim()) {
      error.value = t('admin.providers.oauth.antigravity.pleaseEnterRefreshToken')
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const tokenInfo = await adminAPI.antigravity.refreshAntigravityToken(
        refreshToken.trim(),
        proxyId,
      )
      return tokenInfo as AntigravityTokenInfo
    } catch (err: any) {
      // Refresh validation can run across many submitted tokens. Store each
      // failure locally and return null so batch callers decide how to
      // present individual failures.
      error.value =
        err.response?.data?.detail || t('admin.providers.oauth.antigravity.failedToValidateRT')
      return null
    } finally {
      loading.value = false
    }
  }

  // buildCredentials combines returned token fields with the submitted
  // refresh credential for saving. A nonblank returned refresh token takes
  // precedence, while an absent or whitespace response uses
  // fallbackRefreshToken. Numeric expiration values are floored and
  // stringified, nonblank strings are trimmed, and other values become
  // undefined. The selected refresh token keeps its supplied text so the
  // credential record carries the actual replacement or submitted value
  // through the refresh round trip.
  const buildCredentials = (
    tokenInfo: AntigravityTokenInfo,
    fallbackRefreshToken?: string,
  ): Record<string, unknown> => {
    let expiresAt: string | undefined
    // Numeric expiration is stored as an integer string, matching the
    // credential format consumed when the provider is saved.
    if (typeof tokenInfo.expires_at === 'number' && Number.isFinite(tokenInfo.expires_at)) {
      expiresAt = Math.floor(tokenInfo.expires_at).toString()
    } else if (typeof tokenInfo.expires_at === 'string' && tokenInfo.expires_at.trim()) {
      expiresAt = tokenInfo.expires_at.trim()
    }
    // A refresh response can omit rotation. Carry the submitted credential
    // forward so saving the validated token retains refresh access.
    const refreshToken = tokenInfo.refresh_token?.trim()
      ? tokenInfo.refresh_token
      : fallbackRefreshToken

    return {
      access_token: tokenInfo.access_token,
      refresh_token: refreshToken,
      token_type: tokenInfo.token_type,
      expires_at: expiresAt,
      project_id: tokenInfo.project_id,
      email: tokenInfo.email,
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
    buildCredentials,
  }
}
