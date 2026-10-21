import { ref } from 'vue'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'

// useProviderOAuth.ts handles provider OAuth and setup-token authorization
// forms. The selected add method chooses matching generation and exchange
// endpoints, while manual codes and cookie sessions share status reporting
// and return token metadata for the caller to save.

// AddMethod selects OAuth or setup-token endpoints for provider
// authorization.
export type AddMethod = 'oauth' | 'setup-token'

// AuthInputMethod identifies credential input forms offered by provider
// authorization dialogs.
export type AuthInputMethod =
  | 'manual' // Pasted authorization callback.
  | 'cookie' // Browser session cookie.
  | 'refresh_token' // Provider refresh token.
  | 'mobile_refresh_token' // Mobile client refresh.
  | 'session_token' // Browser session token.
  | 'access_token' // Direct access token.
  | 'codex_session' // Codex session credentials.
  | 'agent_identity' // Agent identity credentials.
  | 'codex_pat' // Codex personal token.
  | 'sso_cookie' // Single sign-on cookie.
  | 'email_password' // Email password credentials.

// TokenInfo describes account metadata returned by provider authorization
// endpoints.
export interface TokenInfo {
  org_uuid?: string // Account organization UUID.
  account_uuid?: string // Authorized account UUID.
  email_address?: string // Account email address.
  [key: string]: unknown // Additional response fields.
}

// useProviderOAuth exposes reactive authorization fields and request helpers
// for the provider form. Call it during component setup with an active
// application store. The caller saves returned token information.
export function useProviderOAuth() {
  const appStore = useAppStore()

  // Authorization refs belong to this form instance. Request helpers update
  // loading and error for the form, and URL generation publishes the session
  // fields used for exchange.
  const authUrl = ref('') // Displayed authorization URL.
  const authCode = ref('') // Pasted authorization code.
  const sessionId = ref('') // Current backend session.
  const sessionKey = ref('') // Entered cookie credential.
  const loading = ref(false) // Request activity indicator.
  const error = ref('') // Current form feedback.

  // resetState clears authorization fields and the current request feedback.
  // Callers use it when resetting the form; pending HTTP requests continue
  // and may subsequently publish their results.
  const resetState = () => {
    authUrl.value = ''
    authCode.value = ''
    sessionId.value = ''
    sessionKey.value = ''
    loading.value = false
    error.value = ''
  }

  // generateAuthUrl requests a fresh authorization URL and replaces the
  // displayed session. A truthy proxy identifier is forwarded; request
  // failures update local feedback, show an error toast, and return false.
  const generateAuthUrl = async (
    addMethod: AddMethod,
    proxyId?: number | null,
  ): Promise<boolean> => {
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    error.value = ''

    try {
      const proxyConfig = proxyId ? { proxy_id: proxyId } : {}
      const endpoint =
        addMethod === 'oauth'
          ? '/admin/providers/generate-auth-url'
          : '/admin/providers/generate-setup-token-url'

      const response = await adminAPI.providers.generateAuthUrl(endpoint, proxyConfig)
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      return true
    } catch (err: any) {
      // The server detail supplies generation feedback when present. A fixed
      // message covers transport failures that arrive without a structured
      // response body.
      error.value = err.response?.data?.detail || 'Failed to generate auth URL'
      appStore.showError(error.value)
      return false
    } finally {
      loading.value = false
    }
  }

  // exchangeAuthCode submits the current form code and session to the
  // selected authorization endpoint. Missing input returns null locally;
  // request failures show an error toast and return null to the form.
  const exchangeAuthCode = async (
    addMethod: AddMethod,
    proxyId?: number | null,
  ): Promise<TokenInfo | null> => {
    if (!authCode.value.trim() || !sessionId.value) {
      error.value = 'Missing auth code or session ID'
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const proxyConfig = proxyId ? { proxy_id: proxyId } : {}
      const endpoint =
        addMethod === 'oauth'
          ? '/admin/providers/exchange-code'
          : '/admin/providers/exchange-setup-token-code'

      const tokenInfo = await adminAPI.providers.exchangeCode(endpoint, {
        session_id: sessionId.value,
        code: authCode.value.trim(),
        ...proxyConfig,
      })

      return tokenInfo as TokenInfo
    } catch (err: any) {
      // The server detail supplies exchange feedback when present. The form
      // keeps its current inputs while a fixed message covers failures
      // without response detail.
      error.value = err.response?.data?.detail || 'Failed to exchange auth code'
      appStore.showError(error.value)
      return null
    } finally {
      loading.value = false
    }
  }

  // cookieAuth submits a trimmed session key to the selected cookie
  // authorization endpoint. Blank input and request failures return null with
  // local feedback, allowing callers to report individual batch failures.
  const cookieAuth = async (
    addMethod: AddMethod,
    sessionKeyValue: string,
    proxyId?: number | null,
  ): Promise<TokenInfo | null> => {
    if (!sessionKeyValue.trim()) {
      error.value = 'Please enter sessionKey'
      return null
    }

    loading.value = true
    error.value = ''

    try {
      const proxyConfig = proxyId ? { proxy_id: proxyId } : {}
      const endpoint =
        addMethod === 'oauth'
          ? '/admin/providers/cookie-auth'
          : '/admin/providers/setup-token-cookie-auth'

      const tokenInfo = await adminAPI.providers.exchangeCode(endpoint, {
        session_id: '',
        code: sessionKeyValue.trim(),
        ...proxyConfig,
      })

      return tokenInfo as TokenInfo
    } catch (err: any) {
      // Cookie authorization can run for several submitted session keys.
      // Keep each failure in local state so the caller can summarize the
      // batch results.
      error.value = err.response?.data?.detail || 'Cookie authorization failed'
      return null
    } finally {
      loading.value = false
    }
  }

  // parseSessionKeys returns trimmed nonempty session keys from newline-separated input.
  const parseSessionKeys = (input: string): string[] => {
    return input
      .split('\n')
      .map((k) => k.trim())
      .filter((k) => k)
  }

  // buildExtraInfo selects optional account metadata for the provider form.
  // The returned record contains the available fields; an empty result is
  // represented by undefined so callers can omit extra metadata.
  const buildExtraInfo = (tokenInfo: TokenInfo): Record<string, string> | undefined => {
    const extra: Record<string, string> = {}
    if (tokenInfo.org_uuid) {
      extra.org_uuid = tokenInfo.org_uuid
    }
    if (tokenInfo.account_uuid) {
      extra.account_uuid = tokenInfo.account_uuid
    }
    if (tokenInfo.email_address) {
      extra.email_address = tokenInfo.email_address
    }
    return Object.keys(extra).length > 0 ? extra : undefined
  }

  return {
    authUrl,
    authCode,
    sessionId,
    sessionKey,
    loading,
    error,
    resetState,
    generateAuthUrl,
    exchangeAuthCode,
    cookieAuth,
    parseSessionKeys,
    buildExtraInfo,
  }
}
