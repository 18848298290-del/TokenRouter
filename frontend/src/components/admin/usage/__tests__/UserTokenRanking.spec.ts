import { afterEach, describe, expect, it, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createPinia } from 'pinia'

import UserTokenRanking from '../UserTokenRanking.vue'

// UserTokenRanking.spec.ts tests ranking queries, user selection, and loading
// presentation. A substituted breakdown API supplies controlled users and pending
// responses, while the real ranking component renders costs using configurable balance
// symbols.

// getUserBreakdown records ranking filters and supplies populated, pending, empty, or
// rejected responses for request and loading cases.
const getUserBreakdown = vi.fn()

vi.mock('@/api/admin/dashboard', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/api/admin/dashboard')>()),
  getUserBreakdown: (...args: unknown[]) => getUserBreakdown(...args),
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

// item creates a user-ranking fixture with the supplied identity and token count.
const item = (id: number, tokens: number) => ({
  user_id: id,
  email: `u${id}@test.com`,
  requests: 1,
  input_tokens: tokens,
  output_tokens: 0,
  cache_tokens: 0,
  total_tokens: tokens,
  actual_cost: 0.5,
})

// mountRanking renders the ranking with default dates and caller prop overrides. It creates
// a fresh Pinia instance and stubs the limit selector while preserving the ranking table
// and request watcher.
const mountRanking = (props: Record<string, unknown> = {}) =>
  mount(UserTokenRanking, {
    props: {
      startDate: '2026-07-01',
      endDate: '2026-07-08',
      filters: {},
      ...props,
    },
    global: { plugins: [createPinia()], stubs: { Select: true } },
  })

// originalConfig retains the application configuration restored after each test changes the
// balance symbol used by ranking charges.
const originalConfig = window.__APP_CONFIG__

describe('UserTokenRanking', () => {
  beforeEach(() => {
    getUserBreakdown.mockReset()
    getUserBreakdown.mockResolvedValue({ users: [item(1, 100), item(2, 50)] })
  })

  // Restore the shared public configuration after symbol overrides so later ranking
  // cases format charges using their own expected balance settings.
  afterEach(() => {
    window.__APP_CONFIG__ = originalConfig
  })

  // The site config supplies a custom balance symbol for ranking charges. The test protects
  // displaying actual user cost with that symbol and four decimal places.
  it('消费列用配置的余额符号显示用户实际扣费', async () => {
    window.__APP_CONFIG__ = { ...originalConfig, balance_unit_symbol: '🍥' }
    const wrapper = mountRanking()
    await flushPromises()

    const costCell = wrapper.findAll('tbody tr')[0].findAll('td').at(-1)!
    expect(costCell.text()).toBe('🍥0.5000')
    wrapper.unmount()
  })

  // The ranking mounts with shared filters and a model selection. The test protects its
  // query parameters and emitting user identity plus email when a row is selected.
  it('loads on mount with shared filters and emits select-user with id + email on row click', async () => {
    const wrapper = mountRanking({ filters: { group_id: 3 }, model: 'claude-fable-5' })
    await flushPromises()

    expect(getUserBreakdown).toHaveBeenCalledWith(
      expect.objectContaining({
        group_id: 3,
        model: 'claude-fable-5',
        start_date: '2026-07-01',
        end_date: '2026-07-08',
        sort_by: 'total_tokens',
        limit: 50,
      }),
    )

    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)

    await rows[0].trigger('click')
    expect(wrapper.emitted('select-user')![0]).toEqual([1, 'u1@test.com'])
  })

  // The parent replaces shared filters after the initial ranking finishes. The test
  // protects issuing another breakdown query with the newly selected user identifier
  // included.
  it('reloads when shared filters change', async () => {
    const wrapper = mountRanking()
    await flushPromises()
    expect(getUserBreakdown).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ filters: { user_id: 9 } })
    await flushPromises()

    expect(getUserBreakdown).toHaveBeenCalledTimes(2)
    expect(getUserBreakdown).toHaveBeenLastCalledWith(expect.objectContaining({ user_id: 9 }))
  })

  // A ranking request remains pending before returning one user. The test protects skeleton
  // column counts and accessible loading state, then restores clickable populated rows
  // after completion.
  it('等待数据时保留表头和列数，完成后恢复可点击的数据行', async () => {
    let finish!: (value: {
      users: ReturnType<typeof item>[] // returned ranking user records
    }) => void
    getUserBreakdown.mockReturnValue(
      new Promise((resolve) => {
        finish = resolve
      }),
    )
    const wrapper = mountRanking()
    await flushPromises()

    const headerCount = wrapper.findAll('thead th').length
    expect(wrapper.get('table').attributes('aria-busy')).toBe('true')
    expect(wrapper.get('tbody').attributes('aria-label')).toBe('common.loading')
    expect(wrapper.findAll('tbody tr')).toHaveLength(5)
    expect(wrapper.findAll('tbody tr')[0].findAll('td')).toHaveLength(headerCount)
    expect(wrapper.text()).not.toContain('admin.dashboard.noDataAvailable')

    finish({ users: [item(1, 100)] })
    await flushPromises()
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.get('table').attributes('aria-busy')).toBe('false')
    await wrapper.get('tbody tr').trigger('click')
    expect(wrapper.emitted('select-user')).toEqual([[1, 'u1@test.com']])
    wrapper.unmount()
  })

  // Ranking retrieval finishes with either an empty result or a rejection. The test
  // protects removing loading skeletons and showing the existing empty-state message for
  // both outcomes.
  it.each(['empty', 'error'])('请求以 %s 结束时退出骨架并显示原有空态', async (result) => {
    if (result === 'empty') getUserBreakdown.mockResolvedValue({ users: [] })
    else getUserBreakdown.mockRejectedValue(new Error('request failed'))
    const wrapper = mountRanking()
    await flushPromises()

    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.dashboard.noDataAvailable')
    wrapper.unmount()
  })
})
