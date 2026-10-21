/**
 * tencentCaptcha.ts loads the Tencent captcha SDK for the configured regional
 * site and exposes its constructor types. Domestic and international scripts
 * install the same browser global but use different constructor signatures, so
 * a constructor needs a matching region before reuse.
 *
 * A shared promise coalesces loads for the currently recorded region. The
 * loader checks the SDK global-region marker after a load event because an
 * event alone does not establish the constructor identity. Failed loads reset
 * module state for retry. A known loaded-region change rejects and asks for a
 * page reload; overlapping pending requests for different regions are not
 * serialized by this module.
 */

/**
 * TencentCaptchaRegion selects domestic or international captcha scripts with
 * incompatible constructor signatures.
 */
export type TencentCaptchaRegion = 'cn' | 'intl'

/**
 * TencentCaptchaProof contains the challenge ticket and random string
 * submitted for verification.
 */
export interface TencentCaptchaProof {
  ticket: string // challenge verification ticket
  randstr: string // challenge random string
}

/**
 * TencentCaptchaResult represents SDK completion data, including optional
 * success proof and error details.
 */
export interface TencentCaptchaResult {
  ret: number // SDK result code
  ticket?: string | null // challenge verification ticket
  randstr?: string | null // challenge random string
  errorCode?: number // SDK error code
  errorMessage?: string // SDK error message
}

/**
 * TencentCaptchaInstance exposes display and cleanup operations for a
 * caller-owned captcha widget.
 */
interface TencentCaptchaInstance {
  /**
   * show opens the captcha interaction for the caller-owned SDK instance. The
   * caller creates the instance using its regional constructor signature and
   * retains it for cleanup after the interaction ends.
   */
  show(): void
  /**
   * destroy releases the captcha SDK instance owned by the caller. Invoke it
   * when the widget lifecycle ends; loading the shared script does not
   * automatically dispose individual widget instances.
   */
  destroy(): void
}

/**
 * TencentCaptchaCallback receives the SDK result after the captcha interaction
 * completes.
 */
type TencentCaptchaCallback = (result: TencentCaptchaResult) => void

/**
 * TencentCaptchaConstructor represents both regional SDK signatures. The
 * domestic form starts with an application ID, while the international form
 * first requires the checkbox container element; callers select the signature
 * matching their region.
 */
type TencentCaptchaConstructor = {
  /**
   * The domestic constructor receives the application identifier as its first
   * argument and returns a caller-owned captcha instance. Use the element-first
   * overload only when the selected region is international.
   */
  new (
    appId: string,
    callback: TencentCaptchaCallback,
    options?: Record<string, unknown>,
  ): TencentCaptchaInstance
  /**
   * The international constructor first requires a caller-owned checkbox
   * container, followed by the application identifier. Its argument order
   * differs from the domestic signature, so callers select it using the
   * configured region.
   */
  new (
    element: HTMLElement,
    appId: string,
    callback: TencentCaptchaCallback,
    options?: Record<string, unknown>,
  ): TencentCaptchaInstance
}

declare global {
  /**
   * Window declares the optional SDK globals installed by the external browser
   * script.
   */
  interface Window {
    TencentCaptcha?: TencentCaptchaConstructor // regional SDK constructor

    /**
     * The international SDK marks this true; the loader uses it to check the
     * installed constructor region.
     */
    TCaptchaGlobal?: boolean
  }
}

/**
 * SCRIPT_SRC selects the domestic or international SDK entry point matching
 * the caller captcha region and constructor signature.
 */
const SCRIPT_SRC: Record<TencentCaptchaRegion, string> = {
  cn: 'https://turing.captcha.qcloud.com/TJCaptcha.js',
  intl: 'https://ca.turing.captcha.qcloud.com/TJNCaptcha-global.js',
}

/**
 * normalizeTencentCaptchaRegion accepts intl and otherwise selects the
 * domestic site. Missing, null, and unrecognized values share the cn default,
 * matching the form representation used by captcha configuration consumers.
 */
export function normalizeTencentCaptchaRegion(value?: string | null): TencentCaptchaRegion {
  return value === 'intl' ? 'intl' : 'cn'
}

/**
 * scriptPromise shares the current region load, while loadedRegion records its
 * region for reuse checks and resets after a failed load.
 */
let scriptPromise: Promise<TencentCaptchaConstructor> | null = null
let loadedRegion: TencentCaptchaRegion | null = null

/**
 * existingGlobalRegion infers the installed SDK region for constructor reuse
 * from the browser marker. Only true selects international; false, missing,
 * and other marker values select the domestic region.
 */
function existingGlobalRegion(): TencentCaptchaRegion {
  return window.TCaptchaGlobal === true ? 'intl' : 'cn'
}

/**
 * loadTencentCaptcha returns a constructor for the requested regional SDK,
 * defaulting to the domestic site. It reuses a global constructor when the
 * recorded or inferred region matches, and shares a pending promise for the
 * same recorded region.
 *
 * A known constructor from another recorded region causes rejection so callers
 * reload before switching incompatible signatures. Load completion checks the
 * global marker before resolving. Script errors and unusable globals clear
 * cached state for retry; requests for different regions while loading have no
 * generation guard and require caller coordination.
 */
export function loadTencentCaptcha(
  region: TencentCaptchaRegion = 'cn',
): Promise<TencentCaptchaConstructor> {
  /**
   * Both regional scripts publish the same constructor name, so reuse checks the
   * recorded or SDK-reported region before returning an existing global.
   */
  const globalRegion = window.TencentCaptcha ? existingGlobalRegion() : null
  if (window.TencentCaptcha && (loadedRegion === region || globalRegion === region)) {
    return Promise.resolve(window.TencentCaptcha)
  }
  if (window.TencentCaptcha && loadedRegion !== null && loadedRegion !== region) {
    /**
     * A known region change cannot safely reuse the incompatible constructor
     * signature; reject and let the caller reload the page before continuing.
     */
    return Promise.reject(new Error('Tencent Captcha region changed; reload the page to apply it'))
  }
  if (scriptPromise && loadedRegion === region) return scriptPromise

  loadedRegion = region
  scriptPromise = new Promise((resolve, reject) => {
    const script = document.createElement('script')
    script.src = SCRIPT_SRC[region]
    script.async = true
    script.onload = () => {
      /**
       * The load event alone does not establish which constructor is installed;
       * require the SDK marker to match before resolving this regional load.
       */
      if (window.TencentCaptcha && existingGlobalRegion() === region) {
        resolve(window.TencentCaptcha)
        return
      }
      /**
       * Reset both cached fields when the loaded script cannot supply a matching
       * constructor, so the next caller can attempt another load.
       */
      scriptPromise = null
      loadedRegion = null
      reject(new Error('Tencent Captcha SDK is unavailable'))
    }
    /**
     * A script network failure invalidates the shared promise and region together
     * before rejection, allowing a later interaction to attempt loading again.
     */
    script.onerror = () => {
      scriptPromise = null
      loadedRegion = null
      reject(new Error('Failed to load Tencent Captcha SDK'))
    }
    document.head.appendChild(script)
  })

  return scriptPromise
}

/**
 * resetTencentCaptchaLoaderForTest clears module promise and region
 * bookkeeping for isolated tests. It does not remove script elements or
 * browser globals, so test fixtures clean up those resources separately.
 */
export function resetTencentCaptchaLoaderForTest(): void {
  scriptPromise = null
  loadedRegion = null
}
