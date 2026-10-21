import { reactive, type Ref } from 'vue'
import type { QuotaResetMode } from '@/constants/provider'

// quotaLimit.ts binds dialog-owned quota refs into the shared field editor value shape.
// Vue unwraps the refs for reads and forwards assignments to their owners, allowing keyed
// update events to change the original create or edit draft.

// QuotaLimitValues represents nullable provider spending ceilings and periodic reset
// schedules.
export interface QuotaLimitValues {
  // The amount and reset drafts remain nullable until configured. Positive amounts
  // enable limits, while fixed reset modes use their local hour, weekday or date, and
  // the shared resetTimezone supplied by the parent.
  totalLimit: number | null // Total spending ceiling in USD.
  dailyLimit: number | null // Daily spending ceiling in USD.
  weeklyLimit: number | null // Weekly spending ceiling in USD.
  monthlyLimit: number | null // Monthly spending ceiling in USD.
  dailyResetMode: QuotaResetMode | null // Daily reset policy draft.
  dailyResetHour: number | null // Daily local reset clock hour.
  weeklyResetMode: QuotaResetMode | null // Weekly reset policy draft.
  monthlyResetMode: QuotaResetMode | null // Monthly reset policy draft.
  weeklyResetDay: number | null // Weekly reset weekday.
  monthlyResetDay: number | null // Monthly reset calendar date.
  weeklyResetHour: number | null // Weekly local reset clock hour.
  monthlyResetHour: number | null // Monthly local reset clock hour.
  // IANA timezone for fixed schedules; the row initializes UTC when entering fixed mode
  // with an empty draft.
  resetTimezone: string | null
}

// QuotaLimitRefs maps quota draft fields to refs owned by the provider dialog.
type QuotaLimitRefs = { [K in keyof QuotaLimitValues]: Ref<QuotaLimitValues[K]> }

// bindQuotaLimits exposes dialog-owned quota refs as a reactive unwrapped value object.
// Assignments through setLimit update those same refs, so callers must retain ownership
// of persistence and draft reset.
export function bindQuotaLimits(refs: QuotaLimitRefs) {
  const limits = reactive(refs) as unknown as QuotaLimitValues

  // setLimit writes one keyed quota value back into the dialog-owned reactive draft.
  function setLimit(key: keyof QuotaLimitValues, value: QuotaLimitValues[keyof QuotaLimitValues]) {
    ;(limits as Record<keyof QuotaLimitValues, unknown>)[key] = value
  }
  return { limits, setLimit }
}
