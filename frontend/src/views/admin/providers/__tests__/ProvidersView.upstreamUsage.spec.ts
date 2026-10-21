import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import ProvidersView from '../ProvidersView.vue'

// ProvidersView.upstreamUsage.spec.ts checks manual upstream usage queries,
// session cache identity, and batch eligibility on the provider page.
// Controlled responses separate cached observations from network work, and
// deferred batches expose whether all chunks start before any result settles.

// Upstream query spies separate single-provider, batch, and detail requests
// from provider list loading and capture success or failure feedback for
// eligibility cases.
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

// DataTableStub renders selection and usage slots for each response row so
// page callbacks receive the same provider records as the fixtures.
const DataTableStub = {
  props: [
    'data', // provider rows supplied by page
  ],
  template: `
    <div>
      <div v-for="row in data" :key="row.id" :data-row-id="row.id">
        <slot name="cell-select" :row="row" />
        <slot name="cell-usage" :row="row" />
      </div>
    </div>
  `,
}

// ProviderUsageCellStub displays cached remaining balance and exposes manual
// query and provider-updated events, connecting rendered state to the page
// cache controller.
const ProviderUsageCellStub = defineComponent({
  props: [
    'provider', // current provider row record
    'upstreamUsage', // manual upstream query result
    'upstreamUsageError', // manual upstream query failure
    'upstreamUsageLoading', // upstream query loading state
    'requestUpstreamUsage', // page upstream query callback
  ],
  emits: [
    'provider-updated', // provider snapshot requiring page update
  ],
  template: `
    <div :data-usage-provider="provider.id">
      <span data-test="upstream-remaining">{{ upstreamUsage?.balance?.remaining ?? '' }}</span>
      <button data-test="query-upstream" @click="requestUpstreamUsage(provider, { force: true })">query</button>
      <button data-test="provider-updated" @click="$emit('provider-updated', provider)">updated</button>
    </div>
  `,
})

// ProviderBulkActionsBarStub forwards a batch upstream-query event and uses
// the page loading prop to disable its action while batch requests run.
const ProviderBulkActionsBarStub = {
  props: [
    'selectedIds', // currently selected provider identifiers
    'upstreamUsageLoading', // upstream query loading state
  ],
  emits: [
    'query-upstream-usage', // request selected upstream observations
  ],
  template:
    '<button data-test="query-upstream-batch" :disabled="upstreamUsageLoading" @click="$emit(\'query-upstream-usage\')">batch</button>',
}

// provider builds a provider fixture with upstream queries enabled for API Key
// accounts. OAuth fixtures omit endpoint and adapter configuration, allowing
// eligibility tests to distinguish them without separate factory logic.
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

// result returns a successful Sub2API dollar balance for one provider.
const result = (id: number) => ({
  provider_id: id,
  adapter: 'sub2api',
  observed_at: '2026-08-20T01:00:00Z',
  provider: 'sub2api',
  mode: 'balance',
  unit: 'USD',
  balance: { remaining: 12.5 },
})

// mountView renders selectable rows and upstream query controls with the page
// controllers active. Configure list responses before mounting; row stubs
// invoke the same request callback used by the production usage cell.
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

