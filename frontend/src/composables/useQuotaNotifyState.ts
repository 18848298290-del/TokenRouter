import { reactive, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import { QUOTA_THRESHOLD_TYPE_FIXED, type QuotaThresholdType } from '@/constants/provider'

// useQuotaNotifyState.ts manages provider quota notification drafts and their Extra
// encoding. Daily, weekly, monthly, and total dimensions share enabled, threshold, and
// threshold-type fields, with null representing missing saved configuration. The global
// feature setting is fetched separately from each provider draft.
//
// Creation writes enabled notification values into the supplied object. Updates also
// remove disabled keys and missing enabled thresholds, so changes to an existing provider
// clear obsolete settings. Both paths iterate the same dimension list, and enabled drafts
// with no threshold type serialize as fixed thresholds. Callers own submission and when to
// reload the draft.

// QUOTA_NOTIFY_DIMS defines the shared dimension order used when loading, writing, and
// resetting provider notification drafts.
export const QUOTA_NOTIFY_DIMS = ['daily', 'weekly', 'monthly', 'total'] as const

// QuotaNotifyDim identifies one supported local spending notification dimension for
// providers.
export type QuotaNotifyDim = (typeof QUOTA_NOTIFY_DIMS)[number]

// QuotaNotifyDimState holds nullable quota notification fields for one provider dimension.
export interface QuotaNotifyDimState {
  // enabled, threshold, and thresholdType represent one dimension's draft. Null values
  // mark missing saved configuration, and writeToExtra applies different clearing rules
  // for creation and updates. thresholdType selects USD for fixed thresholds or percentage
  // points of the dimension limit, where 100 represents the whole limit. A null
  // thresholdType serializes as fixed mode when the dimension is enabled.
  enabled: boolean | null // nullable alert enablement flag
  threshold: number | null // nullable alert threshold value
  thresholdType: QuotaThresholdType | null // nullable threshold interpretation selector
}

// useQuotaNotifyState creates dialog-local provider notification drafts and serializers.
// Load provider Extra before editing, then write the draft into the create or update
// payload; this composable does not submit it.
export function useQuotaNotifyState() {
  // globalEnabled controls feature availability separately from state, whose four
  // dimension drafts retain provider notification values until loading or resetting
  // replaces them.
  const globalEnabled = ref(false)
  const state = reactive<Record<QuotaNotifyDim, QuotaNotifyDimState>>({
    daily: { enabled: null, threshold: null, thresholdType: null },
    weekly: { enabled: null, threshold: null, thresholdType: null },
    monthly: { enabled: null, threshold: null, thresholdType: null },
    total: { enabled: null, threshold: null, thresholdType: null },
  })

  // loadGlobalState starts loading the global notification feature flag and returns
  // immediately. Success enables it only for a true setting; request failure disables it,
  // with no stale-result guard between calls.
  function loadGlobalState() {
    adminAPI.settings
      .getSettings()
      .then((settings) => {
        globalEnabled.value = settings.provider_quota_notify_enabled === true
      })
      // A failed settings request disables notification controls for this draft, keeping
      // an unknown global feature flag from enabling them.
      .catch(() => {
        globalEnabled.value = false
      })
  }

  // loadFromExtra replaces each dimension draft with values from provider Extra. Missing
  // or null fields become null, so loading another provider also clears values absent from
  // that provider.
  function loadFromExtra(extra: Record<string, unknown> | null | undefined) {
    for (const d of QUOTA_NOTIFY_DIMS) {
      state[d].enabled = (extra?.[`quota_notify_${d}_enabled`] as boolean) ?? null
      state[d].threshold = (extra?.[`quota_notify_${d}_threshold`] as number) ?? null
      state[d].thresholdType =
        (extra?.[`quota_notify_${d}_threshold_type`] as QuotaThresholdType) ?? null
    }
  }

  // writeToExtra mutates the supplied Extra object according to the current notification
  // drafts and submission mode. Enabled dimensions write their flag and use fixed
  // threshold mode when no type is selected.
  //
  // Update mode removes missing thresholds and all keys for disabled dimensions, allowing
  // existing values to be cleared. Create mode leaves those keys untouched, so callers
  // should supply an appropriate new payload. The function performs encoding only;
  // persistence and submission errors belong to its caller.
  function writeToExtra(extra: Record<string, unknown>, mode: 'create' | 'update') {
    for (const d of QUOTA_NOTIFY_DIMS) {
      const s = state[d]
      if (s.enabled) {
        extra[`quota_notify_${d}_enabled`] = true
        if (s.threshold != null) {
          extra[`quota_notify_${d}_threshold`] = s.threshold
        } else if (mode === 'update') {
          // Remove the absent threshold from copied Extra so clearing an enabled draft
          // does not retain its previously saved threshold.
          delete extra[`quota_notify_${d}_threshold`]
        }
        // Enabled drafts without a selected threshold type serialize fixed mode so the API
        // receives a concrete interpretation for the configured threshold.
        extra[`quota_notify_${d}_threshold_type`] = s.thresholdType || QUOTA_THRESHOLD_TYPE_FIXED
      } else if (mode === 'update') {
        // Remove all disabled dimension keys from copied Extra so saving the update
        // clears the previous notification configuration for this dimension.
        delete extra[`quota_notify_${d}_enabled`]
        delete extra[`quota_notify_${d}_threshold`]
        delete extra[`quota_notify_${d}_threshold_type`]
      }
    }
  }

  // setField writes a caller-supplied notification value into one draft dimension.
  function setField(
    dim: QuotaNotifyDim,
    field: keyof QuotaNotifyDimState,
    value: QuotaNotifyDimState[keyof QuotaNotifyDimState],
  ) {
    ;(state[dim] as Record<keyof QuotaNotifyDimState, unknown>)[field] = value
  }

  // reset clears every notification draft dimension back to null fields.
  function reset() {
    for (const d of QUOTA_NOTIFY_DIMS) {
      state[d].enabled = null
      state[d].threshold = null
      state[d].thresholdType = null
    }
  }

  return { globalEnabled, state, loadGlobalState, loadFromExtra, writeToExtra, setField, reset }
}
