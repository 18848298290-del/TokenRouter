import { reactive, type Ref } from 'vue'
import type { QuotaResetMode } from '@/constants/provider'

// quotaLimit.ts binds provider quota draft refs to a shared editable view.
// The create and edit dialogs own each value while the reusable field component
// reads their unwrapped values and writes individual changes through setLimit.

// QuotaLimitValues describes provider spending ceilings and periodic reset drafts.
export interface QuotaLimitValues {
  totalLimit: number | null
  dailyLimit: number | null
  weeklyLimit: number | null
  monthlyLimit: number | null // monthly spending ceiling in USD
  dailyResetMode: QuotaResetMode | null
  dailyResetHour: number | null
  weeklyResetMode: QuotaResetMode | null
  monthlyResetMode: QuotaResetMode | null // monthly reset policy selector
  weeklyResetDay: number | null
  monthlyResetDay: number | null // monthly reset calendar date
  weeklyResetHour: number | null
  monthlyResetHour: number | null // monthly local reset hour
  resetTimezone: string | null
}

// QuotaLimitRefs maps shared field names to their dialog-owned reactive refs.
type QuotaLimitRefs = { [K in keyof QuotaLimitValues]: Ref<QuotaLimitValues[K]> }

// bindQuotaLimits exposes dialog-owned quota refs as unwrapped editable values.
// Mutations update the supplied refs directly, so callers retain the same drafts
// when shared controls emit individual field changes.
export function bindQuotaLimits(refs: QuotaLimitRefs) {
  const limits = reactive(refs) as unknown as QuotaLimitValues
  // setLimit writes one quota field back into its owning dialog draft.
  function setLimit(key: keyof QuotaLimitValues, value: QuotaLimitValues[keyof QuotaLimitValues]) {
    (limits as Record<keyof QuotaLimitValues, unknown>)[key] = value
  }
  return { limits, setLimit }
}
