import type { SelectOption } from '@/components/common/Select.vue'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'

// platformOptions.ts builds the platform selector used by the Ops dashboard
// toolbar. The selectable platforms follow execution-provider identities, while
// the empty value clears the platform filter and group options are supplied
// separately.

// buildOpsPlatformOptions prepends an unfiltered option to the
// execution-platform catalog. The caller supplies its translated label; the
// empty string is the value the dashboard uses to clear platform filtering.
export function buildOpsPlatformOptions(allLabel: string): SelectOption[] {
  return [{ value: '', label: allLabel }, ...CONCRETE_PLATFORM_OPTIONS]
}
