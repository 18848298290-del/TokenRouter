import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TencentCaptchaGate from '@/components/auth/TencentCaptchaGate.vue'
import { loadTencentCaptcha, resetTencentCaptchaLoaderForTest } from '@/utils/tencentCaptcha'

// TencentCaptchaGate.spec.ts checks domestic and international Tencent challenge
// lifecycles using local SDK constructors. The cases exercise proof reuse,
// expiration, cancellation, loader region restrictions, and reset while
// controlling vendor callbacks and browser script events.

// locale supplies the active language for every SDK fixture and resets to
// Chinese before each test case.
const locale = { value: 'zh' }

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ locale }),
}))

// CaptchaResult represents the vendor completion payload supplied by local SDK
// fixtures.
type CaptchaResult = {
  ret: number // Vendor completion status code.
  ticket?: string | null // Optional vendor proof ticket.
  randstr?: string | null // Optional vendor random proof.
  errorCode?: number // Optional vendor failure code.
}

describe('TencentCaptchaGate', () => {
  beforeEach(() => {
    locale.value = 'zh'
    delete window.TencentCaptcha
    delete window.TCaptchaGlobal
    document.head
      .querySelectorAll('script[src*="TJCaptcha.js"], script[src*="TJNCaptcha-global.js"]')
      .forEach((node) => node.remove())
    resetTencentCaptchaLoaderForTest()
  })

  // The domestic widget mounts before any verification request. It must expose
  // no visible button because its challenge is opened through the authentication
  // action method.
  it('does not render a visible verification button', () => {
    const wrapper = mount(TencentCaptchaGate, { props: { appId: '123456789' } })

    expect(wrapper.find('button').exists()).toBe(false)
  })

  // The domestic SDK delivers a successful ticket and random string. The pending
  // authentication verification must resolve both proof fields exactly as
  // provided by the callback.
  it('resolves proof after Tencent SDK success', async () => {
    let callback: ((result: CaptchaResult) => void) | undefined
    window.TencentCaptcha = class {
      // constructor captures the SDK proof callback for the completion scenario.
      constructor(_appId: string, resultCallback: (result: CaptchaResult) => void) {
        callback = resultCallback
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    }
    const wrapper = mount(TencentCaptchaGate, { props: { appId: '123456789' } })

    const verification = wrapper.vm.verify()
    await flushPromises()
    callback?.({ ret: 0, ticket: 'ticket-value', randstr: 'rand-value' })

    await expect(verification).resolves.toEqual({ ticket: 'ticket-value', randstr: 'rand-value' })
  })

  // The SDK reports that the user closed its popup. The waiting authentication
  // call must resolve null so cancellation cannot be mistaken for a successful
  // proof.
  it('resolves null when the user closes the popup', async () => {
    let callback: ((result: CaptchaResult) => void) | undefined
    window.TencentCaptcha = class {
      // constructor captures the SDK proof callback for the completion scenario.
      constructor(_appId: string, resultCallback: (result: CaptchaResult) => void) {
        callback = resultCallback
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    }
    const wrapper = mount(TencentCaptchaGate, { props: { appId: '123456789' } })

    const verification = wrapper.vm.verify()
    await flushPromises()
    callback?.({ ret: 2, ticket: null })

    await expect(verification).resolves.toBeNull()
  })

  // SDK loading fails in one attempt and another returns an error ticket. Both
  // verification promises must reject rather than allowing unusable proof data
  // into authentication.
  it('rejects SDK load failures and disaster-recovery tickets', async () => {
    const failedLoad = mount(TencentCaptchaGate, { props: { appId: '123456789' } })
    const loadVerification = failedLoad.vm.verify()
    await flushPromises()
    const script = document.head.querySelector<HTMLScriptElement>('script[src*="TJCaptcha.js"]')
    expect(script).not.toBeNull()
    script?.dispatchEvent(new Event('error'))
    await expect(loadVerification).rejects.toThrow('Failed to load Tencent Captcha SDK')

    let callback: ((result: CaptchaResult) => void) | undefined
    window.TencentCaptcha = class {
      // constructor captures the SDK proof callback for the completion scenario.
      constructor(_appId: string, resultCallback: (result: CaptchaResult) => void) {
        callback = resultCallback
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    }
    const failedResult = mount(TencentCaptchaGate, { props: { appId: '123456789' } })
    const resultVerification = failedResult.vm.verify()
    await flushPromises()
    callback?.({ ret: 0, ticket: 'trerror_1001_123456789', randstr: '@fallback', errorCode: 1001 })

    await expect(resultVerification).rejects.toThrow('Tencent Captcha verification failed')
  })

  // Two verification calls arrive before the domestic challenge completes. They
  // must share one SDK opening and both resolve the same successful proof
  // fields.
  it('reuses one pending promise for concurrent verify calls', async () => {
    const show = vi.fn()
    let callback: ((result: CaptchaResult) => void) | undefined
    window.TencentCaptcha = class {
      // constructor captures the SDK proof callback for the completion scenario.
      constructor(_appId: string, resultCallback: (result: CaptchaResult) => void) {
        callback = resultCallback
      }
      show = show // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    }
    const wrapper = mount(TencentCaptchaGate, { props: { appId: '123456789' } })

    const first = wrapper.vm.verify()
    const second = wrapper.vm.verify()
    await flushPromises()
    callback?.({ ret: 0, ticket: 'ticket-value', randstr: 'rand-value' })

    await expect(first).resolves.toEqual({ ticket: 'ticket-value', randstr: 'rand-value' })
    await expect(second).resolves.toEqual({ ticket: 'ticket-value', randstr: 'rand-value' })
    expect(show).toHaveBeenCalledOnce()
  })

  // International SDK initialization receives the form container before the
  // application ID, keeping its checkbox beside the authentication controls.
  // The international SDK initializes inside the authentication form. Its first
  // argument must be the visible component container, followed by application
  // ID, callback, and popup options.
  it('passes a visible in-form container first for the international site', async () => {
    const args: unknown[][] = []
    let callback: ((result: CaptchaResult) => void) | undefined
    window.TCaptchaGlobal = true
    window.TencentCaptcha = class {
      // constructor records SDK arguments for verifying the selected regional call signature.
      constructor(...received: unknown[]) {
        args.push(received)
        callback = received[2] as (result: CaptchaResult) => void
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha
    const wrapper = mount(TencentCaptchaGate, {
      props: { appId: '123456789', region: 'intl' },
    })

    const verification = wrapper.vm.verify()
    await flushPromises()

    const container = wrapper.get('[data-testid="tencent-captcha-international-container"]')
    expect(args).toHaveLength(1)
    expect(args[0][0]).toBe(container.element)
    expect(args[0][0]).not.toBe(document.body)
    await vi.waitFor(() => expect(container.classes()).not.toContain('scale-[0.01]'))
    expect(args[0][1]).toBe('123456789')
    expect(typeof args[0][2]).toBe('function')
    expect(args[0][3]).toMatchObject({ enableAutoCheck: false, type: 'popup' })

    callback?.({ ret: 0, ticket: 'ticket-value', randstr: 'rand-value' })
    await expect(verification).resolves.toEqual({
      ticket: 'ticket-value',
      randstr: 'rand-value',
    })
    expect(container.classes()).not.toContain('scale-[0.01]')
  })

  // The international widget mounts before form submission. Its checkbox
  // container must become visible after initialization so the user can complete
  // verification in advance.
  it('preloads and displays the international checkbox on mount', async () => {
    window.TCaptchaGlobal = true
    window.TencentCaptcha = class {
      // constructor supplies an inert SDK instance for this initialization scenario.
      constructor() {}
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha
    const wrapper = mount(TencentCaptchaGate, {
      props: { appId: '123456789', region: 'intl' },
    })

    const container = wrapper.get('[data-testid="tencent-captcha-international-container"]')
    await flushPromises()
    await vi.waitFor(() => expect(container.classes()).not.toContain('scale-[0.01]'))
  })

  // International preloading completes before the form calls verify. The form
  // must receive the cached proof and the completed checkbox must remain visible
  // until reset.
  it('reuses a preloaded proof when the SDK reports success', async () => {
    let callback: ((result: CaptchaResult) => void) | undefined
    window.TCaptchaGlobal = true
    window.TencentCaptcha = class {
      // constructor captures the SDK proof callback for the completion scenario.
      constructor(...received: unknown[]) {
        callback = received[2] as (result: CaptchaResult) => void
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha
    const wrapper = mount(TencentCaptchaGate, {
      props: { appId: '123456789', region: 'intl' },
    })

    await flushPromises()
    callback?.({ ret: 0, ticket: 'preloaded-ticket', randstr: 'preloaded-rand' })
    await flushPromises()

    await expect(wrapper.vm.verify()).resolves.toEqual({
      ticket: 'preloaded-ticket',
      randstr: 'preloaded-rand',
    })
    expect(
      wrapper.get('[data-testid="tencent-captcha-international-container"]').classes(),
    ).not.toContain('scale-[0.01]')
  })

  // An international proof reaches the configured four-minute cache limit.
  // Verification must create another SDK instance and return a fresh proof
  // supplied by that replacement challenge.
  it('refreshes a preloaded proof before the provider ticket expires', async () => {
    vi.useFakeTimers()
    try {
      let callback: ((result: CaptchaResult) => void) | undefined
      let constructorCount = 0
      window.TCaptchaGlobal = true
      window.TencentCaptcha = class {
        // constructor counts initialized instances and captures the current proof callback.
        constructor(...received: unknown[]) {
          constructorCount += 1
          callback = received[2] as (result: CaptchaResult) => void
        }
        show = vi.fn() // Vendor display call recorder.
        destroy = vi.fn() // Vendor cleanup call recorder.
      } as unknown as typeof window.TencentCaptcha
      const wrapper = mount(TencentCaptchaGate, {
        props: { appId: '123456789', region: 'intl' },
      })

      await flushPromises()
      callback?.({ ret: 0, ticket: 'expired-ticket', randstr: 'expired-rand' })
      await flushPromises()
      vi.advanceTimersByTime(4 * 60 * 1000)

      const verification = wrapper.vm.verify()
      await flushPromises()
      expect(constructorCount).toBe(2)

      callback?.({ ret: 0, ticket: 'fresh-ticket', randstr: 'fresh-rand' })
      await expect(verification).resolves.toEqual({
        ticket: 'fresh-ticket',
        randstr: 'fresh-rand',
      })
    } finally {
      vi.useRealTimers()
    }
  })

  // The domestic site starts an explicit challenge. Constructor arguments must
  // begin with application ID and callback so the international container
  // argument is not inserted.
  it('keeps the three-argument form for the Chinese mainland site', async () => {
    const args: unknown[][] = []
    window.TencentCaptcha = class {
      // constructor records SDK arguments for verifying the selected regional call signature.
      constructor(...received: unknown[]) {
        args.push(received)
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha
    const wrapper = mount(TencentCaptchaGate, { props: { appId: '123456789' } })

    void wrapper.vm.verify()
    await flushPromises()

    expect(args[0][0]).toBe('123456789')
    expect(typeof args[0][1]).toBe('function')
  })

  // An international challenge starts with no available SDK. The loader must
  // insert the configured international script URL so that site uses its
  // matching vendor implementation.
  it('loads the international SDK script for the international site', async () => {
    const wrapper = mount(TencentCaptchaGate, {
      props: { appId: '123456789', region: 'intl' },
    })
    void wrapper.vm.verify()
    await flushPromises()

    const script = document.head.querySelector<HTMLScriptElement>(
      'script[src*="TJNCaptcha-global.js"]',
    )
    expect(script?.src).toBe('https://ca.turing.captcha.qcloud.com/TJNCaptcha-global.js')
  })

  // The existing global belongs to the domestic SDK; international
  // initialization requires its own constructor signature and matching script.
  // A domestic SDK global exists when an international widget mounts. The
  // international script must still be requested because the constructors use
  // incompatible argument shapes.
  it('does not reuse a mainland global for the international site', async () => {
    window.TencentCaptcha = class {
      // constructor supplies an inert SDK instance for this initialization scenario.
      constructor() {}
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha

    const wrapper = mount(TencentCaptchaGate, {
      props: { appId: '123456789', region: 'intl' },
    })
    void wrapper.vm.verify()
    await flushPromises()

    expect(document.head.querySelector('script[src*="TJNCaptcha-global.js"]')).not.toBeNull()
  })

  // The loader has already completed a domestic script load on this page.
  // Requesting another region must reject and leave the international script
  // absent from the document.
  it('does not inject a second SDK when the region changes in one page', async () => {
    const constructor = class {
      // constructor supplies an inert SDK instance for this initialization scenario.
      constructor() {}
      show = vi.fn() // Vendor display call recorder.
      destroy = vi.fn() // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha

    const firstLoad = loadTencentCaptcha('cn')
    const firstScript = document.head.querySelector<HTMLScriptElement>(
      'script[src*="TJCaptcha.js"]',
    )
    expect(firstScript).not.toBeNull()
    window.TencentCaptcha = constructor
    firstScript?.dispatchEvent(new Event('load'))
    await expect(firstLoad).resolves.toBe(constructor)

    await expect(loadTencentCaptcha('intl')).rejects.toThrow(
      'Tencent Captcha region changed; reload the page to apply it',
    )
    expect(document.head.querySelector('script[src*="TJNCaptcha-global.js"]')).toBeNull()
  })

  // Reset occurs while a domestic verification promise is pending. The SDK
  // instance must be destroyed once and the waiting authentication call must
  // resolve cancellation.
  it('settles a pending verification when reset', async () => {
    const destroy = vi.fn()
    window.TencentCaptcha = class {
      // constructor supplies an inert SDK instance for this initialization scenario.
      constructor(_appId: string, _callback: (result: CaptchaResult) => void) {}
      show = vi.fn() // Vendor display call recorder.
      destroy = destroy // Vendor cleanup call recorder.
    }
    const wrapper = mount(TencentCaptchaGate, { props: { appId: '123456789' } })

    const verification = wrapper.vm.verify()
    await flushPromises()
    wrapper.vm.reset()

    await expect(verification).resolves.toBeNull()
    expect(destroy).toHaveBeenCalledOnce()
  })

  // A mounted international checkbox is reset before verification completes. Its
  // current instance must be destroyed and a replacement checkbox must
  // initialize and remain visible.
  it('reinitializes the international checkbox after reset', async () => {
    const destroy = vi.fn()
    let constructorCount = 0
    window.TCaptchaGlobal = true
    window.TencentCaptcha = class {
      // constructor records the initialized instance count for the reset assertion.
      constructor() {
        constructorCount += 1
      }
      show = vi.fn() // Vendor display call recorder.
      destroy = destroy // Vendor cleanup call recorder.
    } as unknown as typeof window.TencentCaptcha
    const wrapper = mount(TencentCaptchaGate, {
      props: { appId: '123456789', region: 'intl' },
    })

    await flushPromises()
    expect(constructorCount).toBe(1)
    wrapper.vm.reset()
    await flushPromises()

    expect(destroy).toHaveBeenCalledOnce()
    expect(constructorCount).toBe(2)
    expect(
      wrapper.get('[data-testid="tencent-captcha-international-container"]').classes(),
    ).not.toContain('scale-[0.01]')
  })
})
