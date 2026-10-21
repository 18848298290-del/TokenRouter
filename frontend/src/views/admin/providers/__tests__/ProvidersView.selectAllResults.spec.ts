import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ProvidersView from '../ProvidersView.vue'

// ProvidersView.selectAllResults.spec.ts checks selecting every filtered
// provider and preserving selection on request failure. Action and filter
// stubs expose counts and selection events while paginated responses
// distinguish the displayed page from the complete result set.

// Selection spies distinguish ordinary page loading from the larger
// filtered-results request and capture failure notifications without a real
// provider service.
const { listProviders, listWithEtag, getBatchTodayStats, getAllProxies, getAllGroups, showError } =
  vi.hoisted(() => ({
    listProviders: vi.fn(),
    listWithEtag: vi.fn(),
    getBatchTodayStats: vi.fn(),
    getAllProxies: vi.fn(),
    getAllGroups: vi.fn(),
    showError: vi.fn(),
  }))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      list: listProviders,
      listWithEtag,
      getBatchTodayStats,
      batchDelete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      bulkUpdate: vi.fn(),
    },
    proxies: {
      getAll: getAllProxies,
    },
    groups: {
      getAllIncludingInactive: getAllGroups,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token',
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// makeProviders returns sequential Grok OAuth fixtures for the requested
// count.
const makeProviders = (count: number) =>
  Array.from({ length: count }, (_, index) => ({
    id: index + 1,
    name: `provider-${index + 1}`,
    platform: 'grok',
    type: 'oauth',
    status: 'active',
    schedulable: true,
    created_at: '2026-07-23T00:00:00Z',
    updated_at: '2026-07-23T00:00:00Z',
  }))

// ProviderBulkActionsBarStub displays selection counts and completeness while
// exposing select-page, select-all-results, and clear events to the page
// controller.
const ProviderBulkActionsBarStub = {
  props: [
    'selectedIds', // currently selected provider identifiers
    'totalResults', // total matching provider count
    'selectingAll', // complete selection request pending
    'allResultsSelected', // selection covers matching results
  ],
  emits: [
    'select-all-results', // request complete filtered selection
    'select-page', // select currently visible providers
    'clear', // clear selected provider identifiers
  ],
  template: `
    <div>
      <span data-test="selected-count">{{ selectedIds.length }}</span>
      <span data-test="total-results">{{ totalResults }}</span>
      <span data-test="all-results-selected">{{ String(allResultsSelected) }}</span>
      <button data-test="select-page" @click="$emit('select-page')">select page</button>
      <button data-test="select-all-results" @click="$emit('select-all-results')">select all</button>
      <button data-test="clear" @click="$emit('clear')">clear</button>
    </div>
  `,
}

// ProviderTableFiltersStub exposes a filter-change event that exercises page
// selection invalidation without introducing a particular filter field or
// dropdown interaction.
const ProviderTableFiltersStub = {
  emits: [
    'change', // notify parent of input change
  ],
  template: '<button data-test="change-filter" @click="$emit(\'change\')">change filter</button>',
}

// mountView exposes selection counts and filter-change events through provider
// page stubs. Configure list responses first; callers await loading before
// selecting the page or requesting the complete filtered result set.
const mountView = () =>
  mount(ProvidersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: {
          props: [
            'data', // provider rows supplied by page
          ],
          template: '<div data-test="data-table"></div>',
        },
        Pagination: true,
        ConfirmDialog: true,
        ProviderTableActions: {
          template: '<div><slot name="beforeCreate" /><slot name="after" /></div>',
        },
        ProviderTableFilters: ProviderTableFiltersStub,
        ProviderBulkActionsBar: ProviderBulkActionsBarStub,
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
        TLSFingerprintRoutersModal: true,
        CreateProviderModal: true,
        EditProviderModal: true,
        BulkEditProviderModal: true,
        PlatformTypeBadge: true,
        ProviderCapacityCell: true,
        ProviderStatusIndicator: true,
        ProviderTodayStatsCell: true,
        ProviderGroupsCell: true,
        ProviderUsageCell: true,
        Icon: true,
      },
    },
  })

describe('admin ProvidersView select all filtered results', () => {
  beforeEach(() => {
    // Suppress the expected page-load error log while preserving the showError
    // spy, which verifies the failure notification consumed by the interface.
    vi.spyOn(console, 'error').mockImplementation(() => {})
    // Clear persisted table choices before selection requests so page-size and
    // filter assumptions remain controlled by the list fixture for each case.
    localStorage.clear()
    listProviders.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()
    showError.mockReset()

    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null,
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // Forty-five matching providers extend beyond the displayed twenty rows.
  // Selecting all publishes every ID, changing the total clears the
  // completeness indicator, and changing filters empties selection.
  it('selects all matching IDs in one commit and clears the selection when filters change', async () => {
    const allProviders = makeProviders(45)
    listProviders.mockImplementation(async (_page: number, pageSize: number) => {
      if (pageSize === 1000) {
        return {
          items: allProviders,
          total: 45,
          page: 1,
          page_size: 1000,
          pages: 1,
        }
      }
      return {
        items: allProviders.slice(0, 20),
        total: 45,
        page: 1,
        page_size: 20,
        pages: 3,
      }
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="select-all-results"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="selected-count"]').text()).toBe('45')
    expect(wrapper.get('[data-test="total-results"]').text()).toBe('45')
    expect(wrapper.get('[data-test="all-results-selected"]').text()).toBe('true')
    expect(listProviders).toHaveBeenCalledWith(
      1,
      1000,
      expect.objectContaining({
        lite: '1',
        include_scheduler_score: '0',
      }),
    )

    const vm = wrapper.vm as unknown as {
      // The page owns this mutable pagination object; changing its total
      // recomputes whether selection covers every result.
      pagination: {
        total: number // matching provider count
      }
    }
    vm.pagination.total = 46
    await wrapper.vm.$nextTick()
    expect(wrapper.get('[data-test="all-results-selected"]').text()).toBe('false')

    await wrapper.get('[data-test="change-filter"]').trigger('click')

    expect(wrapper.get('[data-test="selected-count"]').text()).toBe('0')
    expect(wrapper.get('[data-test="all-results-selected"]').text()).toBe('false')
  })

  // The complete-results request fails after the current page has been
  // selected. The twenty selected IDs remain available, the completeness
  // indicator stays false, and the page reports failure.
  it('keeps the original page selection when loading all results fails', async () => {
    const currentPage = makeProviders(20)
    listProviders.mockImplementation(async (_page: number, pageSize: number) => {
      if (pageSize === 1000) {
        throw new Error('load all failed')
      }
      return {
        items: currentPage,
        total: 45,
        page: 1,
        page_size: 20,
        pages: 3,
      }
    })

    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-test="select-page"]').trigger('click')
    expect(wrapper.get('[data-test="selected-count"]').text()).toBe('20')

    await wrapper.get('[data-test="select-all-results"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="selected-count"]').text()).toBe('20')
    expect(wrapper.get('[data-test="all-results-selected"]').text()).toBe('false')
    expect(showError).toHaveBeenCalledWith('admin.providers.bulkActions.selectAllFailed')
  })
})

useProtocolCatalogFixture()
