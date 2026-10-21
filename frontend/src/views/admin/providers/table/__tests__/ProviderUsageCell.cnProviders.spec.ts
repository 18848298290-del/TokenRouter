import { createPinia } from 'pinia'
import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Provider, UpstreamUsageQueryResult, WindowStats } from '@/api/admin/providers.types'
import ProviderUsageCell from '../ProviderUsageCell.vue'

// ProviderUsageCell.cnProviders.spec.ts checks the combined local and upstream
// usage layout for Chinese vendors and a general API Key control. Shared
// fixtures exercise query states, disabling, and saved observations while DOM
// order assertions retain local statistics and quotas.

// getUsage records generic usage requests so Chinese provider cases can verify
// that upstream display and local refresh signals do not invoke that path.
const { getUsage } = vi.hoisted(() => ({ getUsage: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: { providers: { getUsage } } }))
vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual('vue-i18n')),
  useI18n: () => ({
    t: (key: string, params?: Record<string, unknown>) =>
      params ? `${key}:${Object.values(params).join('|')}` : key,
  }),
}))

// cases includes supported Chinese vendor modes and an OpenAI API Key control
// so shared local statistics and query placement are checked across branches.
const cases = [
  { platform: 'deepseek', mode: 'payg' },
  { platform: 'kimi', mode: 'payg' },
  { platform: 'kimi', mode: 'coding' },
  { platform: 'zhipu', mode: 'coding' },
  { platform: 'openai', mode: 'payg' },
] as const

// todayStats supplies nonzero request, token, and cost values to every mounted
// cell, making disappearance of local statistics visible during upstream state
// changes.
const todayStats: WindowStats = {
  requests: 123,
  tokens: 456,
  cost: 1.25,
  standard_cost: 1.25,
  user_cost: 0.5,
}
// querySelector targets the shared upstream query action by its accessible
// label, allowing each state to assert that exactly one eligible entry
// remains.
const querySelector = 'button[aria-label="admin.providers.upstreamUsage.query"]'

// makeProvider builds an API Key fixture for a platform and provider mode. It
// includes daily, weekly, and total local quotas so upstream-state transitions
// can be checked against persistent local information.
function makeProvider(platform: Provider['platform'], mode: string): Provider {
  return {
    id: 1948,
    name: '用量布局测试',
    platform,
    type: 'apikey',
    credentials: { provider_mode: mode, api_protocol: 'adaptive' },
    extra: {},
    status: 'active',
    schedulable: true,
    quota_daily_limit: 10,
    quota_daily_used: 2,
    quota_weekly_limit: 20,
    quota_weekly_used: 5,
    quota_limit: 100,
    quota_used: 10,
  } as Provider
}

// makeResult builds an observation matching the fixture platform and mode.
// Coding accounts receive percentage limits, other accounts receive a currency
// balance, and the OpenAI control uses the Sub2API adapter.
function makeResult(provider: Provider): UpstreamUsageQueryResult {
  const coding = provider.credentials?.provider_mode === 'coding'
  return {
    provider_id: provider.id,
    adapter:
      provider.platform === 'openai'
        ? 'sub2api'
        : coding
          ? `${provider.platform}_coding`
          : `${provider.platform}_balance`,
    provider: provider.platform,
    observed_at: '2026-09-09T00:00:00Z',
    mode: coding ? 'limits' : 'balance',
    unit: coding ? 'PERCENT' : 'CNY',
    ...(coding
      ? { limits: [{ name: '5h', used: 25, limit: 100, remaining: 75 }] }
      : { balance: { remaining: 9.5 } }),
  }
}

// mountCell renders the complete usage cell with shared local statistics and a
// query spy by default. A fresh Pinia instance and bar stub expose local data
// independently of upstream response state.
function mountCell(provider: Provider, request = vi.fn()) {
  return mount(ProviderUsageCell, {
    props: {
      provider,
      todayStats,
      requestUpstreamUsage: request /* page upstream query callback */,
    },
    global: {
      plugins: [createPinia()],
      stubs: {
        Icon: true,
        UsageProgressBar: {
          props: [
            'label', // usage window display label
            'utilization', // observed usage percentage value
          ],
          template:
            '<div class="usage-bar" :data-label="label">{{ label }}|{{ utilization }}</div>',
        },
      },
    },
  })
}

