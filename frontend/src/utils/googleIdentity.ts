/**
 * googleIdentity.ts loads Google Identity Services and coordinates the active
 * One Tap callback across SPA pages. Script requests share a module promise
 * and failed loads remove the script so a later attempt can retry. Client
 * initialization is reused until the normalized client ID changes.
 *
 * Prompt ownership follows callback identity. A later show call replaces the
 * active callback before awaiting the SDK, and stale continuations stop after
 * loading. Cancellation with an old callback does not cancel a newer owner's
 * prompt. Eligibility remains a separate predicate over public settings,
 * authentication, captcha, origin, and agreement state; callers apply that
 * predicate before requesting the prompt.
 */

/**
 * The shared script element ID and Google URL identify the SDK resource reused
 * across SPA prompt requests.
 */
const GOOGLE_IDENTITY_SCRIPT_ID = 'google-identity-services'
const GOOGLE_IDENTITY_SCRIPT_SRC = 'https://accounts.google.com/gsi/client'

/**
 * GoogleCredentialResponse carries the credential and selection metadata
 * delivered by Google Identity Services.
 */
export interface GoogleCredentialResponse {
  credential: string // Google credential token
  select_by?: string // SDK selection method
}

/**
 * GoogleOneTapEligibility supplies settings, user state, and browser
 * prerequisites for one prompt decision. All gates are evaluated together;
 * agreement acceptance is required only when the agreement feature is enabled.
 */
export interface GoogleOneTapEligibility {
  publicSettingsLoaded: boolean // public settings ready
  isAuthenticated: boolean // current authentication state
  oneTapEnabled: boolean // One Tap configured
  clientID: string // Google client identifier
  backendModeEnabled: boolean // backend-only login mode
  tencentCaptchaEnabled: boolean // Tencent captcha required
  aliyunCaptchaEnabled: boolean // Aliyun captcha required
  loginAgreementEnabled: boolean // login agreement required
  loginAgreementAccepted: boolean // current agreement accepted
  originSupported: boolean // browser origin eligible
}

/**
 * GoogleIdentityConfiguration supplies initialization settings and the
 * callback used by the SDK client.
 */
interface GoogleIdentityConfiguration {
  client_id: string // Google application identifier
  callback: (response: GoogleCredentialResponse) => void // credential delivery callback
  auto_select: boolean // automatic account selection
  context: 'signin' // SDK sign-in context
  cancel_on_tap_outside: boolean // outside-tap dismissal flag
}

/**
 * GoogleIdentityClient exposes the SDK lifecycle operations used by the shared
 * One Tap coordinator.
 */
interface GoogleIdentityClient {
  /**
   * initialize configures the SDK client with application identity and
   * credential callback. The coordinator invokes it when the normalized client
   * ID changes and forwards later credentials through the current callback
   * owner.
   */
  initialize(config: GoogleIdentityConfiguration): void
  /**
   * prompt asks the initialized SDK client to display One Tap. The coordinator
   * calls it only after its callback ownership check; callers arrange
   * cancellation when that owner leaves.
   */
  prompt(): void
  /**
   * cancel asks the SDK client to dismiss its current One Tap prompt. The
   * coordinator clears callback ownership before invoking it so later credential
   * delivery no longer reaches the cancelled owner.
   */
  cancel(): void
}

declare global {
  /**
   * Window declares the optional SDK globals installed by the external browser
   * script.
   */
  interface Window {
    google?: {
      accounts?: {
        id?: GoogleIdentityClient // Google Identity client
      } // Google accounts namespace
    } // Google SDK namespace
  }
}

/**
 * loadPromise shares SDK loading, initializedClientID tracks the last
 * initialized application, and activeCallback owns delivery and cancellation
 * across page changes.
 */
let loadPromise: Promise<GoogleIdentityClient> | null = null
let initializedClientID = ''
let activeCallback: ((response: GoogleCredentialResponse) => void) | null = null

/**
 * currentGoogleIdentityClient reads the currently installed Google Identity
 * browser client.
 */
function currentGoogleIdentityClient(): GoogleIdentityClient | null {
  return window.google?.accounts?.id ?? null
}

/**
 * loadGoogleIdentityServices returns the installed client or shares a pending
 * script load. It expects browser window and document globals and loads the
 * configured Google script when no matching element exists.
 *
 * Both script errors and a load event without an installed client reject the
 * promise, remove the element, and clear the shared promise for retry.
 * Existing script elements receive listeners rather than duplicate insertion.
 * This loader has no timeout or abort interface, so callers cancel prompt
 * ownership separately.
 */
