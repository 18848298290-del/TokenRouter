/**
 * alipayDeepLink.ts launches an Alipay precreate QR payload through the mobile application
 * scheme and reports whether the browser appears to hand off. A visible browser that stays
 * in place reaches a QR fallback state after the applicable delay, while a pagehide event
 * or hidden document marks the launch as backgrounded.
 *
 * One launcher owns its visibility listeners and fallback timer. Repeated launches replace
 * the previous deadline, and disposal removes both listeners and suppresses later state
 * notifications. Restricted embedded browsers use a shorter deadline so users can reach QR
 * instructions promptly when application schemes do not open from that browser.
 */
// Launch deadlines are milliseconds; embedded browsers receive QR fallback sooner when their
// scheme navigation stays in the foreground.
export const ALIPAY_DEEP_LINK_FALLBACK_DELAY_MS = 2200
export const ALIPAY_EMBEDDED_BROWSER_FALLBACK_DELAY_MS = 300

/**
 * AlipayDeepLinkState describes browser handoff progress and available QR fallback.
 */
export type AlipayDeepLinkState = 'idle' | 'launching' | 'backgrounded' | 'fallback'

// The scan application identifier accepts a single encoded dynamic QR payload in its qrcode
// query parameter.
const ALIPAY_DEEP_LINK_PREFIX = 'alipays://platformapi/startapp?saId=10000007&qrcode='

/**
 * buildAlipayDeepLink URL-encodes one trimmed dynamic QR payload into the Alipay scheme.
 * Empty payloads return an empty string, allowing the launcher to display QR fallback
 * without attempting navigation.
 */
export function buildAlipayDeepLink(qrCode: string): string {
  const dynamicQRCode = qrCode.trim()
  if (!dynamicQRCode) return ''
  return `${ALIPAY_DEEP_LINK_PREFIX}${encodeURIComponent(dynamicQRCode)}`
}

/**
 * isAlipaySchemeRestrictedBrowser identifies WeChat and QQ browser user-agent markers.
 */
export function isAlipaySchemeRestrictedBrowser(userAgent: string): boolean {
  return /MicroMessenger|MQQBrowser|\bQQ\//i.test(userAgent)
}

/**
 * EventTargetLike supplies the browser listener operations owned by one launcher.
 */
interface EventTargetLike {
  /**
   * addEventListener subscribes a callback to the supplied browser lifecycle event.
   * The launcher retains the callback reference so dispose can remove that exact
   * subscription from the same event target.
   */
  addEventListener(type: string, listener: EventListener): void
  /**
   * removeEventListener removes the callback previously installed for this event.
   * Callers supply the same callback identity and event target used at registration,
   * allowing launcher disposal to release its subscriptions.
   */
  removeEventListener(type: string, listener: EventListener): void
}

/**
 * VisibilityDocumentLike exposes document visibility alongside its lifecycle event
 * methods.
 */
interface VisibilityDocumentLike extends EventTargetLike {
  readonly hidden: boolean // document visibility state
}

/**
 * AlipayDeepLinkLauncherOptions supplies browser resources and callbacks for one QR
 * payload.
 */
export interface AlipayDeepLinkLauncherOptions {
  qrCode: string // dynamic gateway QR payload
  /**
   * document and lifecycleTarget provide event sources for the launcher's lifetime.
   * The launcher retains installed callback identities and removes both subscriptions
   * when the owner calls dispose after leaving the payment waiting state.
   */
  document: VisibilityDocumentLike // document visibility event source
  lifecycleTarget: EventTargetLike // pagehide event source
  userAgent: string // browser user-agent value
  assignLocation: (url: string) => void // application scheme navigation callback
  onStateChange: (state: AlipayDeepLinkState) => void // payment panel state publisher
  /**
   * setTimer and clearTimer can provide a matching test scheduler and canceller.
   * Omitted functions use their global timer equivalents, and the launcher owns
   * cancellation of the timer handle produced for each accepted launch attempt.
   */
  setTimer?: typeof setTimeout // optional injected timer scheduler
  clearTimer?: typeof clearTimeout // optional injected timer canceller
}

/**
 * AlipayDeepLinkLauncher starts app handoff and releases its owned browser resources.
 */
export interface AlipayDeepLinkLauncher {
  /**
   * launch attempts app handoff using the configured QR payload and replaces the
   * prior fallback deadline. Calls after disposal have no effect; state updates
   * are delivered through the configured callback while active.
   */
  launch(): void
  /**
   * dispose releases the launcher's event listeners and pending fallback timer.
   * Call it when the owning payment panel leaves its waiting state or unmounts;
   * subsequent launch calls cannot publish state.
   */
  dispose(): void
}

