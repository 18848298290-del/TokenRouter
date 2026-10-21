import { defineComponent } from 'vue'
import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import ProviderUpstreamUsageCell from '../ProviderUpstreamUsageCell.vue'

// ProviderUpstreamUsageCell.spec.ts checks upstream usage presentation, manual
// query events, and provider eligibility. Adapter fixtures distinguish wallet
// balances, token quotas, subscriptions, and Chinese vendor windows while
// local monitor snapshots render without causing network work on mount.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) => {
        if (!params) return key
        return `${key}:${Object.values(params).join('|')}`
      },
    }),
  }
})

// IconStub exposes icon names as data attributes so query and error
// presentation can be inspected without rendering the shared icon
// implementation.
const IconStub = defineComponent({
  props: [
    'name', // requested shared icon name
  ],
  template: '<span :data-icon="name" />',
})

// UsageProgressBarStub exposes label, utilization, and reset props as text,
// allowing upstream limit conversion to be checked independently of
// progress-bar rendering.
const UsageProgressBarStub = defineComponent({
  props: [
    'label', // usage window display label
    'utilization', // observed usage percentage value
    'resetsAt', // usage window reset timestamp
  ],
  template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ resetsAt }}</div>',
})

// provider builds an OpenAI API Key fixture for upstream presentation tests.
// The supplied extra object is attached directly, allowing callers to control
// query eligibility or saved monitor snapshots.
const provider = (extra: Record<string, unknown> = {}) =>
  ({
    id: 17,
    name: 'API key',
    platform: 'openai',
    type: 'apikey',
    extra,
    credentials: { base_url: 'https://gateway.example.test' },
  }) as any

// mountCell renders upstream usage with an eligible API Key provider by
// default. Supplied props override that provider and other inputs; bar and
// icon stubs expose values without their visual implementations.
const mountCell = (props: Record<string, unknown> = {}) =>
  mount(ProviderUpstreamUsageCell, {
    props: { provider: provider(), ...props },
    global: { stubs: { Icon: IconStub, UsageProgressBar: UsageProgressBarStub } },
  })

