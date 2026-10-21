import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAppStore } from '@/stores/app'
import { getPublicSettings } from '@/api/auth'
import type { PublicSettings } from '@/api/settings.types'
import { i18n } from '@/i18n'

// app.spec.ts verifies shared notifications, sidebar state, loading ownership, and
// locale-aware public settings caching. Fake timers drive toast dismissal and deferred
// promises expose overlapping settings requests. Fixtures vary language and browser
// injection independently to protect the settings cache decisions made before mounting.

// createDeferred exposes a promise and its completion controls for settings concurrency
// tests. The test resolves or rejects it after asserting that all overlapping callers
// are still waiting.
function createDeferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })

  return { promise, resolve, reject }
}

// createPublicSettings builds a complete public configuration fixture with caller-
// supplied overrides applied last. Each call creates fresh default arrays, while
// overrides are assigned shallowly and retain caller-supplied array or object identity.
// Tests reusing mutable overrides share those values across fixtures.
function createPublicSettings(overrides: Partial<PublicSettings> = {}): PublicSettings {
  return {
    locale: 'en',
    registration_enabled: false,
    email_verify_enabled: false,
    force_email_on_third_party_signup: false,
    registration_email_suffix_whitelist: [],
    registration_email_domain_quota_enabled: false,
    promo_code_enabled: true,
    password_reset_enabled: false,
    invitation_code_enabled: false,
    turnstile_enabled: false,
    turnstile_site_key: '',
    site_name: 'Test Site',
    site_logo: '',
    site_subtitle: '',
    api_base_url: '',
    contact_info: '',
    doc_url: '',
    home_content: '',
    hide_ccs_import_button: false,
    payment_enabled: false,
    risk_control_enabled: false,
    table_default_page_size: 20,
    table_page_size_options: [10, 20, 50, 100],
    custom_menu_items: [],
    custom_endpoints: [],
    linuxdo_oauth_enabled: false,
    wechat_oauth_enabled: false,
    oidc_oauth_enabled: false,
    oidc_oauth_provider_name: 'OIDC',
    github_oauth_enabled: false,
    google_oauth_enabled: false,
    backend_mode_enabled: false,
    version: '1.0.0',
    balance_low_notify_enabled: false,
    provider_quota_notify_enabled: false,
    balance_low_notify_threshold: 0,
    channel_monitor_enabled: true,
    channel_monitor_default_interval_seconds: 60,
    available_channels_enabled: false,
    service_quota_enabled: false,
    affiliate_enabled: false,
    ...overrides,
  }
}
vi.mock('@/api/admin/system', () => ({
  checkUpdates: vi.fn(),
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: vi.fn(),
}))

