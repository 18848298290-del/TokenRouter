/**
 * device.ts classifies browser environments for mobile and iOS interactions
 * using user-agent and input capabilities. Detection accepts an injected
 * environment for tests, and browser wrappers return false when navigator is
 * unavailable.
 *
 * Mobile client hints, handheld tokens, tablet tokens, and touch input provide
 * independent evidence. Desktop-mode iPad browsers need the MacIntel platform
 * plus multiple touch points because their user-agent can resemble macOS.
 * Coarse pointer and lack of hover only qualify as a mobile fallback when
 * touch is also present. The result is a UI heuristic, not a guarantee about
 * physical device identity.
 */

/**
 * NavigatorUADataLike contains the optional mobile hint consumed by device
 * detection.
 */
interface NavigatorUADataLike {
  mobile?: boolean // browser mobile hint
}

/**
 * NavigatorLike supplies browser identity and touch observations for injected
 * detection environments.
 */
interface NavigatorLike {
  userAgent?: string // browser identity string
  platform?: string // browser platform string
  maxTouchPoints?: number // maximum simultaneous touches
  userAgentData?: NavigatorUADataLike // browser client hints
}

/**
 * MediaQueryResultLike carries the boolean result consumed from a media-query
 * evaluation.
 */
interface MediaQueryResultLike {
  matches: boolean // media-query match result
}

/**
 * DeviceDetectionEnvironment supplies optional browser observations without
 * requiring global browser objects.
 */
interface DeviceDetectionEnvironment {
  navigator?: NavigatorLike // injected browser observations
  matchMedia?: (query: string) => MediaQueryResultLike | null | undefined // injected media-query evaluator
}

/**
 * These user-agent token patterns provide handheld, tablet, and iOS evidence
 * alongside the separately evaluated touch capabilities.
 */
const MOBILE_UA_RE = /\b(Mobi|Android|iPhone|iPod|Windows Phone|webOS|BlackBerry|IEMobile)\b/i
const TABLET_UA_RE = /\b(iPad|Tablet)\b/i
const IOS_UA_RE = /\b(iPhone|iPad|iPod)\b/i

/**
 * matchesQuery reads an optional media-query matcher and reports an exact true
 * match. Missing matchers, missing results, and thrown browser errors become
 * false so device detection can use its remaining evidence.
 */
function matchesQuery(
  matchMedia: DeviceDetectionEnvironment['matchMedia'],
  query: string,
): boolean {
  try {
    return matchMedia?.(query)?.matches === true
  } catch {
    /**
     * Unsupported or throwing media-query APIs contribute no positive signal,
     * allowing user-agent and touch observations to determine the remaining device
     * classification.
     */
    return false
  }
}

/**
 * detectMobileDevice combines client hints, user-agent tokens, and input
 * capabilities to classify a supplied environment. Missing navigator data
 * returns false, and an affirmative mobile client hint returns true
 * immediately.
 *
 * A desktop-mode iPad qualifies through MacIntel with multiple touch points.
 * Other unrecognized devices qualify through the combined coarse-pointer,
 * no-hover, and touch observations rather than any single input signal. Failed
 * media-query checks count as false. Callers use this heuristic for interface
 * decisions and should not interpret it as browser or hardware authentication.
 */
export function detectMobileDevice(env: DeviceDetectionEnvironment = {}): boolean {
  const nav = env.navigator
  if (!nav) return false

  if (nav.userAgentData?.mobile === true) {
    return true
  }

  const userAgent = nav.userAgent || ''
  const maxTouchPoints = nav.maxTouchPoints ?? 0
  /**
   * Desktop-mode iPads can report MacIntel rather than an iPad token; multiple
   * touch points distinguish this heuristic from an ordinary desktop Mac.
   */
  const isIPadOSDesktopMode = nav.platform === 'MacIntel' && maxTouchPoints > 1
  const isMobileUA = MOBILE_UA_RE.test(userAgent)
  const isTabletUA = TABLET_UA_RE.test(userAgent) || isIPadOSDesktopMode
  const coarsePointer = matchesQuery(env.matchMedia, '(pointer: coarse)')
  const noHover = matchesQuery(env.matchMedia, '(hover: none)')
  const hasTouch = maxTouchPoints > 0

  return isMobileUA || isTabletUA || (coarsePointer && noHover && hasTouch)
}

/**
 * isMobileDevice evaluates mobile detection using the current browser
 * navigator and media queries. It returns false without navigator and supplies
 * no matcher when window is unavailable in the execution environment.
 */
export function isMobileDevice(): boolean {
  if (typeof navigator === 'undefined') return false

  return detectMobileDevice({
    navigator,
    matchMedia: typeof window !== 'undefined' ? window.matchMedia.bind(window) : undefined,
  })
}

/**
 * detectIOSDevice recognizes iOS tokens or a desktop-mode iPad signature in
 * the supplied navigator. Missing navigator returns false; MacIntel requires
 * more than one touch point to avoid classifying ordinary desktop Macs.
 */
export function detectIOSDevice(env: DeviceDetectionEnvironment = {}): boolean {
  const nav = env.navigator
  if (!nav) return false

  const userAgent = nav.userAgent || ''
  const maxTouchPoints = nav.maxTouchPoints ?? 0

  const isIPadOSDesktopMode = nav.platform === 'MacIntel' && maxTouchPoints > 1

  return IOS_UA_RE.test(userAgent) || isIPadOSDesktopMode
}

/**
 * isIOSDevice classifies the current navigator through the iOS detection
 * helper. Environments without navigator return false, making this wrapper
 * usable by callers that can also execute outside the browser.
 */
export function isIOSDevice(): boolean {
  if (typeof navigator === 'undefined') return false

  return detectIOSDevice({ navigator })
}
