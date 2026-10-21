import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import type { Toast, ToastType } from '@/types/ui'
import type { PublicSettings } from '@/api/settings.types'
import { getLocale, i18n } from '@/i18n'
import {
  checkUpdates as checkUpdatesAPI,
  type VersionInfo,
  type ReleaseInfo,
} from '@/api/admin/system'
import { getPublicSettings as fetchPublicSettingsAPI } from '@/api/auth'

// app.ts manages shared layout state, toast notifications, release information, and
// public site settings. Loading uses a reference count so overlapping operations can
// release their own contribution. Toast identifiers increase across resets, letting
// delayed dismissals address the notification that created them.
//
// Public settings requests share one active promise, including forced refreshes.
// Completed settings are cached by locale and copied into the browser's injected
// configuration for later consumers. Clearing cached settings does not cancel a running
// request, which can still publish its result. Version requests use a separate busy flag
// and return null to callers that need a request while another version read is busy.

// useAppStore exposes application presentation state and shared settings caches to Pinia
// consumers. Install Pinia before requesting the store; settings initialization and
// remote loading remain separate actions controlled by application startup.
export const useAppStore = defineStore('app', () => {
  // Layout refs belong to the application shell and retain shared sidebar, loading, and
  // notification state across route component changes.
  const sidebarCollapsed = ref<boolean>(false)
  const mobileOpen = ref<boolean>(false)
  const sidebarScrollTop = ref<number>(0)
  const loading = ref<boolean>(false)
  const toasts = ref<Toast[]>([])

  // Public settings refs hold branding and locale-tagged configuration;
  // publicSettingsRequest owns the single active read shared by normal and forced
  // callers.
  const publicSettingsLoaded = ref<boolean>(false)
  const publicSettingsLoading = ref<boolean>(false)
  const defaultSiteName = ref<string>('TokenRouter')
  const siteLogo = ref<string>('')
  const siteVersion = ref<string>('')
  const contactInfo = ref<string>('')
  const apiBaseUrl = ref<string>('')
  const docUrl = ref<string>('')
  const cachedPublicSettings = ref<PublicSettings | null>(null)
  let publicSettingsRequest: Promise<PublicSettings | null> | null = null

  // Version refs retain the last successful release response and track whether callers
  // can reuse it or a request is currently active.
  const versionLoaded = ref<boolean>(false)
  const versionLoading = ref<boolean>(false)
  const currentVersion = ref<string>('')
  const latestVersion = ref<string>('')
  const hasUpdate = ref<boolean>(false)
  const buildType = ref<string>('source')
  const releaseInfo = ref<ReleaseInfo | null>(null)

  // toastIdCounter increases across resets so a delayed toast dismissal cannot
  // accidentally address a later notification.
  let toastIdCounter = 0

  // Presentation selectors derive notification visibility, backend restrictions, and
  // site branding from the current shared settings and notification records.
  const hasActiveToasts = computed(() => toasts.value.length > 0)
  const backendModeEnabled = computed(
    () => cachedPublicSettings.value?.backend_mode_enabled ?? false,
  )
  const siteName = computed(
    () => cachedPublicSettings.value?.site_name || defaultSiteName.value || 'TokenRouter',
  )

  // loadingCount records outstanding loading contributions; reset clears it and ordinary
  // releases clamp at zero to keep excess cleanup harmless.
  const loadingCount = ref<number>(0)

  // toggleSidebar toggles the shared desktop sidebar between collapsed and expanded
  // states.
  function toggleSidebar(): void {
    sidebarCollapsed.value = !sidebarCollapsed.value
  }

  // setSidebarCollapsed assigns the shared desktop sidebar collapse state from the
  // supplied flag.
  function setSidebarCollapsed(collapsed: boolean): void {
    sidebarCollapsed.value = collapsed
  }

  // toggleMobileSidebar toggles the shared mobile sidebar between open and closed
  // states.
  function toggleMobileSidebar(): void {
    mobileOpen.value = !mobileOpen.value
  }

  // setMobileOpen assigns mobile sidebar visibility from the supplied open flag.
  function setMobileOpen(open: boolean): void {
    mobileOpen.value = open
  }

  // setLoading acquires or releases one contribution to the shared loading indicator.
  // Callers pair true with false for each operation; excess releases clamp the counter
  // to zero and cannot make it negative.
  function setLoading(isLoading: boolean): void {
    if (isLoading) {
      loadingCount.value++
    } else {
      loadingCount.value = Math.max(0, loadingCount.value - 1)
    }
    loading.value = loadingCount.value > 0
  }

  // showToast appends a notification and returns its identifier for manual dismissal. A
  // supplied duration schedules removal in milliseconds, while undefined keeps the
  // notification until callers dismiss or clear it.
  function showToast(type: ToastType, message: string, duration?: number): string {
    const id = `toast-${++toastIdCounter}`
    const toast: Toast = {
      id,
      type,
      message,
      duration,
    }

    toasts.value.push(toast)
    if (duration !== undefined) {
      // Each timeout retains its own notification identifier, so later removal is
      // harmless when manual dismissal or reset has already cleared that notification.
      setTimeout(() => {
        hideToast(id)
      }, duration)
    }

    return id
  }

  // showSuccess creates a success notification and returns its dismissal identifier. The
  // default duration is 3000 milliseconds; callers can pass another duration for the
  // shared toast timer.
  function showSuccess(message: string, duration: number = 3000): string {
    return showToast('success', message, duration)
  }

  // showError creates an error notification and returns its dismissal identifier. The
  // default duration is 5000 milliseconds; callers can pass another duration for the
  // shared toast timer.
  function showError(message: string, duration: number = 5000): string {
    return showToast('error', message, duration)
  }

  // showInfo creates an info notification and returns its dismissal identifier. The
  // default duration is 3000 milliseconds; callers can pass another duration for the
  // shared toast timer.
  function showInfo(message: string, duration: number = 3000): string {
    return showToast('info', message, duration)
  }

  // showWarning creates a warning notification and returns its dismissal identifier. The
  // default duration is 4000 milliseconds; callers can pass another duration for the
  // shared toast timer.
  function showWarning(message: string, duration: number = 4000): string {
    return showToast('warning', message, duration)
  }

  // hideToast removes the notification with the supplied identifier when it exists.
  function hideToast(id: string): void {
    const index = toasts.value.findIndex((t) => t.id === id)
    if (index !== -1) {
      toasts.value.splice(index, 1)
    }
  }

  // clearAllToasts removes every active notification from the shared toast list.
  function clearAllToasts(): void {
    toasts.value = []
  }

  // withLoading runs an asynchronous operation while contributing to the shared loading
  // indicator. It returns the operation result or propagates its error, releasing its
  // loading contribution in either case.
  async function withLoading<T>(operation: () => Promise<T>): Promise<T> {
    setLoading(true)
    try {
      return await operation()
    } finally {
      setLoading(false)
    }
  }

  // withLoadingAndError runs an asynchronous operation and reports failures through an
  // error toast. It returns null after failure, preferring an explicit message, then the
  // error message, then the translated unknown-error text; loading is released
  // afterward.
  async function withLoadingAndError<T>(
    operation: () => Promise<T>,
    errorMessage?: string,
  ): Promise<T | null> {
    setLoading(true)
    try {
      return await operation()
    } catch (error) {
      // Convert a rejected operation to a toast and null result, preferring caller text
      // before server detail and the translated unknown-error message.
      const message =
        errorMessage ||
        (error as { message?: string }).message ||
        i18n.global.t('common.unknownError')
      showError(message)
      return null
    } finally {
      setLoading(false)
    }
  }

  // reset restores sidebar collapse, loading count, and toast state to initial values.
  // Public settings, version information, mobile visibility, and sidebar scroll position
  // are retained for their separate consumers.
  function reset(): void {
    sidebarCollapsed.value = false
    loading.value = false
    loadingCount.value = 0
    toasts.value = []
  }

  // fetchVersion returns cached release data for an ordinary read after a successful
  // load. Cache admission precedes the busy check, so ordinary callers can receive a
  // cached snapshot during a forced refresh. Force bypasses that cache check and is
  // forwarded to the API, but any caller reaching the busy check returns null while a
  // request is active.
  //
  // A successful request publishes release fields and marks the cache loaded before
  // returning its response. Failure logs the error and returns null while retaining
  // previous cache contents and loaded state. Request settlement releases the busy
  // flag for a later caller.
  async function fetchVersion(force = false): Promise<VersionInfo | null> {
    if (versionLoaded.value && !force) {
      return {
        current_version: currentVersion.value,
        latest_version: latestVersion.value,
        has_update: hasUpdate.value,
        build_type: buildType.value,
        release_info: releaseInfo.value || undefined,
        cached: true,
      }
    }
    if (versionLoading.value) {
      return null
    }

    versionLoading.value = true
    try {
      const data = await checkUpdatesAPI(force)
      currentVersion.value = data.current_version
      latestVersion.value = data.latest_version
      hasUpdate.value = data.has_update
      buildType.value = data.build_type || 'source'
      releaseInfo.value = data.release_info || null
      versionLoaded.value = true
      return data
    } catch (error) {
      // Retain the last displayed release details on request failure and return null,
      // allowing callers to distinguish failure from cached success.
      console.error('Failed to fetch version:', error)
      return null
    } finally {
      versionLoading.value = false
    }
  }

  // clearVersionCache invalidates release freshness and clears the update-available
  // flag. Cached version strings and release details remain available until a later
  // successful fetch replaces those values for consumers.
  function clearVersionCache(): void {
    // Invalidate the release freshness marker while retaining displayed version details,
    // so the next idle read fetches a replacement without clearing current labels.
    versionLoaded.value = false
    hasUpdate.value = false
  }

  // applySettings publishes public configuration to the injected browser object and
  // reactive store. The injected value is a shallow copy; cachedPublicSettings retains
  // the supplied object, and missing branding fields receive display defaults before
  // loaded becomes true.
  function applySettings(config: PublicSettings): void {
    if (typeof window !== 'undefined') {
      window.__APP_CONFIG__ = { ...config }
    }
    cachedPublicSettings.value = config
    defaultSiteName.value = config.site_name || 'TokenRouter'
    siteLogo.value = config.site_logo || ''
    siteVersion.value = config.version || ''
    contactInfo.value = config.contact_info || ''
    apiBaseUrl.value = config.api_base_url || ''
    docUrl.value = config.doc_url || ''
    publicSettingsLoaded.value = true
  }

  // fetchPublicSettings returns public configuration for the current locale and shares
  // any active request. An active promise wins even over force, leaving locale retry
  // handling to the API layer. When idle, ordinary reads reuse matching injected or
  // stored settings, while force starts a request. Success publishes configuration
  // before returning it. Synchronous and asynchronous request failures resolve to null;
  // promise identity prevents completion from clearing another request's busy state.
  function fetchPublicSettings(force = false): Promise<PublicSettings | null> {
    // Joining the active request precedes force and locale-cache checks, so all
    // concurrent callers wait for the same API-layer locale handling.
    if (publicSettingsRequest) {
      return publicSettingsRequest
    }

    const language = getLocale()
    if (!publicSettingsLoaded.value && !force && window.__APP_CONFIG__?.locale === language) {
      // Reuse injected settings only when their locale matches the active language;
      // otherwise a network read supplies translated configuration.
      applySettings(window.__APP_CONFIG__)
      return Promise.resolve(window.__APP_CONFIG__)
    }

    if (publicSettingsLoaded.value && !force && cachedPublicSettings.value?.locale === language) {
      return Promise.resolve({ ...cachedPublicSettings.value })
    }

    // Mark the public-settings request active before invoking the API, and clear that
    // flag even when the API throws synchronously.
    publicSettingsLoading.value = true
    let apiRequest: Promise<PublicSettings>
    try {
      apiRequest = fetchPublicSettingsAPI()
    } catch (error) {
      console.error('Failed to fetch public settings:', error)
      publicSettingsLoading.value = false
      return Promise.resolve(null)
    }

    const request = apiRequest
      // Successful public settings publish to both reactive state and browser
      // configuration before the shared promise resolves to its consumers.
      .then((data) => {
        applySettings(data)
        return data
      })
      .catch((error) => {
        // Preserve the last successfully published configuration when the request fails,
        // and resolve null so all joined callers observe the same unavailable result.
        console.error('Failed to fetch public settings:', error)
        return null
      })
      .finally(() => {
        // Only the promise still recorded as active clears request ownership, preventing
        // an older completion from releasing another request's busy state.
        if (publicSettingsRequest === request) {
          publicSettingsRequest = null
          publicSettingsLoading.value = false
        }
      })

    publicSettingsRequest = request
    return request
  }

  // clearPublicSettingsCache clears the reactive settings record and its loaded marker.
  // It retains injected configuration and active work, so a later read or pending
  // response can populate the store again.
  function clearPublicSettingsCache(): void {
    // Clear cached public configuration for future reads; injected settings and an
    // already active request retain their separate ability to repopulate it.
    publicSettingsLoaded.value = false
    cachedPublicSettings.value = null
  }

  // initFromInjectedConfig applies browser-injected public settings synchronously and
  // reports whether they existed. Startup calls it after installing Pinia and before
  // mounting, with locale freshness checked separately during bootstrap.
  function initFromInjectedConfig(): boolean {
    if (window.__APP_CONFIG__) {
      applySettings(window.__APP_CONFIG__)
      return true
    }
    return false
  }

  return {
    sidebarCollapsed,
    mobileOpen,
    sidebarScrollTop,
    loading,
    toasts,
    publicSettingsLoaded,
    siteName,
    siteLogo,
    siteVersion,
    contactInfo,
    apiBaseUrl,
    docUrl,
    cachedPublicSettings,
    versionLoaded,
    versionLoading,
    currentVersion,
    latestVersion,
    hasUpdate,
    buildType,
    releaseInfo,
    hasActiveToasts,
    backendModeEnabled,
    toggleSidebar,
    setSidebarCollapsed,
    toggleMobileSidebar,
    setMobileOpen,
    setLoading,
    showToast,
    showSuccess,
    showError,
    showInfo,
    showWarning,
    hideToast,
    clearAllToasts,
    withLoading,
    withLoadingAndError,
    reset,
    fetchVersion,
    clearVersionCache,
    fetchPublicSettings,
    clearPublicSettingsCache,
    initFromInjectedConfig,
  }
})
