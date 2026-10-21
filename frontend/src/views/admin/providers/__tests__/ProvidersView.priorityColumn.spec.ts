import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ProvidersView from '../ProvidersView.vue'

// ProvidersView.priorityColumn.spec.ts checks priority column visibility and
// sorting across saved preference versions. The table stub exposes column
// metadata and emits sorting events so the assertions connect browser storage
// decisions to provider list request parameters.

// listProviders records the sort and enrichment parameters produced by stored
// column preferences, while the remaining admin endpoints return minimal page
// metadata.
const { listProviders } = vi.hoisted(() => ({
  listProviders: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      list: listProviders,
      listWithEtag: vi.fn(),
      getBatchTodayStats: vi.fn().mockResolvedValue({ stats: {} }),
      getUpstreamBillingProbeSettings: vi
        .fn()
        .mockResolvedValue({ enabled: true, interval_minutes: 30 }),
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
    },
    proxies: { getAll: vi.fn().mockResolvedValue([]) },
    groups: {
      getAll: vi.fn().mockResolvedValue([]),
      getAllIncludingInactive: vi.fn().mockResolvedValue([]),
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn(), showSuccess: vi.fn(), showInfo: vi.fn() }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ token: 'test-token' }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// DataTableStub displays each supplied column key and sortable flag and emits
// a priority sort event through a dedicated button.
const DataTableStub = {
  props: [
    'columns', // page table column definitions
  ],
  emits: [
    'sort', // selected column and sort order
  ],
  template: `
    <div data-test="data-table">
      <span v-for="column in columns" :key="column.key" :data-column="column.key">
        {{ column.sortable ? 'sortable' : 'fixed' }}
      </span>
      <button data-test="sort-priority" @click="$emit('sort', 'priority', 'desc')" />
    </div>
  `,
}

// mountView renders provider preferences through a table stub exposing
// sortable columns. List mocks must be configured before mounting; callers
// await pending requests before inspecting columns or emitting a sort action.
function mountView() {
  return mount(ProvidersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: DataTableStub,
        ProviderTableActions: { template: '<div><slot name="after" /></div>' },
        ProviderTableFilters: true,
        ProviderBulkActionsBar: true,
        Pagination: true,
        ConfirmDialog: true,
        ProviderActionMenu: true,
        ImportDataModal: true,
        ReAuthProviderModal: true,
        ProviderTestModal: true,
        ProviderStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateProviderModal: true,
        EditProviderModal: true,
        BulkEditProviderModal: true,
        PlatformTypeBadge: true,
        ProviderCapacityCell: true,
        ProviderStatusIndicator: true,
        ProviderTodayStatsCell: true,
        ProviderGroupsCell: true,
        ProviderUsageCell: true,
        HelpTooltip: true,
        Icon: true,
        Teleport: true,
      },
    },
  })
}

describe('admin ProvidersView priority column preferences', () => {
  beforeEach(() => {
    // Remove preceding preference versions before installing each scenario so
    // a prior migration cannot decide whether priority appears in this mount.
    localStorage.clear()
    listProviders.mockReset().mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0,
    })
  })

  // Fresh browser storage has no hidden-column preferences. Priority appears
  // as sortable, and sorting it descending sends the corresponding sort fields
  // with the list request.
  it('shows priority as a sortable column for fresh preferences', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-column="priority"]').text()).toBe('sortable')

    await wrapper.get('[data-test="sort-priority"]').trigger('click')
    await flushPromises()

    expect(listProviders).toHaveBeenLastCalledWith(
      1,
      20,
      expect.objectContaining({ sort_by: 'priority', sort_order: 'desc' }),
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
  })

  // A current preference version explicitly lists priority as hidden. Mounting
  // the page keeps that column absent and preserves the saved hidden-column
  // entries without rewriting the choice.
  it('preserves an existing preference that explicitly hides priority', async () => {
    localStorage.setItem('provider-hidden-columns', JSON.stringify(['priority', 'today_stats']))
    localStorage.setItem('provider-hidden-columns-version', 'scheduler-score-hidden-by-default')

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-column="priority"]').exists()).toBe(false)
    expect(JSON.parse(localStorage.getItem('provider-hidden-columns') || '[]')).toEqual([
      'priority',
      'today_stats',
    ])
  })

  // Older preferences hide only today's statistics before migration. Priority
  // remains sortable while the migration adds scheduler_score to the hidden
  // columns without adding priority itself.
  it('keeps priority visible while migrating older saved preferences', async () => {
    localStorage.setItem('provider-hidden-columns', JSON.stringify(['today_stats']))

    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.get('[data-column="priority"]').text()).toBe('sortable')
    expect(JSON.parse(localStorage.getItem('provider-hidden-columns') || '[]')).toEqual(
      expect.arrayContaining(['today_stats', 'scheduler_score']),
    )
    expect(JSON.parse(localStorage.getItem('provider-hidden-columns') || '[]')).not.toContain(
      'priority',
    )
  })
})

useProtocolCatalogFixture()
