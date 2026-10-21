import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount } from '@vue/test-utils'
import AliyunCaptchaWidget from '../AliyunCaptchaWidget.vue'

// AliyunCaptchaWidget.spec.ts checks Aliyun challenge initialization and proof
// delivery through the component interface. A local vendor initializer and
// controlled timers reproduce completion, delayed popup appearance,
// cancellation, and reset without contacting the external captcha service.

// CapturedInitOptions records the vendor configuration and proof callback
// supplied by the mounted widget.
interface CapturedInitOptions {
  SceneId: string // Configured vendor scene identifier.
  prefix: string // Configured vendor deployment prefix.
  mode: string // Vendor challenge display mode.
  element: string // Widget host selector string.
  button: string // Widget trigger selector string.
  captchaVerifyCallback: (param: string) => {
    captchaResult: boolean /* Local verification success flag. */
  } // Captured proof publication callback.
  language?: string // Vendor interface language code.
}

// i18nStub installs template translation lookup while the composition
// translation mock provides SDK locale and trigger labels.
const i18nStub = {
  // install supplies a template translation helper for the mounted widget fixture.
  install(app: {
    config: {
      globalProperties: Record<string, unknown> /* Template global helper registry. */
    } /* Mounted application configuration object. */
  }) {
    app.config.globalProperties.$t = (key: string) => key
  },
}

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ locale: { value: 'zh-CN' }, t: (key: string) => key }),
}))

