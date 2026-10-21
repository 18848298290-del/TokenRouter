import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'

import ProvidersView from '../ProvidersView.vue'

// ProvidersView.cacheLifecycle.spec.ts exercises usage-cache response ownership and
// incremental list refreshes through the mounted provider page. Deferred requests
// expose out-of-order completion, while rendered cell values and storage entries
// show whether the current request retains ownership of its result.

// These transport and notification mocks isolate the page from network services and expose observable request arguments.
const {
  listProviders,
  listWithEtag,
  getBatchTodayStats,
  getById,
  queryUpstreamUsage,
  queryBatchUpstreamUsage,
  getAllProxies,
  getAllGroups,
  showError,
  showSuccess,
} = vi.hoisted(() => ({
  listProviders: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getById: vi.fn(),
  queryUpstreamUsage: vi.fn(),
  queryBatchUpstreamUsage: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn(),
  showError: vi.fn(),
  showSuccess: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      list: listProviders,
      listWithEtag,
      getBatchTodayStats,
      getById,
      queryUpstreamUsage,
      queryBatchUpstreamUsage,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
    },
    proxies: { getAll: getAllProxies },
    groups: { getAllIncludingInactive: getAllGroups },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
    showInfo: vi.fn(),
    showWarning: vi.fn(),
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token',
    user: { id: 42 },
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

// DataTableStub and its usage cell expose row identity and query callbacks while preserving provider selection event wiring.
const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-row-id="row.id">
        <slot name="cell-select" :row="row" />
        <slot name="cell-usage" :row="row" />
      </div>
    </div>
  `,
}

// ProviderUsageCellStub renders cached balances and exposes forced refresh plus provider-updated events to exercise invalidation.
const ProviderUsageCellStub = defineComponent({
  props: [
    'provider',
    'upstreamUsage',
    'upstreamUsageError',
    'upstreamUsageLoading',
    'requestUpstreamUsage',
  ],
  emits: ['provider-updated'],
  template: `
    <div :data-usage-provider="provider.id">
      <span data-test="upstream-remaining">{{ upstreamUsage?.balance?.remaining ?? '' }}</span>
      <button data-test="query-upstream" @click="requestUpstreamUsage(provider, { force: true })">query</button>
      <button data-test="provider-updated" @click="$emit('provider-updated', provider)">updated</button>
    </div>
  `,
})

// ProviderBulkActionsBarStub keeps the page bulk-query event available while excluding unrelated menu presentation from these cases.
const ProviderBulkActionsBarStub = {
  props: ['selectedIds', 'upstreamUsageLoading'],
  emits: ['query-upstream-usage'],
  template:
    '<button data-test="query-upstream-batch" :disabled="upstreamUsageLoading" @click="$emit(\'query-upstream-usage\')">batch</button>',
}

// provider builds a list row with upstream usage enabled for API keys.
// OAuth fixtures omit that query configuration so callers can exercise eligibility
// independently from credentials and proxy fields in the same list shape.
const provider = (id: number, type: 'apikey' | 'oauth' = 'apikey') => ({
  id,
  name: `provider-${id}`,
  platform: 'openai',
  type,
  credentials: type === 'apikey' ? { base_url: 'https://gateway.example.test/v1' } : {},
  extra: type === 'apikey' ? { upstream_usage_query: { enabled: true, adapter: 'sub2api' } } : {},
  proxy_id: null,
  concurrency: 1,
  priority: 1,
  status: 'active',
  schedulable: true,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
})

// result builds a valid Sub2API balance response for one provider.
const result = (id: number) => ({
  provider_id: id,
  adapter: 'sub2api',
  observed_at: '2026-08-20T01:00:00Z',
  provider: 'sub2api',
  mode: 'balance',
  unit: 'USD',
  balance: { remaining: 12.5 },
})

// mountView mounts the real provider page with query-capable table stubs.
// Each test owns its wrapper until automatic cleanup or an earlier unmount;
// network responses come from the transport mocks defined above.
const mountView = () =>
  mount(ProvidersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        ProviderTableActions: {
          template: '<div><slot name="beforeCreate" /><slot name="after" /></div>',
        },
        ProviderTableFilters: true,
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
        ProviderUsageCell: ProviderUsageCellStub,
        HelpTooltip: true,
        Icon: true,
      },
    },
  })

// deferred lets a test choose when its simulated transport completes.
// Resolve after the competing refresh or invalidation action, then flush promises
// before asserting the page state and browser cache that the response can affect.
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}

enableAutoUnmount(afterEach)
useProtocolCatalogFixture()

describe('provider cache and incremental refresh lifecycle', () => {
  // Restore timer and visibility overrides after each case so later page mounts observe their ordinary browser scheduling environment.
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  beforeEach(() => {
    localStorage.clear()
    sessionStorage.clear()
    vi.resetAllMocks()
    listProviders.mockResolvedValue({
      items: [provider(1)],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  // Two forced queries for the same provider can finish in reverse order.
  // The latest request owns both the rendered balance and the persisted session snapshot after both responses arrive.
  it('keeps the newer usage response when an earlier query completes last', async () => {
    const older = deferred<ReturnType<typeof result>>()
    const newer = deferred<ReturnType<typeof result>>()
    queryUpstreamUsage.mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="query-upstream"]').trigger('click')
    await wrapper.get('[data-test="query-upstream"]').trigger('click')
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(2)
    newer.resolve({ ...result(1), balance: { remaining: 40 } })
    await flushPromises()
    older.resolve({ ...result(1), balance: { remaining: 10 } })
    await flushPromises()
    expect(wrapper.get('[data-test="upstream-remaining"]').text()).toBe('40')
    expect(sessionStorage.length).toBe(1)
    expect(JSON.parse(sessionStorage.getItem(sessionStorage.key(0)!)!).data.balance.remaining).toBe(
      40,
    )
  })

  // Provider updates invalidate cached usage while a management query may remain pending.
  // Completing that earlier request cannot repopulate the cleared cell or write a session cache entry.
  it('rejects a pending usage response after the provider is updated', async () => {
    const pending = deferred<ReturnType<typeof result>>()
    queryUpstreamUsage.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('[data-test="query-upstream"]').trigger('click')
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    await wrapper.get('[data-test="provider-updated"]').trigger('click')
    pending.resolve(result(1))
    await flushPromises()
    expect(wrapper.get('[data-test="upstream-remaining"]').text()).toBe('')
    expect(sessionStorage.length).toBe(0)
  })

  // A pending bulk response shares the provider's invalidation generation with individual requests.
  // Updating the selected row prevents that bulk result from restoring a balance or session entry.
  it('rejects a bulk usage response after its selected provider is updated', async () => {
    const pending = deferred<{
      usage: Record<string, ReturnType<typeof result>> // Results by provider ID.
      errors: Record<string, never> // Empty batch error map.
    }>()
    queryBatchUpstreamUsage.mockReturnValueOnce(pending.promise)
    const wrapper = mountView()
    await flushPromises()
    await wrapper.get('input[type="checkbox"]').setValue(true)
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    expect(queryBatchUpstreamUsage).toHaveBeenCalledWith([1])
    await wrapper.get('[data-test="provider-updated"]').trigger('click')
    pending.resolve({ usage: { '1': result(1) }, errors: {} })
    await flushPromises()
    expect(wrapper.get('[data-test="upstream-remaining"]').text()).toBe('')
    expect(sessionStorage.length).toBe(0)
  })

  // Incremental refresh sends the last server ETag and reuses unchanged row objects.
  // A subsequent not-modified response leaves the same row mounted while carrying the previous ETag into its request.
  it('reuses row identity and sends the previous ETag on the next refresh', async () => {
    vi.useFakeTimers()
    vi.spyOn(document, 'hidden', 'get').mockReturnValue(false)
    localStorage.setItem(
      'provider-auto-refresh',
      JSON.stringify({ enabled: true, interval_seconds: 5 }),
    )
    const wrapper = mountView()
    await flushPromises()
    const row = wrapper.getComponent(DataTableStub).props('data')[0]
    listWithEtag
      .mockResolvedValueOnce({
        notModified: false,
        etag: 'providers-v1',
        data: { items: [{ ...provider(1) }], total: 1, page: 1, page_size: 20, pages: 1 },
      })
      .mockResolvedValueOnce({ notModified: true, etag: 'providers-v1', data: null })
    // Five countdown ticks precede refresh admission on the sixth tick, exercising the same interval path used by the mounted page.
    await vi.advanceTimersByTimeAsync(6000)
    expect(wrapper.getComponent(DataTableStub).props('data')[0]).toBe(row)
    await vi.advanceTimersByTimeAsync(6000)
    expect(listWithEtag.mock.calls.map((call) => call[3])).toEqual([
      { etag: null },
      { etag: 'providers-v1' },
    ])
    expect(wrapper.getComponent(DataTableStub).props('data')[0]).toBe(row)
  })
})
