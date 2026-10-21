import { beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * googleIdentity.spec.ts checks script loading, prompt ownership, and One Tap
 * eligibility with a synthetic SDK client. Module resets isolate shared loader
 * state, and manually dispatched script events cover retries without
 * contacting Google or relying on an installed client.
 */

/**
 * installGoogleClient installs a synthetic Google Identity global with
 * observable lifecycle methods. It returns the same client for assertions,
 * while the suite resets the global and module state between cases.
 */
function installGoogleClient() {
  const client = {
    initialize: vi.fn(),
    prompt: vi.fn(),
    cancel: vi.fn(),
  }
  Object.defineProperty(window, 'google', {
    configurable: true,
    writable: true,
    value: { accounts: { id: client } },
  })
  return client
}

describe('Google Identity Services loader', () => {
  beforeEach(() => {
    /**
     * Discard module loader state and the synthetic global between cases,
     * preventing a completed SDK load or active callback from leaking into another
     * test.
     */
    vi.resetModules()
    document.getElementById('google-identity-services')?.remove()
    Object.defineProperty(window, 'google', {
      configurable: true,
      writable: true,
      value: undefined,
    })
  })

  /**
   * Two prompt requests overlap before the SDK finishes loading. Only one script
   * and initialization must occur, and credential delivery must reach the latest
   * callback owner.
   */
  it('loads the official script once and initializes one client for the SPA', async () => {
    const { showGoogleOneTap } = await import('@/utils/googleIdentity')
    const firstCallback = vi.fn()
    const secondCallback = vi.fn()

    const firstPrompt = showGoogleOneTap('google-client', firstCallback)
    const secondPrompt = showGoogleOneTap('google-client', secondCallback)
    const scripts = document.querySelectorAll<HTMLScriptElement>('#google-identity-services')
    expect(scripts).toHaveLength(1)
    expect(scripts[0].src).toBe('https://accounts.google.com/gsi/client')
    expect(scripts[0].async).toBe(true)

    const client = installGoogleClient()
    scripts[0].dispatchEvent(new Event('load'))
    await Promise.all([firstPrompt, secondPrompt])

    expect(client.initialize).toHaveBeenCalledOnce()
    expect(client.initialize).toHaveBeenCalledWith(
      expect.objectContaining({
        client_id: 'google-client',
        auto_select: false,
        context: 'signin',
      }),
    )
    expect(client.prompt).toHaveBeenCalledOnce()

    const configuration = client.initialize.mock.calls[0][0]
    configuration.callback({ credential: 'credential' })
    expect(firstCallback).not.toHaveBeenCalled()
    expect(secondCallback).toHaveBeenCalledWith({ credential: 'credential' })

    await showGoogleOneTap('google-client', firstCallback)
    expect(client.initialize).toHaveBeenCalledOnce()
    expect(client.prompt).toHaveBeenCalledTimes(2)
  })

  /**
   * An old callback attempts cancellation after another callback owns the
   * prompt. The SDK cancel method must run only when the current owner requests
   * cancellation.
   */
  it('cancels only the currently active prompt callback', async () => {
    const { cancelGoogleOneTap, showGoogleOneTap } = await import('@/utils/googleIdentity')
    const client = installGoogleClient()
    const activeCallback = vi.fn()
    const staleCallback = vi.fn()

    await showGoogleOneTap('google-client', activeCallback)
    cancelGoogleOneTap(staleCallback)
    expect(client.cancel).not.toHaveBeenCalled()

    cancelGoogleOneTap(activeCallback)
    expect(client.cancel).toHaveBeenCalledOnce()
  })

  /**
   * The SDK script emits an error before a client appears. The failed element
   * must be removed, and a later load must create a fresh script that can
   * resolve.
   */
  it('removes a failed script and allows a later retry', async () => {
    const { loadGoogleIdentityServices } = await import('@/utils/googleIdentity')
    const firstLoad = loadGoogleIdentityServices()
    const firstScript = document.querySelector<HTMLScriptElement>('#google-identity-services')
    expect(firstScript).not.toBeNull()
    firstScript?.dispatchEvent(new Event('error'))
    await expect(firstLoad).rejects.toThrow('Failed to load Google Identity Services')
    expect(document.getElementById('google-identity-services')).toBeNull()

    const secondLoad = loadGoogleIdentityServices()
    const secondScript = document.querySelector<HTMLScriptElement>('#google-identity-services')
    expect(secondScript).not.toBeNull()
    expect(secondScript).not.toBe(firstScript)
    const client = installGoogleClient()
    secondScript?.dispatchEvent(new Event('load'))
    await expect(secondLoad).resolves.toBe(client)
  })

  /**
   * A load event fires without installing the expected Google client. The loader
   * must reject and remove that element, allowing a later complete load to
   * succeed.
   */
  it('retries after a loaded script does not expose the GIS client', async () => {
    const { loadGoogleIdentityServices } = await import('@/utils/googleIdentity')
    const firstLoad = loadGoogleIdentityServices()
    const firstScript = document.querySelector<HTMLScriptElement>('#google-identity-services')
    expect(firstScript).not.toBeNull()

    firstScript?.dispatchEvent(new Event('load'))
    await expect(firstLoad).rejects.toThrow('Google Identity Services did not initialize')
    expect(document.getElementById('google-identity-services')).toBeNull()

    const secondLoad = loadGoogleIdentityServices()
    const secondScript = document.querySelector<HTMLScriptElement>('#google-identity-services')
    expect(secondScript).not.toBeNull()
    expect(secondScript).not.toBe(firstScript)
    const client = installGoogleClient()
    secondScript?.dispatchEvent(new Event('load'))
    await expect(secondLoad).resolves.toBe(client)
  })
})

describe('Google One Tap eligibility', () => {
  /**
   * A fully eligible configuration is modified one prerequisite at a time. Each
   * blocking setting must prevent One Tap, while accepted required agreements
   * restore eligibility.
   */
  it('requires every security and configuration gate', async () => {
    const { isGoogleOneTapEligible } = await import('@/utils/googleIdentity')
    const eligible = {
      publicSettingsLoaded: true,
      isAuthenticated: false,
      oneTapEnabled: true,
      clientID: 'google-client',
      backendModeEnabled: false,
      tencentCaptchaEnabled: false,
      aliyunCaptchaEnabled: false,
      loginAgreementEnabled: false,
      loginAgreementAccepted: false,
      originSupported: true,
    }
    expect(isGoogleOneTapEligible(eligible)).toBe(true)

    for (const key of ['publicSettingsLoaded', 'oneTapEnabled', 'originSupported'] as const) {
      expect(isGoogleOneTapEligible({ ...eligible, [key]: false }), key).toBe(false)
    }
    for (const key of [
      'isAuthenticated',
      'backendModeEnabled',
      'tencentCaptchaEnabled',
      'aliyunCaptchaEnabled',
    ] as const) {
      expect(isGoogleOneTapEligible({ ...eligible, [key]: true }), key).toBe(false)
    }
    expect(isGoogleOneTapEligible({ ...eligible, clientID: ' ' })).toBe(false)
    expect(
      isGoogleOneTapEligible({
        ...eligible,
        loginAgreementEnabled: true,
        loginAgreementAccepted: false,
      }),
    ).toBe(false)
    expect(
      isGoogleOneTapEligible({
        ...eligible,
        loginAgreementEnabled: true,
        loginAgreementAccepted: true,
      }),
    ).toBe(true)
  })

  /**
   * Origin checks compare HTTPS, local HTTP, and remote HTTP fixtures. Secure
   * and loopback origins must qualify, while the ordinary remote HTTP origin
   * remains unsupported.
   */
  it('supports HTTPS and local HTTP origins only', async () => {
    const { isGoogleOneTapOriginSupported } = await import('@/utils/googleIdentity')
    expect(
      isGoogleOneTapOriginSupported({ protocol: 'https:', hostname: 'app.example' } as Location),
    ).toBe(true)
    expect(
      isGoogleOneTapOriginSupported({ protocol: 'http:', hostname: 'localhost' } as Location),
    ).toBe(true)
    expect(
      isGoogleOneTapOriginSupported({ protocol: 'http:', hostname: '127.0.0.1' } as Location),
    ).toBe(true)
    expect(
      isGoogleOneTapOriginSupported({ protocol: 'http:', hostname: 'app.example' } as Location),
    ).toBe(false)
  })
})
