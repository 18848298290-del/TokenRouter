import { afterEach, describe, expect, it } from 'vitest'

import { getPersistedPageSize } from '@/composables/usePersistedPageSize'

// usePersistedPageSize.spec.ts checks that configured table defaults govern pagination
// initialization. Browser storage contains an older user-selected size while injected
// runtime settings provide a different allowed default, making precedence visible through
// the public page-size helper.

describe('usePersistedPageSize', () => {
  afterEach(() => {
    localStorage.clear()
    delete window.__APP_CONFIG__
  })

  // Injected table settings specify a larger default while localStorage retains an older
  // user preference. Reading the page size must return the configured runtime default.
  it('uses the system table default instead of stale localStorage state', () => {
    window.__APP_CONFIG__ = {
      table_default_page_size: 1000,
      table_page_size_options: [20, 50, 1000],
    } as any
    localStorage.setItem('table-page-size', '50')
    localStorage.setItem('table-page-size-source', 'user')

    expect(getPersistedPageSize()).toBe(1000)
  })
})