export function loadGoogleIdentityServices(): Promise<GoogleIdentityClient> {
  const existingClient = currentGoogleIdentityClient()
  if (existingClient) return Promise.resolve(existingClient)
  if (loadPromise) return loadPromise

  loadPromise = new Promise<GoogleIdentityClient>((resolve, reject) => {
    let script = document.getElementById(GOOGLE_IDENTITY_SCRIPT_ID) as HTMLScriptElement | null
    /**
     * handleLoad resolves the SDK load after checking the browser client exists. A
     * load event without that client removes the script and clears the cached
     * promise so a later request can retry.
     */
    const handleLoad = () => {
      const client = currentGoogleIdentityClient()
      if (!client) {
        /**
         * A load event without the expected client cannot complete initialization;
         * remove this script and clear the promise so a later attempt can retry.
         */
        script?.remove()
        loadPromise = null
        reject(new Error('Google Identity Services did not initialize'))
        return
      }
      resolve(client)
    }
    /**
     * handleError rejects the current SDK load and removes the failed script
     * element. Clearing the shared promise allows a later page interaction to
     * create a new load attempt.
     */
    const handleError = () => {
      script?.remove()
      loadPromise = null
      reject(new Error('Failed to load Google Identity Services'))
    }

    if (!script) {
      script = document.createElement('script')
      script.id = GOOGLE_IDENTITY_SCRIPT_ID
      script.src = GOOGLE_IDENTITY_SCRIPT_SRC
      script.async = true
      script.addEventListener('load', handleLoad, { once: true })
      script.addEventListener('error', handleError, { once: true })
      document.head.appendChild(script)
      return
    }

    script.addEventListener('load', handleLoad, { once: true })
    script.addEventListener('error', handleError, { once: true })
  })
  return loadPromise
}

/**
 * showGoogleOneTap prompts with a nonblank client ID and assigns credential
 * delivery to the supplied callback. A blank ID returns without changing the
 * current owner.
 *
 * Ownership is published before awaiting SDK loading and checked again
 * afterward, preventing an older caller with a different callback from
 * prompting after a replacement or cancellation. Initialization occurs when
 * the normalized client ID changes; its SDK callback reads the current owner
 * at delivery time. Callers cancel with the same callback identity when their
 * page stops owning the prompt.
 */
export async function showGoogleOneTap(
  clientID: string,
  callback: (response: GoogleCredentialResponse) => void,
): Promise<void> {
  const normalizedClientID = clientID.trim()
  if (!normalizedClientID) return

  /**
   * Publish callback ownership before SDK loading can suspend, then reject stale
   * continuations so a replaced page cannot prompt after a newer owner arrives.
   */
  activeCallback = callback
  const client = await loadGoogleIdentityServices()
  if (activeCallback !== callback) return

  if (initializedClientID !== normalizedClientID) {
    client.initialize({
      client_id: normalizedClientID,
      callback: (response) => activeCallback?.(response),
      auto_select: false,
      context: 'signin',
      cancel_on_tap_outside: true,
    })
    initializedClientID = normalizedClientID
  }
  client.prompt()
}

/**
 * cancelGoogleOneTap clears the current credential callback and asks the
 * installed SDK client to cancel its prompt. Supplying a callback restricts
 * cancellation to that exact current owner; omitting it cancels whichever
 * owner is active.
 *
 * The identity check precedes both state mutation and SDK cancellation, so
 * teardown from an older page cannot dismiss a different page's prompt.
 * Ownership is cleared before the SDK call, preventing subsequent SDK
 * credential delivery from selecting the cancelled callback. It does not abort
 * the shared script load.
 */
export function cancelGoogleOneTap(callback?: (response: GoogleCredentialResponse) => void): void {
  if (callback && activeCallback !== callback) return
  /**
   * Drop credential delivery ownership before asking the SDK to cancel, so
   * callbacks delivered during or after dismissal have no current recipient.
   */
  activeCallback = null
  currentGoogleIdentityClient()?.cancel()
}

/**
 * isGoogleOneTapOriginSupported accepts HTTPS origins and the listed loopback
 * HTTP hostnames. It defaults to the current browser location, so nonbrowser
 * callers need to supply an explicit location value.
 */
export function isGoogleOneTapOriginSupported(locationValue: Location = window.location): boolean {
  if (locationValue.protocol === 'https:') return true
  if (locationValue.protocol !== 'http:') return false
  return ['localhost', '127.0.0.1', '::1', '[::1]'].includes(locationValue.hostname)
}

/**
 * isGoogleOneTapEligible combines public settings and authentication
 * prerequisites for displaying One Tap. Captcha requirements and an unaccepted
 * enabled login agreement block eligibility even when the client ID and origin
 * are usable.
 */
export function isGoogleOneTapEligible(options: GoogleOneTapEligibility): boolean {
  return (
    options.publicSettingsLoaded &&
    !options.isAuthenticated &&
    options.oneTapEnabled &&
    Boolean(options.clientID.trim()) &&
    !options.backendModeEnabled &&
    !options.tencentCaptchaEnabled &&
    !options.aliyunCaptchaEnabled &&
    (!options.loginAgreementEnabled || options.loginAgreementAccepted) &&
    options.originSupported
  )
}