describe('ProviderUpstreamUsageCell', () => {
  // An eligible upstream cell mounts with a request callback and no result.
  // Mounting leaves it uncalled, and clicking the query button requests a
  // forced refresh.
  it('只在手动点击时触发强制查询', async () => {
    const request = vi.fn()
    const wrapper = mountCell({ request })

    expect(request).not.toHaveBeenCalled()
    await wrapper.find('button').trigger('click')
    expect(request).toHaveBeenCalledWith(expect.objectContaining({ id: 17 }), { force: true })
  })

  // A result contains balance, account limits, subscription limits, and an
  // expiration. The cell renders the remaining balance, six progress bars,
  // plan, and expiry with responsive subscription alignment.
  it('展示余额、周期限额、订阅和到期时间', () => {
    const wrapper = mountCell({
      result: {
        provider_id: 17,
        adapter: 'sub2api',
        observed_at: '2026-08-20T00:00:00Z',
        provider: 'sub2api',
        mode: 'quota',
        unit: 'USD',
        balance: { used: 25, total: 100, remaining: 75 },
        limits: [
          { name: '5h', used: 2, limit: 10, remaining: 8, reset_at: '2026-08-20T05:00:00Z' },
          { name: '1d', used: 3, limit: 20, remaining: 17 },
          { name: '7d', used: 4, limit: 30, remaining: 26 },
        ],
        subscription: {
          plan_name: 'Pro',
          unlimited: false,
          remaining: 8,
          limits: [
            { name: 'daily', used: 1, limit: 10, remaining: 9 },
            { name: 'weekly', used: 2, limit: 20, remaining: 18 },
            { name: 'monthly', used: 3, limit: 30, remaining: 27 },
          ],
        },
        expires_at: '2026-09-01T00:00:00Z',
      },
    })

    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.remainingLine:75 USD')
    expect(wrapper.text()).not.toContain('admin.providers.upstreamUsage.balanceLine')
    expect(wrapper.text()).toContain('5h|20|2026-08-20T05:00:00Z')
    expect(wrapper.findAll('.usage-bar')).toHaveLength(6)
    expect(wrapper.text()).toContain('Pro')
    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.expiresAt')
    expect(wrapper.get('[data-testid="upstream-subscription-row"]').classes()).toContain(
      'justify-end',
    )
    expect(wrapper.get('[data-testid="upstream-subscription-row"]').classes()).toContain(
      'lg:justify-start',
    )
  })

  // An upstream query is loading within a provider card. Skeleton content uses
  // right alignment on narrow layouts and switches to left alignment at the
  // large breakpoint.
  it('查询中的骨架在移动端卡片里与结果一样靠右', () => {
    const wrapper = mountCell({ loading: true })
    const skeleton = wrapper.get('[data-testid="upstream-usage-skeleton"]')
    expect(skeleton.classes()).toContain('items-end')
    expect(skeleton.classes()).toContain('lg:items-start')
  })

  // A timeout result is displayed before an unlimited subscription replaces
  // it. The error retains a forced retry action, and the replacement result
  // receives the unlimited-plan label.
  it('把无限量和错误状态分别显示，并保留重试按钮', async () => {
    const request = vi.fn()
    const wrapper = mountCell({
      request,
      error: { code: 'UPSTREAM_USAGE_TIMEOUT', message: 'timeout' },
    })
    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.errors.UPSTREAM_USAGE_TIMEOUT')
    await wrapper.find('button').trigger('click')
    expect(request).toHaveBeenCalledWith(expect.anything(), { force: true })

    await wrapper.setProps({
      error: null,
      result: {
        provider_id: 17,
        adapter: 'sub2api',
        observed_at: '2026-08-20T00:00:00Z',
        usage: {
          provider: 'sub2api',
          mode: 'subscription',
          unit: 'USD',
          subscription: { plan_name: 'Unlimited', unlimited: true, expires_at: null },
        },
      },
    })
    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.unlimited:Unlimited')
  })

  // A New API response reports a wallet balance and a much larger token quota.
  // The cell separates their presentation and keeps the shared active-query
  // button styling.
  it('使用 OAuth 同款查询按钮，并把 New API 钱包与 Token 限额分开展示', () => {
    const wrapper = mountCell({
      result: {
        provider_id: 17,
        adapter: 'new_api',
        observed_at: '2026-08-21T07:57:22Z',
        provider: 'new_api',
        mode: 'balance',
        unit: 'USD',
        balance: { remaining: 1264.28 },
        limits: [
          { name: 'token_quota', used: 3954.5655, limit: 100000000, remaining: 99996045.4345 },
        ],
        subscription: {
          plan_name: 'New API',
          unlimited: false,
          remaining: 99996045.4345,
        },
      },
    })

    const button = wrapper.find('button')
    expect(button.text()).toContain('admin.providers.usageWindow.activeQuery')
    expect(button.classes()).toContain('font-medium')
    expect(wrapper.text()).not.toContain('admin.providers.upstreamUsage.source')
    expect(wrapper.text()).not.toContain('admin.providers.upstreamUsage.localSource')
    expect(wrapper.findAll('.usage-bar')).toHaveLength(1)
    expect(wrapper.text()).toContain('1,264.28 USD')
    expect(wrapper.text()).toContain('100M USD')
    expect(wrapper.text()).not.toContain('admin.providers.upstreamUsage.subscriptionRemaining')
    expect(wrapper.text()).not.toContain('1,264.28 USD / 100M USD')
  })

  // A New API result contains a wallet balance below two thousand dollars.
  // Formatting retains cents in the displayed balance instead of compressing
  // it into a tiny million-unit value.
  it('New API 小额余额保留美元精度，不压缩成百万单位', () => {
    const wrapper = mountCell({
      result: {
        provider_id: 17,
        adapter: 'new_api',
        observed_at: '2026-08-21T07:57:22Z',
        provider: 'new_api',
        mode: 'quota',
        unit: 'USD',
        balance: { used: 171.76, total: 1436.04, remaining: 1264.28 },
        limits: [{ name: 'token_quota', used: 171.76, limit: 1436.04, remaining: 1264.28 }],
        subscription: { plan_name: 'Default Token', unlimited: false, remaining: 1264.28 },
      },
    })

    expect(wrapper.text().replaceAll(',', '')).toContain('1264.28 USD')
    expect(wrapper.text()).not.toContain('0.001M USD')
  })

  // A New API account has an unlimited token subscription and a finite wallet
  // balance. The cell shows the actual wallet amount and unlimited label
  // without a quota sentinel.
  it('New API 无限量 Token 仍显示真实钱包余额，不显示 quota 哨兵值', () => {
    const wrapper = mountCell({
      result: {
        provider_id: 17,
        adapter: 'new_api',
        observed_at: '2026-08-21T07:57:22Z',
        provider: 'new_api',
        mode: 'balance',
        unit: 'USD',
        balance: { remaining: 1264.28 },
        subscription: { plan_name: 'tf', unlimited: true },
      },
    })

    expect(wrapper.text().replaceAll(',', '')).toContain('1264.28 USD')
    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.unlimited:tf')
    expect(wrapper.text()).not.toContain('100000000')
  })

  // A Zivv response combines wallet totals with a smaller key quota and plan.
  // The cell presents remaining wallet balance, plan name, and total-limit
  // detail without duplicate subscription remaining text.
  it('展示 Zivv 钱包余额、累计用量和 Key 限额', () => {
    const wrapper = mountCell({
      result: {
        provider_id: 17,
        adapter: 'zivv',
        observed_at: '2026-08-21T16:49:46Z',
        provider: 'zivv',
        mode: 'balance',
        unit: 'USD',
        balance: { used: 22460.664473, total: 23361.154657, remaining: 900.490184 },
        limits: [{ name: 'key_quota', used: 206.4, limit: 1000, remaining: 793.6 }],
        subscription: { plan_name: 'cc b', unlimited: false, remaining: 793.6 },
      },
    })

    expect(wrapper.text().replaceAll(',', '')).toContain('900.49 USD')
    expect(wrapper.text()).toContain('cc b')
    expect(wrapper.text()).toContain('admin.providers.upstreamUsage.totalLimit')
    expect(wrapper.text()).not.toContain('admin.providers.upstreamUsage.subscriptionRemaining')
  })

  // Provider configuration explicitly disables upstream usage queries. The
  // upstream data container and query button are both absent when the cell is
  // mounted for that provider.
  it('关闭查询时隐藏上游数据和查询按钮', () => {
    const wrapper = mountCell({ provider: provider({ upstream_usage_query: { enabled: false } }) })
    expect(wrapper.find('[data-testid="provider-upstream-usage"]').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })

  // A disabled provider still receives a previously fetched upstream result.
  // Configuration takes precedence, so neither the result container nor its
  // old balance is displayed.
  it('配置关闭后不展示旧的上游结果', () => {
    const wrapper = mountCell({
      provider: provider({ upstream_usage_query: { enabled: false } }),
      result: {
        provider_id: 17,
        adapter: 'sub2api',
        observed_at: '2026-08-20T00:00:00Z',
        provider: 'sub2api',
        mode: 'balance',
        unit: 'USD',
        balance: { remaining: 10 },
      },
    })
    expect(wrapper.find('[data-testid="provider-upstream-usage"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('10 USD')
  })

  // The cell first receives a Kimi percentage window and then DeepSeek
  // currency balances. It renders a percentage bar without a unit suffix and
  // preserves both currency labels.
  it('展示 CN 百分比窗口和 DeepSeek 多币种余额', async () => {
    const wrapper = mountCell({
      provider: {
        ...provider(),
        platform: 'kimi',
        credentials: { provider_mode: 'coding' },
      },
      result: {
        provider_id: 17,
        adapter: 'kimi_coding',
        observed_at: '2026-08-23T00:00:00Z',
        provider: 'kimi',
        mode: 'limits',
        unit: 'PERCENT',
        limits: [{ name: '5h', used: 75, limit: 100, remaining: 25 }],
      },
    })
    expect(wrapper.text()).toContain('5h|75|')
    expect(wrapper.text()).not.toContain('PERCENT')

    await wrapper.setProps({
      provider: {
        ...provider(),
        platform: 'deepseek',
        credentials: { provider_mode: 'payg' },
      },
      result: {
        provider_id: 17,
        adapter: 'deepseek_balance',
        observed_at: '2026-08-23T00:00:00Z',
        provider: 'deepseek',
        mode: 'balance',
        unit: 'CNY',
        balance: { remaining: 12.5 },
        balances: [
          { currency: 'CNY', remaining: 12.5 },
          { currency: 'USD', remaining: 1.25 },
        ],
      },
    })
    expect(wrapper.text()).toContain('CNY 12.5 · USD 1.25')
  })

  // A Kimi provider carries a saved monitoring balance with no manual result.
  // The cell displays that observation on mount without invoking its upstream
  // request callback.
  it('仅展示监控快照且不会在挂载时查询上游', () => {
    const request = vi.fn()
    const wrapper = mountCell({
      request,
      provider: {
        ...provider(),
        platform: 'kimi',
        credentials: { provider_mode: 'payg' },
        extra: {
          cn_usage_monitor_snapshot: {
            version: 1,
            adapter: 'kimi_balance',
            provider: 'kimi',
            mode: 'balance',
            unit: 'CNY',
            balance: { remaining: 9.5 },
            observed_at: '2026-08-23T00:00:00Z',
          },
        },
      },
    })
    expect(wrapper.text()).toContain('9.5 CNY')
    expect(request).not.toHaveBeenCalled()
  })

  // A Zhipu API Key account uses pay-as-you-go mode. The upstream data region
  // and query button are both absent for that unsupported query mode.
  it('智谱 payg 明确显示不支持且不提供查询按钮', () => {
    const wrapper = mountCell({
      provider: {
        ...provider(),
        platform: 'zhipu',
        credentials: { provider_mode: 'payg' },
      },
    })
    expect(wrapper.find('[data-testid="provider-upstream-usage"]').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
