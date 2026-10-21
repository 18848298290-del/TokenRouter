import { mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import WechatOAuthSection from '@/components/auth/WechatOAuthSection.vue'
import { useAppStore } from '@/stores'
import type { PublicSettings } from '@/api/settings.types'

// WechatOAuthSection.spec.ts checks WeChat start requests against configured modes
// and browser context. Public settings are seeded into the app store, and
// user-agent fixtures distinguish external-browser and WeChat-browser availability
// from the legacy aggregate enabled flag.

// These fixtures provide controllable dependencies and component handles for the
// cases below. Each test configures the responses it needs and inspects calls or
// rendered state at that boundary.
const routeState = vi.hoisted(() => ({
  query: {} as Record<string, unknown>,
})) // test-controlled route state fixture

let pinia: ReturnType<typeof createPinia> // test-controlled pinia fixture

vi.mock('vue-router', () => ({
  // useRoute supplies the callback query fixture read by the component.
  useRoute: () => routeState,
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    // useI18n supplies the translation fixture used by rendered authentication
    // labels.
    useI18n: () => ({
      locale: { value: 'en' },
      // t resolves the translation keys needed by this test fixture. Unhandled
      // keys remain visible as their identifiers so assertions can inspect copy
      // selection without loading locale catalogs.
      t: (key: string, params?: Record<string, string>) => {
        if (key === 'auth.wechatProviderName') {
          return 'Mock WeChat'
        }
        if (key === 'auth.oidc.signIn') {
          return `Continue with ${params?.providerName ?? ''}`.trim()
        }
        if (key === 'auth.oauthFlow.wechatSystemBrowserOnly') {
          return 'MOCK-SYSTEM-BROWSER-ONLY'
        }
        if (key === 'auth.oauthFlow.wechatBrowserOnly') {
          return 'MOCK-WECHAT-BROWSER-ONLY'
        }
        if (key === 'auth.oauthFlow.wechatNotConfigured') {
          return 'MOCK-NOT-CONFIGURED'
        }
        if (key === 'auth.oauthOrContinue') {
          return 'or continue'
        }
        return key
      },
    }),
  }
})

// WeChatPublicSettings extends public settings with mode-specific WeChat
// capabilities for browser tests.
type WeChatPublicSettings = PublicSettings & {
  // wechat_oauth_open_enabled supplies an explicit browser-mode capability when
  // present. Omitting it lets the fixture exercise legacy aggregate settings
  // instead of confirmed per-mode configuration.
  wechat_oauth_open_enabled?: boolean // external-browser WeChat availability

  // wechat_oauth_mp_enabled supplies an explicit browser-mode capability when
  // present. Omitting it lets the fixture exercise legacy aggregate settings
  // instead of confirmed per-mode configuration.
  wechat_oauth_mp_enabled?: boolean // WeChat-browser authentication availability
}

// buildPublicSettings creates the baseline WeChat browser settings for this suite.
// Caller overrides are applied last, allowing each test to isolate per-mode flags
// or legacy aggregate availability.
function buildPublicSettings(overrides: Partial<WeChatPublicSettings> = {}): WeChatPublicSettings {
  return {
    registration_enabled: true,
    email_verify_enabled: false,
    force_email_on_third_party_signup: false,
    registration_email_suffix_whitelist: [],
    promo_code_enabled: true,
    password_reset_enabled: false,
    invitation_code_enabled: false,
    turnstile_enabled: false,
    turnstile_site_key: '',
    site_name: 'TokenRouter',
    site_logo: '',
    site_subtitle: '',
    api_base_url: '/api/v1',
    contact_info: '',
    doc_url: '',
    home_content: '',
    hide_ccs_import_button: false,
    payment_enabled: false,
    table_default_page_size: 20,
    table_page_size_options: [10, 20, 50, 100],
    custom_menu_items: [],
    custom_endpoints: [],
    linuxdo_oauth_enabled: false,
    wechat_oauth_enabled: true,
    oidc_oauth_enabled: false,
    oidc_oauth_provider_name: 'OIDC',
    backend_mode_enabled: false,
    version: 'test',
    balance_low_notify_enabled: false,
    account_quota_notify_enabled: false,
    balance_low_notify_threshold: 0,
    ...overrides,
  }
}

// seedPublicSettings installs the constructed settings in the active app store. It
// also marks loading complete so the mounted provider component uses the fixture
// instead of fetching settings.
function seedPublicSettings(overrides: Partial<WeChatPublicSettings> = {}): void {
  const appStore = useAppStore()
  const settings = buildPublicSettings(overrides)
  appStore.cachedPublicSettings = settings
  appStore.publicSettingsLoaded = true
}

describe('WechatOAuthSection', () => {
  // Reset the dependency fixtures before each case so request histories and
  // configured responses belong to the scenario being asserted in that case.
  // Install a fresh active Pinia instance before mounting store consumers.
  beforeEach(() => {
    pinia = createPinia()
    setActivePinia(pinia)
    routeState.query = { redirect: '/billing?plan=pro' }
    Object.defineProperty(window.navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0',
    })
  })

  // Restore the browser or timer fixtures changed by this case after its
  // assertions, keeping those process-level replacements from affecting the next
  // authentication scenario.
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  // The settings fixture enables WeChat open mode in an external browser. The
  // button must emit that mode and the current redirect alongside the localized
  // provider label.
  it('starts the open WeChat OAuth flow with the current redirect target when open mode is configured', async () => {
    seedPublicSettings({
      wechat_oauth_open_enabled: true,
      wechat_oauth_mp_enabled: false,
    })
    const wrapper = mount(WechatOAuthSection, {
      global: {
        plugins: [pinia],
      },
    })

    expect(wrapper.text()).toContain('Mock WeChat')

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')?.[0]?.[0]).toEqual({
      provider: 'wechat',
      params: { mode: 'open', redirect: '/billing?plan=pro' },
    })
  })

  // The user-agent fixture identifies the WeChat browser and settings enable mp
  // mode. The emitted start request must select mp with the configured redirect
  // destination.
  it('uses mp mode inside the WeChat browser when mp mode is configured', async () => {
    Object.defineProperty(window.navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 MicroMessenger',
    })
    seedPublicSettings({
      wechat_oauth_open_enabled: false,
      wechat_oauth_mp_enabled: true,
    })
    const wrapper = mount(WechatOAuthSection, {
      global: {
        plugins: [pinia],
      },
    })

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')?.[0]?.[0]).toEqual({
      provider: 'wechat',
      params: { mode: 'mp', redirect: '/billing?plan=pro' },
    })
  })

  // Only mp mode is configured while the browser is external. The button must
  // remain disabled, explain the required browser, and emit no start event.
  it('disables the button outside the WeChat browser when only mp mode is configured', async () => {
    seedPublicSettings({
      wechat_oauth_open_enabled: false,
      wechat_oauth_mp_enabled: true,
    })
    const wrapper = mount(WechatOAuthSection, {
      global: {
        plugins: [pinia],
      },
    })

    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('MOCK-WECHAT-BROWSER-ONLY')

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')).toBeUndefined()
  })

  // Only open mode is configured while the user-agent identifies WeChat. The
  // button must remain disabled, explain the external-browser requirement, and
  // emit no start event.
  it('disables the button inside the WeChat browser when only open mode is configured', async () => {
    Object.defineProperty(window.navigator, 'userAgent', {
      configurable: true,
      value: 'Mozilla/5.0 MicroMessenger',
    })
    seedPublicSettings({
      wechat_oauth_open_enabled: true,
      wechat_oauth_mp_enabled: false,
    })
    const wrapper = mount(WechatOAuthSection, {
      global: {
        plugins: [pinia],
      },
    })

    expect(wrapper.get('button').attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('MOCK-SYSTEM-BROWSER-ONLY')

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')).toBeUndefined()
  })

  // The settings fixture supplies only the legacy aggregate WeChat flag. The
  // component must still emit the compatible start request selected for that
  // browser fixture.
  it('uses the legacy overall enabled flag when per-mode settings are not present', async () => {
    seedPublicSettings({
      wechat_oauth_enabled: true,
    })
    const wrapper = mount(WechatOAuthSection, {
      global: {
        plugins: [pinia],
      },
    })

    await wrapper.get('button').trigger('click')

    expect(wrapper.emitted('start')?.[0]?.[0]).toEqual({
      provider: 'wechat',
      params: { mode: 'open', redirect: '/billing?plan=pro' },
    })
  })

  // The settings fixture disables available WeChat login modes. The component must
  // display the translation fixture's not-configured hint so unavailable
  // authorization has an observable explanation.
  it('shows the localized not-configured hint when WeChat OAuth is unavailable', async () => {
    seedPublicSettings({
      wechat_oauth_enabled: false,
      wechat_oauth_open_enabled: false,
      wechat_oauth_mp_enabled: false,
    })

    const wrapper = mount(WechatOAuthSection, {
      global: {
        plugins: [pinia],
      },
    })

    expect(wrapper.text()).toContain('MOCK-NOT-CONFIGURED')
  })
})