describe('useAppStore', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    i18n.global.locale.value = 'en'
    vi.useFakeTimers()
    localStorage.clear()
    vi.mocked(getPublicSettings).mockReset()
    // Remove prior injected configuration before each case so a cached browser value
    // cannot bypass the request behavior under test.
    delete (window as any).__APP_CONFIG__
  })

  afterEach(() => {
    vi.useRealTimers()
    localStorage.clear()
  })

  describe('Toast 消息管理', () => {
    // A success notification is added through the convenience action. The stored record
    // keeps its success type and original message, and returns a dismissal identifier.
    it('showSuccess 创建 success 类型 toast', () => {
      const store = useAppStore()
      const id = store.showSuccess('操作成功')

      expect(id).toMatch(/^toast-/)
      expect(store.toasts).toHaveLength(1)
      expect(store.toasts[0].type).toBe('success')
      expect(store.toasts[0].message).toBe('操作成功')
    })

    // An error notification is added through the convenience action. The stored record
    // retains the error type and supplied message for the global toast renderer.
    it('showError 创建 error 类型 toast', () => {
      const store = useAppStore()
      store.showError('出错了')

      expect(store.toasts).toHaveLength(1)
      expect(store.toasts[0].type).toBe('error')
      expect(store.toasts[0].message).toBe('出错了')
    })

    // A warning notification is added through the convenience action. The shared toast
    // list contains one warning record, protecting the helper's mapping to presentation
    // type.
    it('showWarning 创建 warning 类型 toast', () => {
      const store = useAppStore()
      store.showWarning('警告信息')

      expect(store.toasts).toHaveLength(1)
      expect(store.toasts[0].type).toBe('warning')
    })

    // An information notification is added through the convenience action. The shared
    // toast list contains one information record, protecting the helper's mapping to
    // presentation type.
    it('showInfo 创建 info 类型 toast', () => {
      const store = useAppStore()
      store.showInfo('提示信息')

      expect(store.toasts).toHaveLength(1)
      expect(store.toasts[0].type).toBe('info')
    })

    // A notification is created with an explicit three-second duration. Advancing the
    // fake clock to that deadline removes it from the shared toast list.
    it('toast 在指定 duration 后自动消失', () => {
      const store = useAppStore()
      store.showSuccess('临时消息', 3000)

      expect(store.toasts).toHaveLength(1)

      vi.advanceTimersByTime(3000)

      expect(store.toasts).toHaveLength(0)
    })

    // Two different notifications are active and one identifier is dismissed. The
    // matching record is removed while the other notification remains available to the
    // renderer.
    it('hideToast 移除指定 toast', () => {
      const store = useAppStore()
      const id = store.showSuccess('消息1')
      store.showError('消息2')

      expect(store.toasts).toHaveLength(2)

      store.hideToast(id)

      expect(store.toasts).toHaveLength(1)
      expect(store.toasts[0].type).toBe('error')
    })

    // The store contains several notifications with different presentation types.
    // Clearing all notifications empties the shared list regardless of each record's
    // type or dismissal timer.
    it('clearAllToasts 清除所有 toast', () => {
      const store = useAppStore()
      store.showSuccess('消息1')
      store.showError('消息2')
      store.showWarning('消息3')

      expect(store.toasts).toHaveLength(3)

      store.clearAllToasts()

      expect(store.toasts).toHaveLength(0)
    })

    // The toast list starts empty, receives a notification, and is cleared. The active-
    // toast selector follows those transitions so consumers observe current notification
    // presence.
    it('hasActiveToasts 正确反映 toast 状态', () => {
      const store = useAppStore()
      expect(store.hasActiveToasts).toBe(false)

      store.showSuccess('消息')
      expect(store.hasActiveToasts).toBe(true)

      store.clearAllToasts()
      expect(store.hasActiveToasts).toBe(false)
    })

    // Several notifications are created consecutively in one store instance. Their
    // returned identifiers differ, allowing later dismissal callbacks to address the
    // intended notification record.
    it('多个 toast 的 ID 唯一', () => {
      const store = useAppStore()
      const id1 = store.showSuccess('消息1')
      const id2 = store.showSuccess('消息2')
      const id3 = store.showSuccess('消息3')

      expect(id1).not.toBe(id2)
      expect(id2).not.toBe(id3)
    })
  })

  describe('侧边栏管理', () => {
    // The desktop sidebar starts expanded and is toggled twice. The shared collapsed
    // state follows both transitions and returns to its initial expanded value.
    it('toggleSidebar 切换折叠状态', () => {
      const store = useAppStore()
      expect(store.sidebarCollapsed).toBe(false)

      store.toggleSidebar()
      expect(store.sidebarCollapsed).toBe(true)

      store.toggleSidebar()
      expect(store.sidebarCollapsed).toBe(false)
    })

    // Callers assign the desktop sidebar collapse state directly. Both true and false
    // values are reflected by the store so layout consumers receive the supplied choice.
    it('setSidebarCollapsed 直接设置状态', () => {
      const store = useAppStore()

      store.setSidebarCollapsed(true)
      expect(store.sidebarCollapsed).toBe(true)

      store.setSidebarCollapsed(false)
      expect(store.sidebarCollapsed).toBe(false)
    })

    // The shared sidebar scroll position starts at zero. Consumers can write a
    // remembered offset and later reset it, preserving the writable restoration state.
    it('sidebarScrollTop 默认为 0 且可读写', () => {
      const store = useAppStore()
      expect(store.sidebarScrollTop).toBe(0)

      store.sidebarScrollTop = 256
      expect(store.sidebarScrollTop).toBe(256)

      store.sidebarScrollTop = 0
      expect(store.sidebarScrollTop).toBe(0)
    })

    // The mobile sidebar starts closed and is toggled twice. Shared visibility opens on
    // the first toggle and closes on the second toggle for layout consumers.
    it('toggleMobileSidebar 切换移动端状态', () => {
      const store = useAppStore()
      expect(store.mobileOpen).toBe(false)

      store.toggleMobileSidebar()
      expect(store.mobileOpen).toBe(true)

      store.toggleMobileSidebar()
      expect(store.mobileOpen).toBe(false)
    })
  })

  describe('Loading 状态管理', () => {
    // Two overlapping operations acquire the global loading indicator. Releasing one
    // contribution keeps loading visible until the second operation releases its
    // remaining contribution.
    it('setLoading 管理引用计数', () => {
      const store = useAppStore()
      expect(store.loading).toBe(false)

      store.setLoading(true)
      expect(store.loading).toBe(true)

      store.setLoading(true)
      expect(store.loading).toBe(true)

      store.setLoading(false)
      expect(store.loading).toBe(true)

      store.setLoading(false)
      expect(store.loading).toBe(false)
    })

    // Loading receives more releases than prior acquisitions. The counter stays
    // nonnegative, and a subsequent balanced acquire and release still updates the
    // indicator correctly.
    it('setLoading(false) 不会使计数为负', () => {
      const store = useAppStore()

      store.setLoading(false)
      store.setLoading(false)
      expect(store.loading).toBe(false)

      store.setLoading(true)
      expect(store.loading).toBe(true)

      store.setLoading(false)
      expect(store.loading).toBe(false)
    })

    // An asynchronous operation runs through the loading wrapper and succeeds. Loading
    // is visible inside the operation, its result reaches the caller, and completion
    // clears loading.
    it('withLoading 自动管理 loading 状态', async () => {
      const store = useAppStore()

      const result = await store.withLoading(async () => {
        expect(store.loading).toBe(true)
        return 'done'
      })

      expect(result).toBe('done')
      expect(store.loading).toBe(false)
    })

    // An asynchronous operation rejects inside the loading wrapper. The wrapper
    // propagates its error and still releases the loading contribution so the indicator
    // does not remain active.
    it('withLoading 错误时也恢复 loading 状态', async () => {
      const store = useAppStore()

      await expect(
        store.withLoading(async () => {
          throw new Error('操作失败')
        }),
      ).rejects.toThrow('操作失败')

      expect(store.loading).toBe(false)
    })

    // An asynchronous operation rejects inside the toast-reporting loading wrapper. The
    // caller receives null, loading clears, and an error notification is added to shared
    // state.
    it('withLoadingAndError 错误时显示 toast 并返回 null', async () => {
      const store = useAppStore()

      const result = await store.withLoadingAndError(async () => {
        throw new Error('网络错误')
      })

      expect(result).toBeNull()
      expect(store.loading).toBe(false)
      expect(store.toasts).toHaveLength(1)
      expect(store.toasts[0].type).toBe('error')
    })
  })

  describe('reset', () => {
    // The sidebar, loading indicator, and toast list contain changed presentation state.
    // Reset restores the checked defaults and removes notifications from the shared
    // application store.
    it('重置所有 UI 状态', () => {
      const store = useAppStore()

      store.setSidebarCollapsed(true)
      store.setLoading(true)
      store.showSuccess('消息')

      store.reset()

      expect(store.sidebarCollapsed).toBe(false)
      expect(store.loading).toBe(false)
      expect(store.toasts).toHaveLength(0)
    })
  })

  describe('公开设置加载', () => {
    // Ordinary and forced settings calls overlap one unresolved API request. All callers
    // wait for one response, which publishes feature availability and marks settings
    // loaded.
    it('并发调用复用并等待同一个请求，包括 force 调用', async () => {
      const deferred = createDeferred<PublicSettings>()
      vi.mocked(getPublicSettings).mockReturnValue(deferred.promise)
      const settings = createPublicSettings({ payment_enabled: true })
      const store = useAppStore()

      const first = store.fetchPublicSettings()
      const second = store.fetchPublicSettings()
      const forced = store.fetchPublicSettings(true)

      expect(getPublicSettings).toHaveBeenCalledTimes(1)

      const settled = vi.fn()
      void first.then(settled)
      void second.then(settled)
      void forced.then(settled)
      await Promise.resolve()
      expect(settled).not.toHaveBeenCalled()

      deferred.resolve(settings)
      await expect(Promise.all([first, second, forced])).resolves.toEqual([
        settings,
        settings,
        settings,
      ])
      expect(store.publicSettingsLoaded).toBe(true)
      expect(store.cachedPublicSettings?.payment_enabled).toBe(true)
    })

    // A forced settings refresh starts after initial settings have been cached. An
    // ordinary concurrent caller awaits the refresh result, and subsequent reads reuse
    // the replacement.
    it('force 在无活动请求时绕过缓存，刷新期间的普通调用等待刷新结果', async () => {
      const initial = createPublicSettings({ site_name: 'Initial Site' })
      vi.mocked(getPublicSettings).mockResolvedValueOnce(initial)
      const store = useAppStore()
      await store.fetchPublicSettings()

      const deferred = createDeferred<PublicSettings>()
      const updated = createPublicSettings({ site_name: 'Updated Site' })
      vi.mocked(getPublicSettings).mockReturnValueOnce(deferred.promise)

      const refresh = store.fetchPublicSettings(true)
      const duringRefresh = store.fetchPublicSettings()

      expect(getPublicSettings).toHaveBeenCalledTimes(2)

      deferred.resolve(updated)
      await expect(Promise.all([refresh, duringRefresh])).resolves.toEqual([updated, updated])
      expect(store.siteName).toBe('Updated Site')

      await expect(store.fetchPublicSettings()).resolves.toEqual(updated)
      expect(getPublicSettings).toHaveBeenCalledTimes(2)
    })

    // Two settings callers share an API request that rejects. Both resolve to null, and
    // the store keeps settings unloaded with no cached public configuration.
    it('并发请求失败时所有调用得到 null，且不会标记设置已加载', async () => {
      const deferred = createDeferred<PublicSettings>()
      vi.mocked(getPublicSettings).mockReturnValue(deferred.promise)
      const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined)
      const store = useAppStore()

      const first = store.fetchPublicSettings()
      const second = store.fetchPublicSettings()
      deferred.reject(new Error('network unavailable'))

      await expect(Promise.all([first, second])).resolves.toEqual([null, null])
      expect(getPublicSettings).toHaveBeenCalledTimes(1)
      expect(store.publicSettingsLoaded).toBe(false)
      expect(store.cachedPublicSettings).toBeNull()
      consoleError.mockRestore()
    })

    // Account restoration changes language after injected settings were applied. The
    // next read fetches translated configuration, updates branding and injection, then
    // reuses the matching cache.
    it('账户语言在组件挂载前恢复时，重新读取首屏配置', async () => {
      window.__APP_CONFIG__ = createPublicSettings({ locale: 'en', site_name: 'English site' })
      const store = useAppStore()
      store.initFromInjectedConfig()
      i18n.global.locale.value = 'zh-Hans'
      const translated = createPublicSettings({ locale: 'zh-Hans', site_name: '中文站点' })
      vi.mocked(getPublicSettings).mockResolvedValue(translated)

      await expect(store.fetchPublicSettings()).resolves.toEqual(translated)
      expect(store.siteName).toBe('中文站点')
      expect(window.__APP_CONFIG__?.locale).toBe('zh-Hans')
      await store.fetchPublicSettings()
      expect(getPublicSettings).toHaveBeenCalledTimes(1)
    })

    // Injected configuration has a different language from the current locale. The first
    // read requests current-language settings and publishes their site name instead of
    // using the injection.
    it('首次读取时跳过语言不匹配的注入配置', async () => {
      window.__APP_CONFIG__ = createPublicSettings({ locale: 'zh-Hans', site_name: '中文站点' })
      const english = createPublicSettings({ locale: 'en', site_name: 'English site' })
      vi.mocked(getPublicSettings).mockResolvedValue(english)
      const store = useAppStore()

      await expect(store.fetchPublicSettings()).resolves.toEqual(english)
      expect(getPublicSettings).toHaveBeenCalledTimes(1)
      expect(store.siteName).toBe('English site')
    })

    // Injected configuration already matches the active locale before the first read.
    // The store returns that configuration directly and avoids issuing a public-settings
    // API request.
    it('复用当前语言的注入配置', async () => {
      const settings = createPublicSettings()
      window.__APP_CONFIG__ = settings
      const store = useAppStore()

      await expect(store.fetchPublicSettings()).resolves.toEqual(settings)
      expect(getPublicSettings).not.toHaveBeenCalled()
    })

    // Browser-injected branding exists before store initialization. Synchronous
    // initialization publishes its name, logo, and version, returns true, and marks
    // public settings loaded for consumers.
    it('从 window.__APP_CONFIG__ 初始化', () => {
      const windowAny = window as any
      windowAny.__APP_CONFIG__ = {
        site_name: 'TestSite',
        site_logo: '/logo.png',
        version: '1.0.0',
        contact_info: 'test@test.com',
        api_base_url: 'https://api.test.com',
        doc_url: 'https://docs.test.com',
      }

      const store = useAppStore()
      const result = store.initFromInjectedConfig()

      expect(result).toBe(true)
      expect(store.siteName).toBe('TestSite')
      expect(store.siteLogo).toBe('/logo.png')
      expect(store.siteVersion).toBe('1.0.0')
      expect(store.publicSettingsLoaded).toBe(true)
    })

    // No browser-injected configuration exists during startup. Synchronous
    // initialization returns false and leaves the public-settings loaded marker unset so
    // normal loading can proceed.
    it('无注入配置时返回 false', () => {
      const store = useAppStore()
      const result = store.initFromInjectedConfig()

      expect(result).toBe(false)
      expect(store.publicSettingsLoaded).toBe(false)
    })

    // Public settings have been initialized from browser injection. Clearing the cache
    // removes the reactive record and loaded marker, exposing an unloaded state to
    // consumers.
    it('clearPublicSettingsCache 清除缓存', () => {
      const windowAny = window as any
      windowAny.__APP_CONFIG__ = { site_name: 'Test' }
      const store = useAppStore()
      store.initFromInjectedConfig()

      expect(store.publicSettingsLoaded).toBe(true)

      store.clearPublicSettingsCache()

      expect(store.publicSettingsLoaded).toBe(false)
      expect(store.cachedPublicSettings).toBeNull()
    })

    // A forced settings read returns updated table defaults and choices. The browser
    // injection receives those values while table preference storage keys remain
    // untouched by this settings operation.
    it('fetchPublicSettings(force) 会同步更新运行时注入配置', async () => {
      vi.mocked(getPublicSettings).mockResolvedValue({
        registration_enabled: false,
        email_verify_enabled: false,
        registration_email_suffix_whitelist: [],
        promo_code_enabled: true,
        password_reset_enabled: false,
        invitation_code_enabled: false,
        turnstile_enabled: false,
        turnstile_site_key: '',
        site_name: 'Updated Site',
        site_logo: '',
        site_subtitle: '',
        api_base_url: '',
        contact_info: '',
        doc_url: '',
        home_content: '',
        hide_ccs_import_button: false,
        purchase_subscription_enabled: false,
        purchase_subscription_url: '',
        table_default_page_size: 1000,
        table_page_size_options: [20, 100, 1000],
        custom_menu_items: [],
        custom_endpoints: [],
        linuxdo_oauth_enabled: false,
        backend_mode_enabled: false,
        version: '1.0.0',
      })

      const store = useAppStore()
      await store.fetchPublicSettings(true)

      expect((window as any).__APP_CONFIG__.table_default_page_size).toBe(1000)
      expect((window as any).__APP_CONFIG__.table_page_size_options).toEqual([20, 100, 1000])
      expect(localStorage.getItem('table-page-size')).toBeNull()
      expect(localStorage.getItem('table-page-size-source')).toBeNull()
    })
  })
})
