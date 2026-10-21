import { defineStore } from 'pinia'
import { ref } from 'vue'
import { adminAPI } from '@/api'
import type { CustomMenuItem } from '@/api/settings.types'

// adminSettings.ts caches administrator navigation switches and custom menu entries.
// Browser storage supplies initial switch values before the settings and payment
// requests complete, reducing changes to visible navigation during startup.
//
// The two requests publish their results together after Promise.all succeeds. A failed
// request retains cached values and marks the store loaded, so another ordinary read
// does not repeatedly retry. Local setters publish their reactive value before
// attempting persistence, and storage failures leave that value available. An
// application-lifetime window listener applies the operations-disabled signal emitted by
// the HTTP interceptor.

// useAdminSettingsStore exposes administrator navigation switches and custom menus
// through Pinia. Browser-cached switches initialize synchronously, and an application-lifetime listener applies operations-disabled signals when window is available.
export const useAdminSettingsStore = defineStore('adminSettings', () => {
  // loaded prevents repeated ordinary reads after success or failure, while loading
  // prevents overlapping settings and payment configuration requests.
  const loaded = ref(false)
  const loading = ref(false)

  // readCachedBool reads literal true or false from a browser storage key. Missing,
  // invalid, or inaccessible values return the supplied default so navigation
  // initialization can continue without storage.
  const readCachedBool = (key: string, defaultValue: boolean): boolean => {
    try {
      const raw = localStorage.getItem(key)
      if (raw === 'true') return true
      if (raw === 'false') return false
    } catch {
      // Browser storage may be unavailable during initialization; use the supplied
      // default so cached navigation reads can still complete.
    }
    return defaultValue
  }

  // writeCachedBool attempts to persist a navigation switch as a boolean string. Storage
  // failures are suppressed because reactive navigation state has already been updated
  // by the calling action.
  const writeCachedBool = (key: string, value: boolean) => {
    try {
      localStorage.setItem(key, value ? 'true' : 'false')
    } catch {
      // Reactive switches are already published by the caller; ignore storage
      // failure so navigation keeps the locally selected state.
    }
  }

  // Navigation switches initialize from browser storage before network loading;
  // customMenuItems is populated from administrator settings after the combined request
  // succeeds.
  const opsMonitoringEnabled = ref(readCachedBool('ops_monitoring_enabled_cached', true))
  const opsRealtimeMonitoringEnabled = ref(
    readCachedBool('ops_realtime_monitoring_enabled_cached', true),
  )
  const paymentEnabled = ref(readCachedBool('payment_enabled_cached', false))
  const customMenuItems = ref<CustomMenuItem[]>([])

  // fetch loads administrator settings and payment availability together unless already
  // loaded or busy. Force bypasses freshness but still respects the busy flag. Both
  // responses must succeed before values publish, and each switch is persisted after its
  // reactive assignment. Failure retains current values and marks the store loaded,
  // requiring force for another attempt. Concurrent callers return immediately rather
  // than waiting for the active request.
  async function fetch(force = false): Promise<void> {
    if (loaded.value && !force) return
    if (loading.value) return

    loading.value = true
    try {
      const [settings, paymentConfigResp] = await Promise.all([
        adminAPI.settings.getSettings(),
        adminAPI.payment.getConfig(),
      ])
      // Publish switches only after both responses succeed, then persist each
      // corresponding value so later startup can reuse the last learned navigation
      // state.
      opsMonitoringEnabled.value = settings.ops_monitoring_enabled ?? true
      writeCachedBool('ops_monitoring_enabled_cached', opsMonitoringEnabled.value)

      opsRealtimeMonitoringEnabled.value = settings.ops_realtime_monitoring_enabled ?? true
      writeCachedBool('ops_realtime_monitoring_enabled_cached', opsRealtimeMonitoringEnabled.value)

      customMenuItems.value = Array.isArray(settings.custom_menu_items)
        ? settings.custom_menu_items
        : []

      paymentEnabled.value = paymentConfigResp.data?.enabled ?? false
      writeCachedBool('payment_enabled_cached', paymentEnabled.value)

      loaded.value = true
    } catch (err) {
      // Treat this failed load as attempted while keeping cached switches; only a forced
      // refresh retries, preventing repeated navigation reads from hammering unavailable
      // settings.
      loaded.value = true
      console.error('[adminSettings] Failed to fetch settings:', err)
    } finally {
      loading.value = false
    }
  }

  // setOpsMonitoringEnabledLocal publishes operations monitoring and attempts to persist
  // the switch. It also marks settings loaded so ordinary fetch calls keep this locally
  // learned value until a forced refresh.
  function setOpsMonitoringEnabledLocal(value: boolean) {
    opsMonitoringEnabled.value = value
    writeCachedBool('ops_monitoring_enabled_cached', value)
    loaded.value = true
  }

  // setOpsRealtimeMonitoringEnabledLocal publishes realtime monitoring and attempts to
  // persist the switch. It also marks settings loaded so ordinary fetch calls keep this
  // locally learned value until a forced refresh.
  function setOpsRealtimeMonitoringEnabledLocal(value: boolean) {
    opsRealtimeMonitoringEnabled.value = value
    writeCachedBool('ops_realtime_monitoring_enabled_cached', value)
    loaded.value = true
  }

  // setPaymentEnabledLocal publishes payment availability and attempts to persist the
  // switch. It also marks settings loaded so ordinary fetch calls keep this locally
  // learned value until a forced refresh.
  function setPaymentEnabledLocal(value: boolean) {
    paymentEnabled.value = value
    writeCachedBool('payment_enabled_cached', value)
    loaded.value = true
  }

  // eventHandlerCleanup retains the installed handler closure for this store lifetime
  // and acts as the marker that prevents duplicate listener registration.
  let eventHandlerCleanup: (() => void) | null = null

  // initializeEventListeners registers the operations-disabled event once for this store
  // instance. Its retained cleanup closure also acts as the installation marker; window-access failures are suppressed for environments lacking browser events.
  function initializeEventListeners() {
    if (eventHandlerCleanup) return

    try {
      // handler disables operations navigation after the interceptor reports unavailable
      // monitoring.
      const handler = () => {
        setOpsMonitoringEnabledLocal(false)
      }
      window.addEventListener('ops-monitoring-disabled', handler)
      eventHandlerCleanup = () => {
        window.removeEventListener('ops-monitoring-disabled', handler)
      }
    } catch {
      // A browser event target may be unavailable in this environment; leave the
      // installation marker empty so later initialization can retry.
    }
  }

  if (typeof window !== 'undefined') {
    initializeEventListeners()
  }

  return {
    loaded,
    loading,
    opsMonitoringEnabled,
    opsRealtimeMonitoringEnabled,
    paymentEnabled,
    customMenuItems,
    fetch,
    setOpsMonitoringEnabledLocal,
    setOpsRealtimeMonitoringEnabledLocal,
    setPaymentEnabledLocal,
  }
})
