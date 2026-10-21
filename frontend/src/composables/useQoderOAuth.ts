import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { QoderPollResponse, QoderSite, QoderTokenInfo } from '@/api/admin/qoder'

// useQoderOAuth.ts manages Qoder device authorization and the credentials
// returned by that flow. URL generation, code exchange, and polling share a
// request generation counter. Starting another operation or invalidating
// pending work changes that counter, so an older response cannot publish
// session data, errors, or completion flags over the current operation.
//
// Callers invalidate requests when changing authorization context and reset
// the form when starting over. Invalidated HTTP requests continue running,
// with their results discarded on completion. Polling performs one request per
// call and exposes the server interval for caller scheduling; the composable
// itself owns no polling timer.

// useQoderOAuth exposes Qoder authorization state and request helpers for a
// provider form. Call it during component setup with the application store
// and i18n context available. Callers invalidate requests when the site or
// form lifetime changes and schedule device polling themselves. Each
// operation captures the generation that may publish its result. Reset
// advances that generation before clearing fields, so late responses cannot
// repopulate a discarded session. Before replacing a pending poll with URL
// generation or code exchange, call invalidatePendingRequests to clear its
// polling flag; those operations otherwise leave that flag set.
export function useQoderOAuth() {
  const appStore = useAppStore()
  const { t } = useI18n()

  // Session refs hold the currently accepted authorization response.
  // requestGeneration owns permission to publish asynchronous results;
  // loading tracks request activity, while polling is cleared by a current
  // poll's completion or invalidation. Superseding a pending poll with URL
  // generation or exchange alone can leave polling true after completion.
  const authUrl = ref('') // Displayed authorization URL.
  const sessionId = ref('') // Current backend session.
  const state = ref('') // Current callback state.
  const pollInterval = ref(2) // Polling interval seconds.
  const loading = ref(false) // Request activity indicator.
  const polling = ref(false) // Poll activity indicator.
  const error = ref('') // Current form feedback.
  let requestGeneration = 0 // Current publication generation.

  // beginRequest advances the generation used by the next authorization
  // operation. Previously captured generations become stale immediately;
  // callers retain the returned value to gate their response and completion
  // handling.
  const beginRequest = () => {
    requestGeneration += 1
    return requestGeneration
  }

  // isCurrentRequest reports whether a captured generation can still publish
  // results.
  const isCurrentRequest = (generation: number) => generation === requestGeneration

  // invalidatePendingRequests makes every captured request generation stale
  // and clears activity flags. In-flight HTTP requests continue, but their
  // guarded completion paths cannot publish feedback after this invalidation.
  const invalidatePendingRequests = () => {
    requestGeneration += 1
    loading.value = false
    polling.value = false
  }

  // resetState invalidates pending authorization work before clearing the
  // displayed form. It restores the default polling interval and clears
  // status feedback, so delayed responses remain stale after the reset.
  const resetState = () => {
    invalidatePendingRequests()
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    pollInterval.value = 2
    loading.value = false
    polling.value = false
    error.value = ''
  }

  // generateAuthUrl starts Qoder authorization for the selected site,
  // defaulting to global. It clears the displayed session and captures a
  // fresh generation before requesting a URL. A stale response or failure
  // returns false and leaves the current operation's fields untouched.
  // Current success publishes the server session and polling interval
  // together, while current failure shows feedback. Completion clears loading
  // only while its generation remains current. Invalidate a pending poll
  // before this call because URL generation does not clear polling.
  const generateAuthUrl = async (
    proxyId: number | null | undefined,
    site: QoderSite = 'global',
  ): Promise<boolean> => {
    const generation = beginRequest()
    loading.value = true
    authUrl.value = ''
    sessionId.value = ''
    state.value = ''
    error.value = ''

    try {
      const payload: Record<string, unknown> = { site }
      if (proxyId) payload.proxy_id = proxyId

      const response = await adminAPI.qoder.generateAuthUrl(payload as any)
      // Another operation or reset may own the form now. Ignore this
      // completion so it cannot replace the current session or feedback.
      if (!isCurrentRequest(generation)) return false
      authUrl.value = response.auth_url
      sessionId.value = response.session_id
      state.value = response.state
      // A missing or zero server interval uses the form default of two
      // seconds for the caller scheduling subsequent polls.
      pollInterval.value = response.interval || 2
      return true
    } catch (err: any) {
      // Check ownership before selecting failure feedback. A stale URL
      // request must not show an error for a form that has already changed
      // authorization context.
      if (!isCurrentRequest(generation)) return false
      error.value =
        err.response?.data?.detail ||
        err.message ||
        t('admin.providers.oauth.qoder.failedToGenerateUrl')
      appStore.showError(error.value)
      return false
    } finally {
      // A newer request may still be running when this one finishes. Check
      // the generation before clearing activity to avoid hiding that request.
      if (isCurrentRequest(generation)) {
        loading.value = false
      }
    }
  }

  // exchangeAuthCode submits a Qoder session with an optional code or
  // callback URL. Session identifier and state are required; device
  // authorization may omit both optional inputs. Starting the request
  // invalidates previous operations through a fresh generation. A stale
  // response returns null, and stale failures leave current feedback
  // untouched. Current errors show a toast and return null, while completion
  // clears loading only for the generation that owns the request. Invalidate
  // a pending poll before this call because code exchange does not clear
  // polling.
  const exchangeAuthCode = async (params: {
    code?: string // Pasted authorization code.
    callbackUrl?: string // Pasted callback URL.
    sessionId: string // Backend session identifier.
    state: string // Callback correlation state.
  }): Promise<QoderTokenInfo | null> => {
    if (!params.sessionId || !params.state) {
      error.value = t('admin.providers.oauth.qoder.missingExchangeParams')
      return null
    }

    const generation = beginRequest()
    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = {
        session_id: params.sessionId,
        state: params.state,
      }
      const code = params.code?.trim()
      const callbackUrl = params.callbackUrl?.trim()
      if (code) payload.code = code
      if (callbackUrl) payload.callback_url = callbackUrl

      const tokenInfo = await adminAPI.qoder.exchangeCode(payload as any)
      // Another operation or reset may own the form now. Ignore this
      // completion so it cannot replace the current session or feedback.
      if (!isCurrentRequest(generation)) return null
      return tokenInfo as QoderTokenInfo
    } catch (err: any) {
      // An exchange can fail after another operation has started. Check its
      // generation before replacing feedback so current session errors
      // remain authoritative.
      if (!isCurrentRequest(generation)) return null
      error.value =
        err.response?.data?.detail ||
        err.message ||
        t('admin.providers.oauth.qoder.failedToExchangeCode')
      appStore.showError(error.value)
      return null
    } finally {
      // A newer request may still be running when this one finishes. Check
      // the generation before clearing activity to avoid hiding that request.
      if (isCurrentRequest(generation)) {
        loading.value = false
      }
    }
  }

  // pollAuthorization checks a Qoder device session once and returns its
  // current status. Callers supply the session and state and schedule
  // subsequent polls themselves. Each poll captures a fresh generation, which
  // makes older authorization work stale. Results and failures from a stale
  // generation return null. Current failures show feedback, and the owning
  // generation clears both loading and polling when its request completes.
  const pollAuthorization = async (params: {
    sessionId: string // Backend session identifier.
    state: string // Callback correlation state.
  }): Promise<QoderPollResponse | null> => {
    if (!params.sessionId || !params.state) {
      error.value = t('admin.providers.oauth.qoder.missingExchangeParams')
      return null
    }

    const generation = beginRequest()
    polling.value = true
    loading.value = true
    error.value = ''

    try {
      const payload: Record<string, unknown> = {
        session_id: params.sessionId,
        state: params.state,
      }

      const result = await adminAPI.qoder.poll(payload as any)
      // A poll can complete after a site change. Return null for that stale
      // response so callers discard its authorization result.
      return isCurrentRequest(generation) ? result : null
    } catch (err: any) {
      // A rejected poll may belong to an obsolete session. Check its
      // generation before reporting the failure to the current authorization
      // form.
      if (!isCurrentRequest(generation)) return null
      error.value =
        err.response?.data?.detail ||
        err.message ||
        t('admin.providers.oauth.qoder.failedToExchangeCode')
      appStore.showError(error.value)
      return null
    } finally {
      // A newer request may still be running when this one finishes. Check
      // the generation before clearing activity to avoid hiding that request.
      if (isCurrentRequest(generation)) {
        loading.value = false
        polling.value = false
      }
    }
  }

  // buildCredentials copies Qoder token fields into the provider credential
  // record. The extra object retains its response reference, so callers that
  // mutate nested metadata also change that shared response object.
  const buildCredentials = (tokenInfo: QoderTokenInfo): Record<string, unknown> => ({
    security_oauth_token: tokenInfo.security_oauth_token,
    refresh_token: tokenInfo.refresh_token,
    machine_id: tokenInfo.machine_id,
    machine_token: tokenInfo.machine_token,
    machine_type: tokenInfo.machine_type,
    uid: tokenInfo.uid,
    aid: tokenInfo.aid,
    organization_id: tokenInfo.organization_id,
    organization_name: tokenInfo.organization_name,
    name: tokenInfo.name,
    user_type: tokenInfo.user_type,
    site: tokenInfo.site,
    refresh_mode: tokenInfo.refresh_mode,
    expires_at: tokenInfo.expires_at,
    extra: tokenInfo.extra,
  })

  return {
    authUrl,
    sessionId,
    state,
    pollInterval,
    loading,
    polling,
    error,
    invalidatePendingRequests,
    resetState,
    generateAuthUrl,
    exchangeAuthCode,
    pollAuthorization,
    buildCredentials,
  }
}