describe('国产平台提供商完整用量布局', () => {
  beforeEach(() => vi.clearAllMocks())

  // Each supported platform and mode progresses through idle, loading,
  // success, failure, and disabled states. One query action remains correctly
  // ordered after local statistics and quotas until disabled.
  it.each(cases)(
    '$platform/$mode 在各查询状态只保留一个入口并展示本地数据',
    async ({ platform, mode }) => {
      const provider = makeProvider(platform, mode)
      const request = vi.fn()
      const wrapper = mountCell(provider, request)
      await flushPromises()
      expect(getUsage).not.toHaveBeenCalled()
      expect(request).not.toHaveBeenCalled()
      expect(wrapper.findAll(querySelector)).toHaveLength(1)
      expect(wrapper.get('[data-stat="requests"]').text()).toContain('123')
      expect(wrapper.get('[data-stat="cost"]').text()).toContain('$1.25')
      expect(wrapper.get('[data-stat="userCost"]').text()).toContain('$0.50')
      expect(wrapper.get('[data-label="1d"]').text()).toBe('1d|20')
      expect(wrapper.get('[data-label="7d"]').text()).toBe('7d|25')
      expect(wrapper.get('[data-label="total"]').text()).toBe('total|10')

      await wrapper.get(querySelector).trigger('click')
      expect(request).toHaveBeenCalledTimes(1)
      expect(request).toHaveBeenCalledWith(provider, { force: true })
      await wrapper.setProps({ upstreamUsageLoading: true })
      expect(wrapper.findAll(querySelector)).toHaveLength(1)
      expect(wrapper.get(querySelector).attributes('disabled')).toBeDefined()
      await wrapper.get(querySelector).trigger('click')
      expect(request).toHaveBeenCalledTimes(1)

      await wrapper.setProps({ upstreamUsageLoading: false, upstreamUsage: makeResult(provider) })
      const upstream = wrapper.get('[data-testid="provider-upstream-usage"]')
      expect(upstream.text()).toContain(mode === 'coding' ? '5h|25' : '9.5 CNY')
      const stats = wrapper.get('[data-stat="requests"]')
      const quota = wrapper.get('[data-label="1d"]')
      const button = wrapper.get(querySelector)
      // Compare actual DOM order across the upstream result, local statistics,
      // quotas, and action so vendor branches cannot silently bypass local
      // information.
      for (const [before, after] of [
        [upstream, stats],
        [stats, quota],
        [quota, button],
      ]) {
        expect(
          before.element.compareDocumentPosition(after.element) & Node.DOCUMENT_POSITION_FOLLOWING,
        ).toBeTruthy()
      }
      await wrapper.setProps({
        upstreamUsage: null,
        upstreamUsageError: { code: 'UPSTREAM_USAGE_TIMEOUT' },
      })
      expect(wrapper.text()).toContain(
        'admin.providers.upstreamUsage.errors.UPSTREAM_USAGE_TIMEOUT',
      )
      expect(wrapper.findAll(querySelector)).toHaveLength(1)
      await wrapper.get(querySelector).trigger('click')
      expect(request).toHaveBeenCalledTimes(2)
      expect(request).toHaveBeenLastCalledWith(provider, { force: true })

      await wrapper.setProps({
        provider: { ...provider, extra: { upstream_usage_query: { enabled: false } } },
      })
      expect(wrapper.findAll(querySelector)).toHaveLength(0)
      expect(wrapper.find('[data-testid="provider-upstream-usage"]').exists()).toBe(false)
      expect(wrapper.get('[data-stat="requests"]').text()).toContain('123')
      expect(wrapper.get('[data-label="total"]').text()).toBe('total|10')
      wrapper.unmount()
    },
  )

  // Zhipu pay-as-you-go or missing mode makes upstream queries unavailable.
  // Local statistics and quota bars remain visible while both usage request
  // paths stay uncalled.
  it.each(['payg', undefined])('智谱模式 %s 隐藏上游查询区域并展示本地统计和配额', async (mode) => {
    const provider = makeProvider('zhipu', mode ?? '')
    if (mode === undefined && provider.credentials) delete provider.credentials.provider_mode
    const request = vi.fn()
    const wrapper = mountCell(provider, request)
    await flushPromises()
    expect(wrapper.find('[data-testid="provider-upstream-usage"]').exists()).toBe(false)
    expect(wrapper.findAll(querySelector)).toHaveLength(0)
    expect(wrapper.get('[data-stat="requests"]').text()).toContain('123')
    expect(wrapper.get('[data-label="1d"]').text()).toBe('1d|20')
    expect(request).not.toHaveBeenCalled()
    expect(getUsage).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  // A DeepSeek monitor snapshot is present before mounting and a manual result
  // arrives later. Refresh signals cause no upstream query, and the manual
  // result replaces the displayed balance.
  it('监控快照和手动结果共用展示，挂载及刷新均不请求上游', async () => {
    const provider = makeProvider('deepseek', 'payg')
    provider.extra = { cn_usage_monitor_snapshot: { version: 1, ...makeResult(provider) } }
    const request = vi.fn()
    const wrapper = mountCell(provider, request)
    await flushPromises()
    expect(wrapper.text()).toContain('9.5 CNY')
    expect(wrapper.findAll(querySelector)).toHaveLength(1)
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()
    expect(request).not.toHaveBeenCalled()
    expect(getUsage).not.toHaveBeenCalled()
    await wrapper.setProps({
      upstreamUsage: { ...makeResult(provider), balance: { remaining: 7 } },
    })
    expect(wrapper.text()).toContain('7 CNY')
    expect(wrapper.text()).not.toContain('9.5 CNY')
    wrapper.unmount()
  })

  // A supported provider has no local statistics and all local quota limits
  // are zero. The cell still exposes one upstream query button without
  // rendering quota bars.
  it('完全没有统计和配额时仍只有一个查询按钮', async () => {
    const wrapper = mountCell(makeProvider('deepseek', 'payg'))
    await wrapper.setProps({
      todayStats: null,
      provider: {
        ...makeProvider('deepseek', 'payg'),
        quota_daily_limit: 0,
        quota_weekly_limit: 0,
        quota_limit: 0,
      },
    })
    expect(wrapper.findAll(querySelector)).toHaveLength(1)
    expect(wrapper.findAll('.usage-bar')).toHaveLength(0)
    wrapper.unmount()
  })
})
