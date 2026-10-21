import { ref, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api'
import type {
  WebSearchEmulationConfig,
  WebSearchProviderConfig,
  WebSearchTestResult,
} from '@/api/admin/settings'
import type { Proxy } from '@/api/admin/proxies.types'
import { extractApiErrorMessage } from '@/utils/apiError'
import { useAppStore } from '@/stores'

/**
 * useWebSearchSettings.ts owns search emulation provider drafts and the settings page
 * probe dialog. Configuration uses a dedicated endpoint, while the page coordinator
 * determines its position after the bulk save. Provider secrets pass through with
 * their configured flags; this module normalizes quota limits while leaving secret
 * interpretation to the API.
 *
 * Expansion and key visibility use provider array indices, so deleting a provider
 * remaps both maps to the remaining rows. Subscription dates round-trip through UTC
 * midnight to keep calendar dates stable across browser timezones. Proxy lookup
 * failure becomes an empty selector list and does not prevent the configuration
 * request from loading.
 */

/**
 * useWebSearchSettings creates the page-owned search draft and actions during
 * component setup. The caller schedules initial loading and persistence; the returned
 * state survives category changes while the settings page stays mounted.
 */
export function useWebSearchSettings() {
  // Translation and notification services belong to this setup invocation and serve all returned actions.
  const { t } = useI18n()
  const appStore = useAppStore()

  // ProxySelector consumes this list after configuration loading; a failed proxy request supplies an empty list.
  const webSearchProxies = ref<Proxy[]>([])

  // New provider rows start with this request limit; a saved null limit represents unlimited usage.
  const DEFAULT_WEB_SEARCH_QUOTA_LIMIT = 1000

  // Configuration is an editable draft shared with the provider editor and serialized by saveWebSearchConfig.
  const webSearchConfig = reactive<WebSearchEmulationConfig>({
    enabled: false, // Global search emulation switch.
    providers: [], // Ordered provider configuration drafts.
  })

  // expandedProviders and apiKeyVisible share current provider-array indices; removal remaps both
  // together so another provider never inherits the deleted row's open or secret-visible state.
  const expandedProviders = reactive<Record<number, boolean>>({})

  const apiKeyVisible = reactive<Record<number, boolean>>({})

  // Probe state belongs to the dialog, with results cleared on reopening or starting a new request.
  const wsTestQuery = ref('')

  const wsTestLoading = ref(false)

  const wsTestResult = ref<WebSearchTestResult | null>(null)

  const wsTestDialogOpen = ref(false)

  /**
   * openTestDialog opens the probe dialog after clearing its previous results.
   */
  function openTestDialog() {
    wsTestResult.value = null
    wsTestDialogOpen.value = true
  }

  /**
   * toggleProviderExpand reverses the detail visibility for one provider row.
   */
  function toggleProviderExpand(idx: number) {
    expandedProviders[idx] = !expandedProviders[idx]
  }

  /**
   * removeWebSearchProvider deletes one draft provider and remaps row-owned presentation
   * state. Callers pass an index from the rendered provider list. Both expansion and API
   * key visibility are keyed by that index, so subsequent rows inherit their preceding
   * indices while retaining their own flags. The maps are updated in place because the
   * template shares their reactive identities, and absent flags become false for the
   * remaining rows.
   */
  function removeWebSearchProvider(idx: number) {
    webSearchConfig.providers.splice(idx, 1)
    // Removing a row shifts every following index; remap both presentation maps before
    // publishing them so expansion and key visibility continue to belong to each surviving provider.
    const newExpanded: Record<number, boolean> = {}
    const newVisible: Record<number, boolean> = {}
    for (let i = 0; i < webSearchConfig.providers.length; i++) {
      const oldIdx = i >= idx ? i + 1 : i
      newExpanded[i] = expandedProviders[oldIdx] ?? false
      newVisible[i] = apiKeyVisible[oldIdx] ?? false
    }
    Object.keys(expandedProviders).forEach((k) => delete expandedProviders[Number(k)])
    Object.keys(apiKeyVisible).forEach((k) => delete apiKeyVisible[Number(k)])
    Object.assign(expandedProviders, newExpanded)
    Object.assign(apiKeyVisible, newVisible)
  }

  /**
   * addWebSearchProvider appends an expanded Brave provider with an empty unconfigured
   * key. The draft starts with the standard quota limit and unset subscription, proxy,
   * and expiry values for subsequent editing.
   */
  function addWebSearchProvider() {
    const idx = webSearchConfig.providers.length
    webSearchConfig.providers.push({
      type: 'brave',
      api_key: '',
      api_key_configured: false,
      quota_limit: DEFAULT_WEB_SEARCH_QUOTA_LIMIT,
      subscribed_at: null,
      proxy_id: null,
      expires_at: null,
    } as WebSearchProviderConfig)
    expandedProviders[idx] = true
  }

  /**
   * formatSubscribedAt converts epoch seconds into the UTC calendar date displayed by
   * the subscription input. Missing or zero timestamps produce an empty field. The
   * formatter reads UTC components because parseSubscribedAt creates UTC midnight
   * timestamps; using local components could move that same timestamp to the previous
   * day in a negative timezone. Date inputs discard time-of-day precision, so this
   * function exposes only year, month, and day.
   */
  function formatSubscribedAt(ts: number | null): string {
    if (!ts) return ''
    // The input edits calendar days; UTC accessors match parseSubscribedAt so reading and
    // resaving a date cannot move it to the previous day in another timezone.
    const d = new Date(ts * 1000)
    const y = d.getUTCFullYear()
    const m = String(d.getUTCMonth() + 1).padStart(2, '0')
    const day = String(d.getUTCDate()).padStart(2, '0')
    return `${y}-${m}-${day}`
  }

  /**
   * parseSubscribedAt turns a subscription date input into UTC epoch seconds, or null
   * for an empty field. It appends a UTC midnight time before constructing the Date.
   * This matches formatSubscribedAt and keeps a saved calendar day stable when another
   * administrator uses a different browser timezone. Milliseconds are divided and
   * floored for the endpoint seconds representation; the date input supplies the
   * expected calendar-date text.
   */
  function parseSubscribedAt(dateStr: string): number | null {
    if (!dateStr) return null
    // Append UTC midnight before converting milliseconds to seconds so the API timestamp
    // round-trips through formatSubscribedAt independently of the browser's local timezone.
    return Math.floor(new Date(dateStr + 'T00:00:00Z').getTime() / 1000)
  }

  /**
   * quotaPercentage reports consumed requests as a percentage of the configured quota.
   * Missing, zero, or negative limits return zero; positive limits use zero for missing
   * usage and do not cap over-quota results.
   */
  function quotaPercentage(provider: WebSearchProviderConfig): number {
    if (!provider.quota_limit || provider.quota_limit <= 0) return 0
    return ((provider.quota_used ?? 0) / provider.quota_limit) * 100
  }

  /**
   * resetWebSearchUsage asks for confirmation before resetting the selected provider
   * type remotely. A missing row or rejected confirmation returns immediately; local
   * usage becomes zero only after the API confirms success.
   */
  async function resetWebSearchUsage(idx: number) {
    const provider = webSearchConfig.providers[idx]
    if (!provider) return
    if (!confirm(t('admin.settings.webSearchEmulation.resetUsageConfirm'))) return
    try {
      // Reset the remote counter before clearing the displayed usage, so failed persistence
      // leaves the previous count visible instead of presenting an unconfirmed reset.
      await adminAPI.settings.resetWebSearchUsage({
        provider_type: provider.type,
      })
      provider.quota_used = 0
      appStore.showSuccess(t('admin.settings.webSearchEmulation.resetUsageSuccess'))
    } catch (err: unknown) {
      appStore.showError(extractApiErrorMessage(err, t('common.error')))
    }
  }

  /**
   * copyApiKey copies the current draft key for the selected row. An empty key displays
   * the key-entry hint; clipboard permission or write failures display an error without
   * changing the draft.
   */
  async function copyApiKey(idx: number) {
    const key = webSearchConfig.providers[idx]?.api_key
    if (!key) {
      appStore.showError(t('admin.settings.webSearchEmulation.apiKeyPlaceholder'))
      return
    }
    try {
      await navigator.clipboard.writeText(key)
      appStore.showSuccess(t('admin.settings.webSearchEmulation.copied'))
    } catch {
      appStore.showError(t('common.error'))
    }
  }

  /**
   * testWebSearchProvider runs a probe using the trimmed dialog query or its translated
   * default. Starting a probe clears the previous results, and completion releases the
   * loading indicator whether the API succeeds or reports an error.
   */
  async function testWebSearchProvider() {
    wsTestLoading.value = true
    wsTestResult.value = null
    try {
      const query =
        wsTestQuery.value.trim() || t('admin.settings.webSearchEmulation.testDefaultQuery')
      wsTestResult.value = await adminAPI.settings.testWebSearchEmulation(query)
    } catch (err: unknown) {
      appStore.showError(extractApiErrorMessage(err, t('common.error')))
    } finally {
      wsTestLoading.value = false
    }
  }

  /**
   * loadWebSearchConfig loads provider configuration and proxy choices concurrently.
   * Proxy errors become an empty list so configuration can still populate the editor.
   * Configuration failures with an explicit status other than 404 display an error;
   * missing status and 404 leave the current draft unchanged.
   */
  async function loadWebSearchConfig() {
    try {
      // Proxy choices are optional to the configuration editor; suppress their failure locally
      // so an otherwise successful settings response still populates the search provider draft.
      const [resp, proxiesResp] = await Promise.all([
        adminAPI.settings.getWebSearchEmulationConfig(),
        adminAPI.proxies.list().catch(() => ({ items: [] as Proxy[] })),
      ])
      if (resp) {
        webSearchConfig.enabled = resp.enabled || false
        webSearchConfig.providers = resp.providers || []
      }
      webSearchProxies.value = proxiesResp.items || []
    } catch (err: unknown) {
      // An absent configuration reports 404, and statusless failures are also silent here;
      // retain the current draft in both cases while reporting other explicit HTTP failures.
      const status = (err as { status?: number /* Optional HTTP response status. */ })?.status
      if (status !== 404 && status !== undefined) {
        appStore.showError(extractApiErrorMessage(err, t('common.error')))
      }
    }
  }

  /**
   * saveWebSearchConfig validates quota limits and writes the separate search
   * configuration endpoint. The page coordinator consumes its boolean to track partial
   * save success. Nonzero numeric limits below one reject the request. Accepted numeric
   * limits are serialized as numbers, while zero, absent,
   * and nonnumeric limits become null. Provider objects are copied so quota
   * normalization does not mutate the editable draft; other fields, including secret
   * flags, pass through to the endpoint for their own interpretation.
   */
  async function saveWebSearchConfig(): Promise<boolean> {
    try {
      for (const p of webSearchConfig.providers) {
        const raw = p.quota_limit
        if (raw != null && Number(raw) !== 0 && Number(raw) < 1) {
          appStore.showError(t('admin.settings.webSearchEmulation.quotaLimitMustBePositive'))
          return false
        }
      }
      // The API uses null for unlimited quota. Normalize a copied provider list so
      // the draft retains what the administrator entered until the page finishes saving.
      const providers = webSearchConfig.providers.map((p: WebSearchProviderConfig) => ({
        ...p,
        quota_limit: Number(p.quota_limit) > 0 ? Number(p.quota_limit) : null,
      }))
      await adminAPI.settings.updateWebSearchEmulationConfig({
        enabled: webSearchConfig.enabled,
        providers,
      })
      return true
    } catch (err: unknown) {
      appStore.showError(extractApiErrorMessage(err, t('common.error')))
      return false
    }
  }
  return {
    webSearchProxies,
    DEFAULT_WEB_SEARCH_QUOTA_LIMIT,
    webSearchConfig,
    expandedProviders,
    apiKeyVisible,
    wsTestQuery,
    wsTestLoading,
    wsTestResult,
    wsTestDialogOpen,
    openTestDialog,
    toggleProviderExpand,
    removeWebSearchProvider,
    addWebSearchProvider,
    formatSubscribedAt,
    parseSubscribedAt,
    quotaPercentage,
    resetWebSearchUsage,
    copyApiKey,
    testWebSearchProvider,
    loadWebSearchConfig,
    saveWebSearchConfig,
  }
}
