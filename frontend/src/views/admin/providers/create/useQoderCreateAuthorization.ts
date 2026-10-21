import { computed, onBeforeUnmount, ref } from 'vue'
import { useQoderOAuth } from '@/composables/useQoderOAuth'
import type { QoderSite, QoderTokenInfo } from '@/api/admin/qoder'

// useQoderCreateAuthorization.ts owns the device authorization lifecycle for
// provider creation, including browser popup leases, polling, callback exchange,
// and the creation lock. Poll requests and authorization flows use separate
// generations because stopping polling before a callback exchange must leave
// that exchange eligible to create the provider within the same flow.
//
// The caller supplies current dialog identity and the persistence boundary.
// Responses only affect the flow that captured them, and popup cleanup checks
// its own lease before closing a browser window. Creation cannot be cancelled
// once submitted, so the lock remains owned by its generation until settlement.

// QoderCreateAuthorizationOptions connects dialog identity and persistence to
// one authorization controller without exposing its polling or popup bookkeeping.
interface QoderCreateAuthorizationOptions {
  // show, platform, and site are live identity readers; each completion compares
  // them with its captured flow before changing current authorization or provider state.
  show: () => boolean // current dialog visibility
  platform: () => string // selected provider platform
  site: () => QoderSite // selected authorization site
  proxyId: () => number | null // selected outbound proxy
  // submitting and setSubmitting share the dialog persistence flag; only the
  // generation holding the creation lock clears it when that persistence call settles.
  submitting: () => boolean // shared submission status
  setSubmitting: (value: boolean) => void // shared submission update
  oauthState: () => string // pasted callback state
  // create persists credentials and checks the supplied guard before publishing
  // completion, so stale requests cannot close a newer dialog flow.
  create: (token: QoderTokenInfo, current: () => boolean) => Promise<boolean>
  popupBlocked: () => void // blocked popup notification
  authFailed: () => string // localized authorization failure
  showError: (message: string) => void // visible error notification
}