describe('AliyunCaptchaWidget', () => {
  let initOptions: CapturedInitOptions | null

  beforeEach(() => {
    initOptions = null
    window.initAliyunCaptcha = vi.fn((options: CapturedInitOptions) => {
      initOptions = options
    }) as unknown as typeof window.initAliyunCaptcha
  })

  afterEach(() => {
    vi.useRealTimers()
    delete window.initAliyunCaptcha
    delete window.AliyunCaptchaConfig
    document.getElementById('aliyunCaptcha-window-popup')?.remove()
    document.getElementById('aliyunCaptcha-mask')?.remove()
  })

  // mountWidget mounts the Aliyun fixture with local scene settings and
  // translations. The returned wrapper is attached to the document so the SDK
  // selector and popup polling paths can locate it.
  function mountWidget() {
    return mount(AliyunCaptchaWidget, {
      props: { sceneId: 'scene-1', prefix: 'prefix-1', region: 'cn' as const },
      attachTo: document.body,
      global: { plugins: [i18nStub] },
    })
  }

  // createVisiblePopup adds a visible vendor popup fixture to the document. The
  // caller removes it to simulate cancellation, while afterEach also removes any
  // remaining vendor popup elements.
  function createVisiblePopup(): HTMLElement {
    const popup = document.createElement('div')
    popup.id = 'aliyunCaptcha-window-popup'
    popup.style.display = 'block'
    document.body.appendChild(popup)
    return popup
  }

  // The widget mounts with configured scene, prefix, and Chinese region. It must
  // show a visible trigger and initialize popup mode after publishing the global
  // SDK configuration.
  it('渲染可见验证按钮并以 popup 模式初始化，全局配置就位', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const button = wrapper.get('button')
    expect(button.text()).toContain('auth.captchaClickToVerify')
    expect(window.AliyunCaptchaConfig).toEqual({ region: 'cn', prefix: 'prefix-1' })
    expect(initOptions).not.toBeNull()
    expect(initOptions!.mode).toBe('popup')
    expect(initOptions!.SceneId).toBe('scene-1')
    expect(initOptions!.language).toBe('cn')

    wrapper.unmount()
  })

  // The user clicks the trigger and the SDK supplies a verification parameter.
  // The component must emit that proof and present a disabled completed-
  // verification button.
  it('用户点击按钮进入验证中，验证完成后 emit verify 并置已通过', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    await wrapper.get('button').trigger('click')
    expect(wrapper.get('button').text()).toContain('auth.captchaVerifying')

    const result = initOptions!.captchaVerifyCallback('captcha-param-1')
    expect(result).toEqual({ captchaResult: true })
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('verify')).toEqual([['captcha-param-1']])
    expect(wrapper.get('button').text()).toContain('auth.captchaVerified')
    expect(wrapper.get('button').attributes('disabled')).toBeDefined()

    wrapper.unmount()
  })

  // The SDK finishes verification before the form requests an action proof.
  // Calling verify must resolve the cached parameter that the authentication
  // request will submit.
  it('verify() 在已预验证时直接复用缓存的 captchaVerifyParam', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    await wrapper.get('button').trigger('click')
    initOptions!.captchaVerifyCallback('captcha-param-2')

    const vm = wrapper.vm as unknown as {
      verify: () => Promise<string | null> /* Authentication proof verification callback. */
    }
    await expect(vm.verify()).resolves.toBe('captcha-param-2')

    wrapper.unmount()
  })

  // The form requests verification before the user completes a challenge. The
  // trigger must enter pending state and the returned promise must await the SDK
  // proof callback.
  it('verify() 在未预验证时触发弹窗流程并等待结果', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const vm = wrapper.vm as unknown as {
      verify: () => Promise<string | null> /* Authentication proof verification callback. */
    }
    const pending = vm.verify()
    await Promise.resolve()
    await Promise.resolve()
    await wrapper.vm.$nextTick()
    expect(wrapper.get('button').text()).toContain('auth.captchaVerifying')

    initOptions!.captchaVerifyCallback('captcha-param-3')
    await expect(pending).resolves.toBe('captcha-param-3')

    wrapper.unmount()
  })

  // The SDK has not yet made its popup visible after programmatic verification.
  // Polling must retry the button click so delayed vendor listener registration
  // can still open it.
  it('弹窗未出现前会按 tick 重试触发按钮（SDK 异步绑定兜底）', async () => {
    vi.useFakeTimers()
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const vm = wrapper.vm as unknown as {
      verify: () => Promise<string | null> /* Authentication proof verification callback. */
    }
    void vm.verify()
    await Promise.resolve()
    await Promise.resolve()

    const button = wrapper.get('button').element as HTMLButtonElement
    const clickSpy = vi.fn()
    button.addEventListener('click', clickSpy)

    await vi.advanceTimersByTimeAsync(1000)
    expect(clickSpy.mock.calls.length).toBeGreaterThanOrEqual(3)

    button.removeEventListener('click', clickSpy)
    wrapper.unmount()
  })

  // A popup becomes visible and then disappears before delivering a proof. The
  // waiting form must receive null and the trigger must return to its idle
  // label.
  it('弹窗出现后被用户关闭时 resolve null 并回到未验证态', async () => {
    vi.useFakeTimers()
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    const vm = wrapper.vm as unknown as {
      verify: () => Promise<string | null> /* Authentication proof verification callback. */
    }
    const pending = vm.verify()
    await Promise.resolve()
    await Promise.resolve()

    const popup = createVisiblePopup()
    await vi.advanceTimersByTimeAsync(400)
    popup.remove()
    await vi.advanceTimersByTimeAsync(400)

    await expect(pending).resolves.toBeNull()
    expect(wrapper.get('button').text()).toContain('auth.captchaClickToVerify')

    wrapper.unmount()
  })

  // A completed proof is followed by reset and another verification request. The
  // next request must wait for a new parameter instead of returning the cleared
  // cached result.
  it('reset() 清空缓存并取消进行中的验证', async () => {
    const wrapper = mountWidget()
    await Promise.resolve()
    await Promise.resolve()

    await wrapper.get('button').trigger('click')
    initOptions!.captchaVerifyCallback('captcha-param-4')

    const vm = wrapper.vm as unknown as {
      verify: () => Promise<string | null> /* Authentication proof verification callback. */
      reset: () => void /* Reset current verification state. */
    }
    vm.reset()
    await wrapper.vm.$nextTick()
    expect(wrapper.get('button').text()).toContain('auth.captchaClickToVerify')

    // Reset discards the prior parameter, so this verification waits for a new
    // SDK callback before returning proof to the form.
    const pending = vm.verify()
    await Promise.resolve()
    initOptions!.captchaVerifyCallback('captcha-param-5')
    await expect(pending).resolves.toBe('captcha-param-5')

    wrapper.unmount()
  })
})

declare global {
  // Window exposes the optional Aliyun SDK hooks used by these local browser
  // fixtures.
  interface Window {
    initAliyunCaptcha?: (options: unknown) => void /* Loaded vendor initialization callback. */
    AliyunCaptchaConfig?: {
      region: string // Vendor SDK regional setting.
      prefix: string // Vendor deployment prefix value.
    } /* Settings read before SDK initialization. */
  }
}
