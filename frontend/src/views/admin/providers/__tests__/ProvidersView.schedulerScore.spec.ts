import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ProvidersView from '../ProvidersView.vue'

// ProvidersView.schedulerScore.spec.ts checks scheduler score rendering and
// opt-in column preferences in the provider list. Fixtures distinguish grouped
// scores, an ungrouped base score, and missing data while list spies expose
// whether score enrichment is requested.

// Scheduler list spies provide grouped and ungrouped fixtures while recording
// whether stored column preferences request scheduler score enrichment from
// the server.
const { listProviders, listWithEtag, getBatchTodayStats, getAllProxies, getAllGroups } = vi.hoisted(
  () => ({
    listProviders: vi.fn(),
    listWithEtag: vi.fn(),
    getBatchTodayStats: vi.fn(),
    getAllProxies: vi.fn(),
    getAllGroups: vi.fn(),
  }),
)

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      list: listProviders,
      listWithEtag,
      getBatchTodayStats,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
    },
    proxies: {
      getAll: getAllProxies,
    },
    groups: {
      getAll: getAllGroups,
      getAllIncludingInactive: getAllGroups,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
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

// DataTableStub renders scheduler-score slots for every fixture row, exposing
// base-score fallback and missing-score presentation independently of column
// visibility.
const DataTableStub = {
  props: [
    'columns', // page table column definitions
    'data', // provider rows supplied by page
  ],
  template: `
    <div data-test="data-table">
      <div v-for="row in data" :key="row.id" :data-test="'scheduler-score-' + row.id">
        <slot name="cell-scheduler_score" :row="row" />
      </div>
    </div>
  `,
}

// mountView renders scheduler score slots for every mocked provider row.
// Configure provider responses before mounting; the table stub renders
// supplied slots even when the production column preference marks them hidden.
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
        HelpTooltip: true,
        Pagination: true,
        ConfirmDialog: true,
        ProviderTableActions: {
          template: '<div><slot name="beforeCreate" /><slot name="after" /></div>',
        },
        ProviderTableFilters: { template: '<div></div>' },
        ProviderBulkActionsBar: true,
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
        Icon: true,
      },
    },
  })
}

// baseProvider supplies common active OpenAI fields so each score fixture
// changes only identity, grouping, and the score observations under test.
const baseProvider = {
  platform: 'openai',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  concurrency: 1,
  priority: 0,
  error_message: null,
  last_used_at: null,
  expires_at: null,
  auto_pause_on_expired: false,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
}

describe('admin ProvidersView scheduler score column', () => {
  beforeEach(() => {
    // Reset the preference version between cases so score enrichment requests
    // are driven by the specific hidden-column scenario being exercised.
    localStorage.clear()

    listProviders.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()

    listProviders.mockResolvedValue({
      items: [
        {
          ...baseProvider,
          id: 1,
          name: 'ungrouped-openai',
          // Omit per-group scores here so this row exercises the base-score
          // fallback independently of the grouped fixture.
          scheduler_score: {
            base_score: 1.234567,
            sticky_score: 0,
            sticky_weighted_enabled: false,
          },
        },
        {
          ...baseProvider,
          id: 2,
          name: 'grouped-openai',
          scheduler_score: {
            base_score: 2,
            sticky_score: 3,
            sticky_weighted_enabled: true,
          },
          scheduler_scores: [
            {
              group_id: 5,
              group_name: 'group-five',
              base_score: 2,
              sticky_score: 3,
              sticky_weighted_enabled: true,
            },
          ],
        },
        {
          ...baseProvider,
          id: 3,
          name: 'no-score',
          platform: 'anthropic',
        },
      ],
      total: 3,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null,
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  // An ungrouped provider has a base score without per-group scores. Its cell
  // displays that value and the ungrouped label even when enrichment is
  // disabled.
  it('falls back to the base score for ungrouped providers instead of showing a dash', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(listProviders.mock.calls[0]?.[2]).toEqual(
      expect.objectContaining({
        include_scheduler_score: '0',
      }),
    )

    const ungroupedCell = wrapper.find('[data-test="scheduler-score-1"]')
    expect(ungroupedCell.exists()).toBe(true)
    expect(ungroupedCell.text()).toContain('1.234567')
    expect(ungroupedCell.text()).toContain('admin.providers.schedulerScore.ungrouped')
    expect(ungroupedCell.text()).not.toBe('-')
  })

  // A provider includes a named group and its scheduler score. The rendered
  // score cell includes the group name and value supplied by that group entry.
  it('renders per-group scores for grouped providers', async () => {
    const wrapper = mountView()
    await flushPromises()

    const groupedCell = wrapper.find('[data-test="scheduler-score-2"]')
    expect(groupedCell.exists()).toBe(true)
    expect(groupedCell.text()).toContain('group-five')
    expect(groupedCell.text()).toContain('2')
  })

  // An older hidden-column preference predates scheduler-score opt-in.
  // Mounting migrates scheduler_score into the hidden set and requests the
  // list with score enrichment disabled.
  it('keeps scheduler score hidden for old saved column settings until the admin opts in again', async () => {
    localStorage.setItem('provider-hidden-columns', JSON.stringify(['today_stats']))

    mountView()
    await flushPromises()

    expect(listProviders.mock.calls[0]?.[2]).toEqual(
      expect.objectContaining({
        include_scheduler_score: '0',
      }),
    )
    expect(JSON.parse(localStorage.getItem('provider-hidden-columns') || '[]')).toContain(
      'scheduler_score',
    )
  })

  // A current preference version leaves scheduler_score visible. The provider
  // list request enables score enrichment so the displayed column can receive
  // its group-specific values.
  it('requests scheduler scores when the migrated column settings explicitly show the column', async () => {
    localStorage.setItem('provider-hidden-columns', JSON.stringify(['today_stats']))
    localStorage.setItem('provider-hidden-columns-version', 'scheduler-score-hidden-by-default')

    mountView()
    await flushPromises()

    expect(listProviders.mock.calls[0]?.[2]).toEqual(
      expect.objectContaining({
        include_scheduler_score: '1',
      }),
    )
  })

  // A provider lacks both aggregate and per-group score data. Its score cell
  // displays a single dash to distinguish missing information from a numeric
  // score.
  it('still shows a dash when no scheduler score is available', async () => {
    const wrapper = mountView()
    await flushPromises()

    const emptyCell = wrapper.find('[data-test="scheduler-score-3"]')
    expect(emptyCell.exists()).toBe(true)
    expect(emptyCell.text()).toBe('-')
  })
})

useProtocolCatalogFixture()
