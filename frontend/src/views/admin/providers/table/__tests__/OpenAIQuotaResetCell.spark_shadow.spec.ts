import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpenAIQuotaResetCell from '../OpenAIQuotaResetCell.vue'
import type { Provider } from '@/api/admin/providers.types'
import { refreshOpenAIQuota } from '@/api/admin/providers'

// OpenAIQuotaResetCell.spark_shadow.spec.ts checks quota query controls and
// reset-credit snapshots for OpenAI parent and shadow providers. Cached
// expiration details distinguish usable credits from unknown counts, while
// live query results remain visible when persistence fails.

vi.mock('@/api/admin/providers', () => ({
  refreshOpenAIQuota: vi.fn(),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// makeProvider builds an active OpenAI OAuth account for quota-query
// assertions. Overrides replace complete fields after defaults, allowing
// parent identity and credit snapshots to vary independently between mounts.
function makeProvider(overrides: Partial<Provider>): Provider {
  return {
    id: 1,
    name: 'acc',
    platform: 'openai',
    type: 'oauth',
    proxy_id: null,
    concurrency: 3,
    priority: 50,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: false,
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

beforeEach(() => {
  vi.mocked(refreshOpenAIQuota).mockReset()
})

describe('OpenAIQuotaResetCell — fork 查询-only 语义', () => {
  // An OpenAI shadow provider has a nonempty parent identifier. Its quota cell
  // exposes one query button with the load tooltip and no reset action.
  it('影子提供商(parent_provider_id 非空)只展示查询按钮，不展示重置入口', () => {
    const provider = makeProvider({ parent_provider_id: 100 })
    const wrapper = mount(OpenAIQuotaResetCell, { props: { provider } })

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('title')).toBe('admin.providers.openaiQuotaReset.countTooltipLoad')
    wrapper.unmount()
  })

  // An OpenAI provider has no parent identifier. Its quota cell still exposes
  // only the query button with the load tooltip, matching the shadow row's
  // action scope.
  it('普通提供商(无 parent_provider_id)同样只展示查询按钮', () => {
    const provider = makeProvider({ parent_provider_id: null })
    const wrapper = mount(OpenAIQuotaResetCell, { props: { provider } })

    const buttons = wrapper.findAll('button')
    expect(buttons).toHaveLength(1)
    expect(buttons[0].attributes('title')).toBe('admin.providers.openaiQuotaReset.countTooltipLoad')
    wrapper.unmount()
  })

  // Stored credit details contain two future expirations and a known count.
  // Mounting restores the count and expandable expiry details without calling
  // the quota refresh endpoint.
  it('从提供商 extra 恢复未过期的次数和到期明细', () => {
    const provider = makeProvider({
      extra: {
        codex_reset_credit_snapshot: {
          available_count: 2,
          credits: [{ expires_at: '2099-07-05T04:05:06Z' }, { expires_at: '2099-07-03T04:05:06Z' }],
        },
      },
    })

    const wrapper = mount(OpenAIQuotaResetCell, { props: { provider } })

    expect(refreshOpenAIQuota).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('admin.providers.openaiQuotaReset.count2')
    expect(wrapper.text()).toContain('admin.providers.openaiQuotaReset.expiresAt')
    expect(wrapper.text()).toContain('+1')
    // The additional button expands expiration details; it is distinct from a
    // quota reset operation.
    expect(wrapper.findAll('button')).toHaveLength(2)
    wrapper.unmount()
  })

  // Every credit expiration in the cached snapshot is in the past. The cell
  // treats the count as unknown and shows only the query action without expiry
  // details.
  it('缓存次数全部过期时视为未知', () => {
    const provider = makeProvider({
      extra: {
        codex_reset_credit_snapshot: {
          available_count: 1,
          credits: [{ expires_at: '2020-07-03T04:05:06Z' }],
        },
      },
    })

    const wrapper = mount(OpenAIQuotaResetCell, { props: { provider } })

    expect(wrapper.text()).not.toContain('admin.providers.openaiQuotaReset.count1')
    expect(wrapper.text()).not.toContain('admin.providers.openaiQuotaReset.expiresAt')
    expect(wrapper.findAll('button')).toHaveLength(1)
    wrapper.unmount()
  })

  // A live quota response reports two credits but failed cache persistence.
  // The cell retains the returned count and displays a persistence warning
  // alongside its query action.
  it('快照持久化失败时仍显示实时次数并告警', async () => {
    vi.mocked(refreshOpenAIQuota).mockResolvedValue({
      rate_limit_reset_credits: { available_count: 2 },
      fetched_at: 1770000000,
      cache_persisted: false,
    })
    const wrapper = mount(OpenAIQuotaResetCell, {
      props: { provider: makeProvider({ parent_provider_id: null }) },
    })

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(refreshOpenAIQuota).toHaveBeenCalledWith(1)
    expect(wrapper.text()).toContain('admin.providers.openaiQuotaReset.count2')
    expect(wrapper.text()).toContain('admin.providers.openaiQuotaReset.refreshCachePersistFailed')
    expect(wrapper.findAll('button')).toHaveLength(1)
    wrapper.unmount()
  })
})