/**
 * createAlipayDeepLinkLauncher installs browser visibility listeners and returns launch
 * and disposal operations. The caller owns disposal when the payment panel is replaced or
 * unmounted. A launch clears any preceding fallback timer, reports launching, and attempts
 * scheme navigation.
 *
 * Browser backgrounding cancels the fallback deadline and reports a handoff state; staying
 * visible reports fallback when the deadline expires. Scheme assignment exceptions also
 * report fallback. Disposal clears the current timer, removes the installed listeners, and
 * suppresses future state publication through the shared disposed flag.
 */
export function createAlipayDeepLinkLauncher(
  options: AlipayDeepLinkLauncherOptions,
): AlipayDeepLinkLauncher {
  // Injected scheduling functions let component tests advance launch deadlines while the
  // production launcher uses browser timers.
  const setTimer = options.setTimer ?? setTimeout
  const clearTimer = options.clearTimer ?? clearTimeout
  let timer: ReturnType<typeof setTimeout> | null = null
  let disposed = false

  /**
   * setState publishes progress through the configured payment-panel callback.
   * A disposed launcher suppresses the callback, allowing a queued lifecycle
   * observation to finish after the owning panel has released its resources.
   */
  const setState = (state: AlipayDeepLinkState) => {
    if (!disposed) options.onStateChange(state)
  }
  /**
   * clearFallbackTimer cancels the currently owned launch deadline and clears its
   * handle. Repeated cleanup is safe, and a subsequent launch can install a new
   * deadline without retaining the preceding attempt's timer.
   */
  const clearFallbackTimer = () => {
    if (timer) {
      clearTimer(timer)
      timer = null
    }
  }
  /**
   * markBackgrounded cancels visible-page fallback before reporting application
   * handoff. Both visibilitychange and pagehide use this ordering so a later
   * fallback timer cannot reveal QR instructions after observed browser departure.
   */
  const markBackgrounded = () => {
    clearFallbackTimer()
    setState('backgrounded')
  }
  /**
   * handleVisibilityChange reports handoff when the document becomes hidden.
   * Returning to a visible document leaves the existing state unchanged, so
   * reopening the payment page does not start another launch automatically.
   */
  const handleVisibilityChange: EventListener = () => {
    if (options.document.hidden) markBackgrounded()
  }
  /**
   * handlePageHide reports browser departure through the shared handoff operation.
   * It cancels the pending launch deadline even when visibilitychange has not
   * reported a hidden document for this navigation.
   */
  const handlePageHide: EventListener = () => markBackgrounded()

  options.document.addEventListener('visibilitychange', handleVisibilityChange)
  options.lifecycleTarget.addEventListener('pagehide', handlePageHide)

  return {
    /**
     * launch starts an Alipay scheme attempt and replaces the prior fallback deadline.
     * Calls after disposal have no effect; empty QR payloads and navigation assignment
     * failures publish fallback immediately. After assignment, the browser-specific timer
     * publishes backgrounded when the document is hidden and fallback while visible.
     * A visibilitychange that reports a hidden document, or a pagehide event, cancels a
     * pending deadline before reporting handoff. That pending timer therefore cannot
     * subsequently replace the backgrounded state with QR instructions.
     */
    launch() {
      if (disposed) return
      // A repeat tap owns a fresh deadline, so the previous attempt cannot publish fallback
      // during the new launch.
      clearFallbackTimer()
      const deepLink = buildAlipayDeepLink(options.qrCode)
      if (!deepLink) {
        setState('fallback')
        return
      }

      setState('launching')
      try {
        options.assignLocation(deepLink)
      } catch {
        // Navigation assignment can fail inside an embedded browser; retain the order and
        // expose its QR recovery path.
        setState('fallback')
        return
      }

      const delay = isAlipaySchemeRestrictedBrowser(options.userAgent)
        ? ALIPAY_EMBEDDED_BROWSER_FALLBACK_DELAY_MS
        : ALIPAY_DEEP_LINK_FALLBACK_DELAY_MS
      timer = setTimer(() => {
        timer = null
        if (options.document.hidden) {
          setState('backgrounded')
          return
        }
        setState('fallback')
      }, delay)
    },
    /**
     * dispose cancels the fallback timer and removes both lifecycle listeners.
     * The disposed flag also suppresses future notifications, making repeated
     * cleanup safe when a payment state transition and component unmount coincide.
     */
    dispose() {
      // Remove the launch deadline and event sources together so a discarded panel cannot
      // receive later handoff notifications.
      clearFallbackTimer()
      options.document.removeEventListener('visibilitychange', handleVisibilityChange)
      options.lifecycleTarget.removeEventListener('pagehide', handlePageHide)
      disposed = true
    },
  }
}
