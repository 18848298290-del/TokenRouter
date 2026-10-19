import { reactive, ref } from 'vue'
import { adminAPI } from '@/api/admin'
import { QUOTA_THRESHOLD_TYPE_FIXED, type QuotaThresholdType } from '@/constants/provider'

// useQuotaNotifyState.ts manages provider quota notification drafts and encoding.
// Daily, weekly, monthly, and total dimensions each carry an enabled flag, a
// threshold, and its interpretation. Null draft values represent omitted saved
// configuration, while an enabled draft uses the fixed threshold mode when its
// mode is absent. The global setting controls whether forms offer notification
// controls and is read separately from provider-specific values.
//
// Loading and saving iterate the same dimension list so newly enabled monthly
// notices survive an edit round trip. Update serialization removes disabled
// notification keys, while create serialization emits configured enabled values.

// QUOTA_NOTIFY_DIMS determines the shared iteration order for loading, saving,
// and resetting quota notification drafts.
export const QUOTA_NOTIFY_DIMS = ['daily', 'weekly', 'monthly', 'total'] as const
// QuotaNotifyDim identifies one supported local spending notification dimension.
export type QuotaNotifyDim = (typeof QUOTA_NOTIFY_DIMS)[number]

// QuotaNotifyDimState represents nullable notification settings in provider
// form drafts.
export interface QuotaNotifyDimState {
  enabled: boolean | null // quota alert enablement flag
  threshold: number | null // configured alert threshold value
  thresholdType: QuotaThresholdType | null // alert threshold interpretation selector
}

// useQuotaNotifyState creates dialog-local notification drafts and their
// serializers.
// Callers load provider Extra when opening the dialog and write its drafts into
// the appropriate create or update payload before submission.
export function useQuotaNotifyState() {
  const globalEnabled = ref(false)
  const state = reactive<Record<QuotaNotifyDim, QuotaNotifyDimState>>({
    daily: { enabled: null, threshold: null, thresholdType: null },
    weekly: { enabled: null, threshold: null, thresholdType: null },
    monthly: { enabled: null, threshold: null, thresholdType: null },
    total: { enabled: null, threshold: null, thresholdType: null },
  })

  function loadGlobalState() {
    adminAPI.settings
      .getSettings()
      .then((settings) => {
        globalEnabled.value = settings.provider_quota_notify_enabled === true
      })
      .catch(() => {
        globalEnabled.value = false
      })
  }

  function loadFromExtra(extra: Record<string, unknown> | null | undefined) {
    for (const d of QUOTA_NOTIFY_DIMS) {
      state[d].enabled = (extra?.[`quota_notify_${d}_enabled`] as boolean) ?? null
      state[d].threshold = (extra?.[`quota_notify_${d}_threshold`] as number) ?? null
      state[d].thresholdType = (extra?.[`quota_notify_${d}_threshold_type`] as QuotaThresholdType) ?? null
    }
  }

  function writeToExtra(extra: Record<string, unknown>, mode: 'create' | 'update') {
    for (const d of QUOTA_NOTIFY_DIMS) {
      const s = state[d]
      if (s.enabled) {
        extra[`quota_notify_${d}_enabled`] = true
        if (s.threshold != null) {
          extra[`quota_notify_${d}_threshold`] = s.threshold
        } else if (mode === 'update') {
          delete extra[`quota_notify_${d}_threshold`]
        }
        extra[`quota_notify_${d}_threshold_type`] = s.thresholdType || QUOTA_THRESHOLD_TYPE_FIXED
      } else if (mode === 'update') {
        delete extra[`quota_notify_${d}_enabled`]
        delete extra[`quota_notify_${d}_threshold`]
        delete extra[`quota_notify_${d}_threshold_type`]
      }
    }
  }

  // 表单组件通过 setField 逐项回写共享状态中的通知阈值。
  function setField(
    dim: QuotaNotifyDim,
    field: keyof QuotaNotifyDimState,
    value: QuotaNotifyDimState[keyof QuotaNotifyDimState],
  ) {
    (state[dim] as Record<keyof QuotaNotifyDimState, unknown>)[field] = value
  }

  function reset() {
    for (const d of QUOTA_NOTIFY_DIMS) {
      state[d].enabled = null
      state[d].threshold = null
      state[d].thresholdType = null
    }
  }

  return { globalEnabled, state, loadGlobalState, loadFromExtra, writeToExtra, setField, reset }
}