describe('admin ProvidersView upstream usage', () => {
  beforeEach(() => {
    // Reset preferences and session observations together so each cache
    // scenario begins with its own provider identity, adapter selection, and
    // query history.
    localStorage.clear()
    sessionStorage.clear()
    vi.clearAllMocks()
    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getById.mockRejectedValue(new Error('unexpected getById call'))
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  // A derouter observation is cached under its selected adapter and restored
  // on remount. Reopening the provider list reuses that result without sending
  // another management query.
  it('restores cached derouter account balances under the correct adapter identity', async () => {
    const item = provider(1)
    item.extra = { upstream_usage_query: { enabled: true, adapter: 'derouter' } }
    listProviders.mockResolvedValue({ items: [item], total: 1, page: 1, page_size: 20, pages: 1 })
    queryUpstreamUsage.mockResolvedValue({
      ...result(1),
      adapter: 'derouter',
      provider: 'derouter',
      balance: { remaining: 80 },
    })
    const first = mountView()
    await flushPromises()
    await first.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(first.get('[data-test="upstream-remaining"]').text()).toBe('80')
    expect(sessionStorage.key(0)).toContain('derouter')
    first.unmount()
    const second = mountView()
    await flushPromises()
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('80')
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    second.unmount()
  })

  // An API Key row is mounted, queried, and reopened within the cache
  // lifetime. Reopening reuses the balance, an explicit query bypasses it, and
  // a provider update invalidates it.
  it('只在手动查询时访问上游，并在五分钟会话缓存中恢复成功结果', async () => {
    listProviders.mockResolvedValue({
      items: [provider(1)],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    queryUpstreamUsage.mockResolvedValue(result(1))

    const first = mountView()
    await flushPromises()
    expect(queryUpstreamUsage).not.toHaveBeenCalled()

    await first.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    expect(first.get('[data-test="upstream-remaining"]').text()).toBe('12.5')
    expect(sessionStorage.length).toBe(1)
    first.unmount()

    const second = mountView()
    await flushPromises()
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('12.5')

    // Query again while the cached result remains fresh so this request
    // distinguishes forced refresh from ordinary cache restoration.
    await second.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(2)

    await second.get('[data-test="provider-updated"]').trigger('click')
    await flushPromises()
    expect(sessionStorage.length).toBe(0)
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('')
  })

  // A bulk selection combines an eligible API Key row with an OAuth row. Only
  // the key ID reaches the upstream batch endpoint, and the skipped row causes
  // no error.
  it('混合批量选择只查询 API Key 提供商，不把 OAuth 提供商计为失败', async () => {
    listProviders.mockResolvedValue({
      items: [provider(1), provider(2, 'oauth')],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    queryBatchUpstreamUsage.mockResolvedValue({ usage: { '1': result(1) }, errors: {} })

    const wrapper = mountView()
    await flushPromises()
    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    expect(checkboxes).toHaveLength(2)
    await checkboxes[0].setValue(true)
    await checkboxes[1].setValue(true)
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()

    expect(queryBatchUpstreamUsage).toHaveBeenCalledWith([1])
    expect(showSuccess).toHaveBeenCalledWith('admin.providers.upstreamUsage.success')
    expect(showError).not.toHaveBeenCalled()
  })

  // A successful session cache entry is aged beyond five minutes before
  // remount. The stale balance is removed without an automatic replacement
  // request to the upstream service.
  it('清除超过五分钟的成功缓存，不因列表加载自动补查上游', async () => {
    listProviders.mockResolvedValue({
      items: [provider(1)],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    queryUpstreamUsage.mockResolvedValue(result(1))

    const first = mountView()
    await flushPromises()
    await first.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    const cacheKey = sessionStorage.key(0)
    expect(cacheKey).toBeTruthy()
    // Age the stored observation one millisecond beyond the five-minute
    // lifetime so restoration exercises expiry rather than a still-valid cache
    // hit.
    sessionStorage.setItem(
      cacheKey!,
      JSON.stringify({
        data: result(1),
        ts: Date.now() - 5 * 60 * 1000 - 1,
      }),
    )
    first.unmount()

    const second = mountView()
    await flushPromises()
    expect(second.get('[data-test="upstream-remaining"]').text()).toBe('')
    expect(sessionStorage.length).toBe(0)
    expect(queryUpstreamUsage).toHaveBeenCalledTimes(1)
  })

  // Selected rows mix disabled queries, Zhipu pay-as-you-go, OAuth, and
  // supported Chinese vendor modes. The batch includes only eligible IDs and
  // reports success without counting skipped rows as failures.
  it('混合批量选择跳过关闭查询与智谱按量提供商，并查询合法国产平台提供商', async () => {
    const providers = [
      provider(1),
      { ...provider(2), extra: { upstream_usage_query: { enabled: false } } },
      { ...provider(3), platform: 'zhipu', credentials: { provider_mode: 'payg' } },
      provider(4, 'oauth'),
      { ...provider(5), platform: 'zhipu', credentials: { provider_mode: 'coding' }, extra: {} },
      { ...provider(6), platform: 'deepseek', credentials: { provider_mode: 'payg' }, extra: {} },
      { ...provider(7), platform: 'kimi', credentials: { provider_mode: 'coding' }, extra: {} },
    ]
    listProviders.mockResolvedValue({
      items: providers,
      total: providers.length,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    queryBatchUpstreamUsage.mockResolvedValue({
      usage: {
        '1': result(1),
        '5': { ...result(5), provider: 'zhipu', adapter: 'zhipu_coding' },
        '6': { ...result(6), provider: 'deepseek', adapter: 'deepseek_balance' },
        '7': { ...result(7), provider: 'kimi', adapter: 'kimi_coding' },
      },
      errors: {},
    })
    const wrapper = mountView()
    await flushPromises()
    for (const checkbox of wrapper.findAll('input[type="checkbox"]')) await checkbox.setValue(true)
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()
    expect(queryBatchUpstreamUsage).toHaveBeenCalledTimes(1)
    expect(queryBatchUpstreamUsage).toHaveBeenCalledWith([1, 5, 6, 7])
    expect(showError).not.toHaveBeenCalled()
    expect(showSuccess).toHaveBeenCalledWith('admin.providers.upstreamUsage.success')
    wrapper.unmount()
  })

  // The row stub invokes the query callback for disabled or unsupported
  // providers. Callback guards prevent both upstream requests and session
  // cache writes even when a control is present.
  it.each([
    { ...provider(2), extra: { upstream_usage_query: { enabled: false } } },
    { ...provider(3), platform: 'zhipu', credentials: { provider_mode: 'payg' } },
    provider(4, 'oauth'),
  ])('单次查询入口也拒绝无资格提供商 $id', async (blocked) => {
    listProviders.mockResolvedValue({
      items: [blocked],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    const wrapper = mountView()
    await flushPromises()
    // The stub invokes the callback even for blocked rows, exposing request
    // eligibility guards independently of production button visibility.
    await wrapper.get('[data-test="query-upstream"]').trigger('click')
    await flushPromises()
    expect(queryUpstreamUsage).not.toHaveBeenCalled()
    expect(sessionStorage.length).toBe(0)
    wrapper.unmount()
  })

  // Selecting 205 eligible providers produces batches of 100, 100, and five.
  // All requests start while their results remain pending, exposing serial
  // dispatch as a regression.
  it('把 API Key 批量请求按 100 个提供商分块并并发执行', async () => {
    const providers = Array.from({ length: 205 }, (_, index) => provider(index + 1))
    listProviders.mockResolvedValue({
      items: providers,
      total: 205,
      page: 1,
      page_size: 500,
      pages: 1,
    })
    // Keep every batch response pending until dispatch counts are asserted;
    // resolving an earlier chunk would allow sequential code to satisfy the
    // same counts.
    const releases: Array<() => void> = []
    queryBatchUpstreamUsage.mockImplementation(
      (ids: number[]) =>
        new Promise((resolve) => {
          releases.push(() =>
            resolve({
              usage: Object.fromEntries(ids.map((id) => [String(id), result(id)])),
              errors: {},
            }),
          )
        }),
    )

    const wrapper = mountView()
    await flushPromises()
    // Dispatch all selection events before Vue flushes, preventing 205
    // complete table renders from dominating this request-batching regression
    // under load.
    await Promise.all(
      wrapper.findAll('input[type="checkbox"]').map((checkbox) => checkbox.setValue(true)),
    )
    await wrapper.get('[data-test="query-upstream-batch"]').trigger('click')
    await flushPromises()

    // All batches must start while every result remains pending; resolving one
    // early would allow a serial implementation to satisfy the request counts.
    expect(queryBatchUpstreamUsage).toHaveBeenCalledTimes(3)
    expect(queryBatchUpstreamUsage).toHaveBeenNthCalledWith(
      1,
      Array.from({ length: 100 }, (_, index) => index + 1),
    )
    expect(queryBatchUpstreamUsage).toHaveBeenNthCalledWith(
      2,
      Array.from({ length: 100 }, (_, index) => index + 101),
    )
    expect(queryBatchUpstreamUsage).toHaveBeenNthCalledWith(3, [201, 202, 203, 204, 205])
    releases.forEach((release) => release())
    await flushPromises()
    wrapper.unmount()
  })
})

useProtocolCatalogFixture()