// useQoderCreateAuthorization coordinates one dialog's authorization resources.
// Call it during component setup so unmount can stop polling and close its popup.
// The create callback must respect the supplied identity guard when publishing
// asynchronous completion; the controller keeps its creation lock until that
// callback settles, even if the visible form has changed in the meantime.
// Flow invalidation and polling invalidation remain distinct because callback
// exchange pauses polling without invalidating its own authorization identity.
export function useQoderCreateAuthorization(options: QoderCreateAuthorizationOptions) {
  const qoderOAuth = useQoderOAuth()
  // Poll generation owns the interval and in-flight admission flag; flow
  // generation separately owns completed authorization and the pending creation lock.
  let qoderPollTimer: number | null = null // active interval handle
  // QoderAuthPopupLease associates the named browser window with its owning
  // generation.
  interface QoderAuthPopupLease {
    generation: number // popup ownership generation
    popup: Window | null // opened browser window
  }

  let qoderAuthPopupLease: QoderAuthPopupLease | null = null // current popup ownership
  let qoderAuthPopupGeneration = 0 // monotonically allocated lease identity
  let qoderPollInFlight = false // current poll admission
  let qoderPollGeneration = 0 // polling invalidation generation
  const qoderFlowGeneration = ref(0) // authorization identity generation
  // qoderProviderCreateGeneration stays occupied after flow invalidation until
  // its pending persistence request settles; null admits a new provider creation.
  const qoderProviderCreateGeneration = ref<number | null>(null)
  let qoderOAuthCompleted = false // current flow completion

  // The reactive lock drives dialog controls while the request remains pending,
  // including after other authorization state has been invalidated or reset.
  const isQoderOAuthProviderCreating = computed(() => qoderProviderCreateGeneration.value !== null)
  // getQoderPopupFeatures centers a bounded authorization window within the
  // available screen.
  const getQoderPopupFeatures = () => {
    // Leave forty pixels of screen margin around the centered authorization window.
    const width = Math.min(1100, (window.screen?.availWidth || 1100) - 40)
    const height = Math.min(820, (window.screen?.availHeight || 820) - 40)
    const left = Math.max(0, Math.floor(((window.screen?.availWidth || width) - width) / 2))
    const top = Math.max(0, Math.floor(((window.screen?.availHeight || height) - height) / 2))
    return `width=${width},height=${height},left=${left},top=${top},scrollbars=yes,resizable=yes`
  }

  // stopQoderPolling retires the active polling generation and clears its interval.
  // It also invalidates the OAuth composable's transport generation, so outstanding
  // requests lose permission to publish their results even though HTTP work continues.
  // The poll admission flag becomes available for a later polling generation, whose
  // flag cannot be released by an old poll's finally branch. Authorization flow
  // identity stays valid so manual exchange can continue after this polling pause.
  const stopQoderPolling = () => {
    // Invalidate request ownership before clearing the timer so pending responses
    // cannot publish completion after the polling loop has been stopped.
    qoderPollGeneration += 1
    qoderOAuth.invalidatePendingRequests()
    if (qoderPollTimer) {
      window.clearInterval(qoderPollTimer)
      qoderPollTimer = null
    }
    qoderPollInFlight = false
  }

  // closeQoderAuthPopup releases the current popup when its optional captured lease
  // still owns the window. Late URL-generation completions pass their lease because
  // the browser can reuse the same named window across authorization attempts.
  // A mismatched generation leaves the current lease and window available to the
  // newer attempt. Calls without a lease release whichever popup is current, as used
  // by dialog cleanup and accepted authorization completion.
  const closeQoderAuthPopup = (lease?: QoderAuthPopupLease) => {
    const currentLease = qoderAuthPopupLease
    if (!currentLease || (lease && currentLease.generation !== lease.generation)) return

    // Detach the lease before closing so a resumed obsolete request cannot
    // retain ownership of the named window after a newer flow acquires it.
    qoderAuthPopupLease = null
    currentLease.popup?.close()
  }

  // resetQoderOAuthCompletionState invalidates authorization identity and clears
  // completion status. It does not release a pending creation lock; the request
  // that acquired that lock remains responsible for releasing it after
  // settlement.
  const resetQoderOAuthCompletionState = () => {
    // Advance flow identity independently of polling so stopping device checks
    // does not invalidate a callback exchange belonging to the same authorization.
    qoderFlowGeneration.value += 1
    qoderOAuthCompleted = false
  }

  // QoderFlowContext captures the authorization generation and selected site for
  // completion checks.
  interface QoderFlowContext {
    generation: number // captured flow generation
    site: QoderSite // captured authorization site
  }

  // captureQoderFlowContext snapshots the current generation and authorization
  // site for asynchronous work.
  const captureQoderFlowContext = (): QoderFlowContext => ({
    generation: qoderFlowGeneration.value,
    site: options.site(),
  })
  // isCurrentQoderFlow reports whether a captured authorization still belongs to
  // the visible Qoder form. Platform, site, generation, and dialog visibility
  // must all match before publishing asynchronous results.
  const isCurrentQoderFlow = (context: QoderFlowContext) =>
    context.generation === qoderFlowGeneration.value &&
    context.site === options.site() &&
    options.platform() === 'qoder' &&
    options.show()
  // createQoderOAuthProvider admits one completed authorization into provider
  // persistence. Missing tokens, obsolete flows, completed flows, and an occupied
  // creation lock return false without submitting another request. The captured
  // generation owns the lock until persistence settles, even when the selected
  // site or platform changes while the request is pending. Only that owner clears
  // the lock and shared submission state; successful current completion marks the
  // authorization consumed.
  const createQoderOAuthProvider = async (
    tokenInfo: QoderTokenInfo | undefined,
    context: QoderFlowContext,
  ): Promise<boolean> => {
    if (
      !tokenInfo ||
      !isCurrentQoderFlow(context) ||
      qoderProviderCreateGeneration.value !== null ||
      qoderOAuthCompleted
    )
      return false

    qoderProviderCreateGeneration.value = context.generation
    try {
      if (!isCurrentQoderFlow(context)) return false
      const created = await options.create(tokenInfo, () => isCurrentQoderFlow(context))
      if (created && isCurrentQoderFlow(context)) {
        qoderOAuthCompleted = true
      }
      return created
    } finally {
      // Release only the generation that acquired this lock; an obsolete request
      // must not release another flow's pending provider creation state.
      if (qoderProviderCreateGeneration.value === context.generation) {
        qoderProviderCreateGeneration.value = null
        options.setSubmitting(false)
      }
    }
  }

  // pollQoderAuthorizationOnce polls a complete session when no poll or completed
  // creation blocks it. Both the polling generation and authorization identity
  // must still match before a response can stop polling, close the popup, or
  // create a provider. A manual exchange invalidates pending poll results while
  // preserving the flow identity for its own result. The finally branch releases
  // only its still-current poll admission flag.
  const pollQoderAuthorizationOnce = async () => {
    if (
      qoderPollInFlight ||
      qoderOAuthCompleted ||
      !qoderOAuth.sessionId.value ||
      !qoderOAuth.state.value
    )
      return
    const generation = qoderPollGeneration
    const flowContext = captureQoderFlowContext()
    const sessionId = qoderOAuth.sessionId.value
    const state = qoderOAuth.state.value
    if (!isCurrentQoderFlow(flowContext)) return
    qoderPollInFlight = true

    try {
      const result = await qoderOAuth.pollAuthorization({
        sessionId,
        state,
      })
      if (generation !== qoderPollGeneration || !isCurrentQoderFlow(flowContext)) return
      if (!result && qoderOAuth.error.value) {
        stopQoderPolling()
        return
      }
      if (result?.status !== 'completed' || !result.token_info) return

      // Stop competing device checks and release their popup before persistence;
      // a later interval response must not create the same authorized provider.
      stopQoderPolling()
      closeQoderAuthPopup()
      await createQoderOAuthProvider(result.token_info, flowContext)
    } finally {
      if (generation === qoderPollGeneration) {
        qoderPollInFlight = false
      }
    }
  }

  // startQoderPolling replaces the active polling interval and launches an immediate
  // check for the current authorization. Stopping the previous loop advances polling
  // and transport generations before the new timer captures ownership, so queued old
  // ticks and responses cannot complete the new loop. The interval defaults to two
  // seconds and is clamped to at least one second. Each current tick delegates to
  // pollQoderAuthorizationOnce, whose admission flag prevents overlapping polls.
  const startQoderPolling = (intervalSeconds = 2) => {
    stopQoderPolling()
    const generation = qoderPollGeneration
    void pollQoderAuthorizationOnce()
    // Server polling intervals use seconds; browser timers require milliseconds.
    const intervalMs = Math.max(1, intervalSeconds) * 1000
    qoderPollTimer = window.setInterval(() => {
      if (generation !== qoderPollGeneration) return
      void pollQoderAuthorizationOnce()
    }, intervalMs)
  }

  // handleGenerateUrl starts a new device authorization with a synchronously
  // opened browser popup. Pending provider persistence blocks generation
  // replacement because its server request cannot be cancelled. Each invocation
  // owns a popup lease, so a late response can close only the window still
  // associated with that invocation. Current successful generation navigates the
  // window and starts polling; blocked popups show a warning while polling still
  // proceeds.
  const handleGenerateUrl = async () => {
    // Preserve identity while persistence is pending because replacing the flow
    // would classify its eventual successful server response as obsolete.
    if (options.submitting() || isQoderOAuthProviderCreating.value) return
    stopQoderPolling()
    closeQoderAuthPopup()
    resetQoderOAuthCompletionState()
    const flowContext = captureQoderFlowContext()
    const authPopup = window.open('about:blank', 'qoderAuthPopup', getQoderPopupFeatures())
    const popupLease: QoderAuthPopupLease = {
      generation: ++qoderAuthPopupGeneration,
      popup: authPopup,
    }
    qoderAuthPopupLease = popupLease
    const ok = await qoderOAuth.generateAuthUrl(options.proxyId(), flowContext.site)
    if (!isCurrentQoderFlow(flowContext)) {
      closeQoderAuthPopup(popupLease)
      return
    }
    if (!ok) {
      closeQoderAuthPopup(popupLease)
      return
    }
    if (authPopup) {
      authPopup.location.href = qoderOAuth.authUrl.value
      authPopup.focus()
    } else {
      options.popupBlocked()
    }
    startQoderPolling(qoderOAuth.pollInterval.value)
  }

  // handleQoderExchange completes the current authorization from pasted callback
  // input. It pauses polling before exchange so a pending device response cannot
  // race the manual result into duplicate provider creation. Failed exchange
  // resumes previously active polling only while the original flow remains
  // current and still has session credentials. Once token exchange succeeds,
  // persistence failure leaves manual retry available without resuming device
  // polling; stale completion cannot overwrite current loading state.
  const handleQoderExchange = async (authCode: string) => {
    const flowContext = captureQoderFlowContext()
    if (
      !qoderOAuth.sessionId.value ||
      !isCurrentQoderFlow(flowContext) ||
      qoderOAuthCompleted ||
      qoderProviderCreateGeneration.value !== null
    )
      return

    const shouldResumePolling = qoderPollTimer !== null
    const sessionId = qoderOAuth.sessionId.value
    stopQoderPolling()
    qoderOAuth.loading.value = true
    qoderOAuth.error.value = ''
    // resumePollingIfNeeded restores device polling after manual exchange cannot
    // produce a token for the captured flow. The pre-exchange timer snapshot records
    // whether there was a polling loop to resume, and current flow identity rejects
    // recovery after a site, platform, or dialog change. Existing replacement timers
    // keep ownership of their loop. Both session fields must remain available before
    // startQoderPolling creates a fresh polling generation and starts its immediate check.
    const resumePollingIfNeeded = () => {
      if (
        isCurrentQoderFlow(flowContext) &&
        shouldResumePolling &&
        !qoderPollTimer &&
        qoderOAuth.sessionId.value &&
        qoderOAuth.state.value
      ) {
        startQoderPolling(qoderOAuth.pollInterval.value)
      }
    }

    let exchanged = false
    try {
      const stateFromInput = options.oauthState()
      const stateToUse = stateFromInput || qoderOAuth.state.value
      if (!stateToUse) {
        qoderOAuth.error.value = options.authFailed()
        options.showError(qoderOAuth.error.value)
        resumePollingIfNeeded()
        return
      }

      const rawInput = authCode.trim()
      const tokenInfo = await qoderOAuth.exchangeAuthCode({
        code: rawInput,
        callbackUrl: rawInput,
        sessionId,
        state: stateToUse,
      })
      if (!isCurrentQoderFlow(flowContext)) return
      if (!tokenInfo) {
        resumePollingIfNeeded()
        return
      }

      exchanged = true
      await createQoderOAuthProvider(tokenInfo, flowContext)
    } catch (error: any) {
      if (!isCurrentQoderFlow(flowContext)) return
      qoderOAuth.error.value = error.response?.data?.detail || options.authFailed()
      options.showError(qoderOAuth.error.value)
      if (!exchanged) {
        resumePollingIfNeeded()
      }
    } finally {
      if (isCurrentQoderFlow(flowContext)) {
        qoderOAuth.loading.value = false
      }
    }
  }

  // Component disposal calls stopQoderPolling to retire polling and transport
  // generations, then releases the popup lease so pending responses cannot reclaim it.
  onBeforeUnmount(() => {
    stopQoderPolling()
    closeQoderAuthPopup()
  })

  return {
    qoderOAuth,
    isQoderOAuthProviderCreating,
    stopQoderPolling,
    closeQoderAuthPopup,
    resetQoderOAuthCompletionState,
    handleGenerateUrl,
    handleQoderExchange,
  }
}
