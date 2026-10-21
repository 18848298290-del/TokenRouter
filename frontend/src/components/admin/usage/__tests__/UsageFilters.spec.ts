import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'

import UsageFilters from '../UsageFilters.vue'

// UsageFilters.spec.ts tests administrator autocomplete ordering, stale user responses,
// and supplied filter choices. Mounted components use API substitutes and selected
// control stubs, allowing assertions to observe shared filter mutation and emitted
// changes without contacting the server.

// messages fixes user-deletion badges and unfiltered selector labels so autocomplete
// and option assertions compare actual rendered text.
const messages: Record<string, string> = {
  'admin.usage.userDeletedBadge': 'deleted',
  'admin.usage.userFilter': 'User',
  'admin.usage.teamFilter': 'Team',
  'admin.usage.allTeams': 'All Teams',
  'admin.usage.searchUserPlaceholder': 'Search user...',
  'usage.apiKeyFilter': 'API Key',
  'admin.usage.searchApiKeyPlaceholder': 'Search API key...',
  'usage.model': 'Model',
  'admin.usage.allModels': 'All Models',
  'admin.usage.provider': 'Provider',
  'admin.usage.searchProviderPlaceholder': 'Search provider...',
  'usage.type': 'Type',
  'admin.usage.allTypes': 'All Types',
  'usage.ws': 'WS',
  'usage.stream': 'Stream',
  'usage.sync': 'Sync',
  'usage.cyber': 'Cyber',
  'admin.usage.billingType': 'Billing Type',
  'admin.usage.allBillingTypes': 'All Billing Types',
  'admin.usage.billingTypeBalance': 'Balance',
  'admin.usage.billingTypeSubscription': 'Subscription',
  'admin.usage.billingMode': 'Billing Mode',
  'admin.usage.allBillingModes': 'All Billing Modes',
  'admin.usage.billingModeToken': 'Token',
  'admin.usage.billingModePerRequest': 'Per Request',
  'admin.usage.billingModeImage': 'Image',
  'admin.usage.group': 'Group',
  'admin.usage.allGroups': 'All Groups',
  'common.refresh': 'Refresh',
  'common.reset': 'Reset',
  'admin.usage.cleanup.button': 'Cleanup',
  'usage.exportExcel': 'Export',
}

// Deterministic translation text lets dropdown assertions identify rendered labels
// independently of the application locale catalog.
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

// mockSearchUsers controls autocomplete response order and records queries used by
// stale-user-search tests in this suite.
const mockSearchUsers = vi.fn()
// mockSearchApiKeys supplies selected-user key suggestions while recording the identity
// passed by user selection and key focus.
const mockSearchApiKeys = vi.fn().mockResolvedValue([])
// mockGroupsList supplies group choices for mounting filters while keeping user-search
// assertions independent of administrator group retrieval.
const mockGroupsList = vi.fn().mockResolvedValue({ items: [] })
// mockTeamsList supplies team identities and names whose values are inspected through
// the project Select component props.
const mockTeamsList = vi.fn().mockResolvedValue([])
// mockGetModelStats records unexpected dashboard model retrieval so the suite can
// protect ownership of parent-supplied model choices.
const mockGetModelStats = vi.fn().mockResolvedValue({ models: [] })
// mockProvidersList supplies a controlled empty provider page while tests exercise the
// other autocomplete and selector paths.
const mockProvidersList = vi.fn().mockResolvedValue({ items: [] })

vi.mock('@/api/admin', () => ({
  adminAPI: {
    usage: {
      searchUsers: (...args: any[]) => mockSearchUsers(...args),
      searchApiKeys: (...args: any[]) => mockSearchApiKeys(...args),
    },
    groups: { list: (...args: any[]) => mockGroupsList(...args) },
    teams: { list: (...args: any[]) => mockTeamsList(...args) },
    dashboard: { getModelStats: (...args: any[]) => mockGetModelStats(...args) },
    providers: { list: (...args: any[]) => mockProvidersList(...args) },
  },
}))

// defaultFilters returns a fresh unselected usage filter object for each mount.
const defaultFilters = () => ({
  user_id: undefined,
  api_key_id: undefined,
  provider_id: undefined,
  model: null,
  request_type: null,
  billing_type: null,
  billing_mode: null,
  group_id: null,
  team_id: null,
  start_date: '',
  end_date: '',
})

// mountFilters renders UsageFilters with deterministic dates and caller-provided mutable
// filters. It stubs Select and Teleport so tests can observe control props and emitted
// changes while autocomplete requests remain controlled.
function mountFilters(filters = defaultFilters()) {
  return mount(UsageFilters, {
    props: {
      modelValue: filters,
      exporting: false,
      startDate: '2026-05-01',
      endDate: '2026-05-28',
      showActions: false,
      modelOptions: [],
    },
    global: {
      stubs: {
        Select: true,
        Teleport: true,
      },
    },
  })
}

// deferred returns a promise and its externally controlled settlement functions. Tests
// resolve or reject that promise after later input to reproduce response ordering
// independently of elapsed network time.
function deferred<T>() {
  let resolve!: (value: T | PromiseLike<T>) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })

  return { promise, resolve, reject }
}

describe('UsageFilters — user search dropdown', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mockSearchUsers.mockReset()
    mockSearchApiKeys.mockResolvedValue([])
    mockGetModelStats.mockClear()
    mockGroupsList.mockClear()
    mockTeamsList.mockClear()
    mockProvidersList.mockClear()
  })

  // Restore the real clock after each fake-timer scenario so later cases do not inherit
  // its pending debounce or refresh schedule.
  afterEach(() => {
    vi.useRealTimers()
  })

  // Active and deleted users arrive together in autocomplete results. The test protects
  // active-first ordering, translated deletion badges, and the selected user identifier
  // emitted through shared filters.
  it('(a) labels deleted users with the i18n badge and (b) sorts active users before deleted ones, (c) selection sets user_id', async () => {
    // Return the deleted user first so the rendered order proves that active users
    // receive priority in suggestions.
    mockSearchUsers.mockResolvedValue([
      { id: 2, email: 'gone@test.com', deleted: true },
      { id: 1, email: 'active@test.com', deleted: false },
    ])

    const wrapper = mountFilters()

    // Focus and typing reproduce the autocomplete path that schedules the user debounce
    // timer before requesting suggestions.
    const input = wrapper.find('input[placeholder="Search user..."]')
    await input.trigger('focus')
    await input.setValue('test')
    await input.trigger('input')

    // Advance the debounce clock before flushing promises so the request callback has
    // started when assertions inspect its results.
    vi.advanceTimersByTime(300)
    await flushPromises()

    // Inspect actual element order so the assertion covers displayed suggestion
    // ordering rather than the response fixture alone.
    const buttons = wrapper.findAll('.usage-filter-dropdown button[type="button"]')
    const emailTexts = buttons.map((b) => b.text())

    const activeIdx = emailTexts.findIndex((t) => t.includes('active@test.com'))
    const deletedIdx = emailTexts.findIndex((t) => t.includes('gone@test.com'))
    expect(activeIdx).toBeGreaterThanOrEqual(0)
    expect(deletedIdx).toBeGreaterThanOrEqual(0)
    expect(activeIdx).toBeLessThan(deletedIdx)

    const deletedButton = buttons[deletedIdx]
    expect(deletedButton.text()).toContain('deleted')

    const activeButton = buttons[activeIdx]
    expect(activeButton.text()).not.toContain('deleted')

    await activeButton.trigger('click')
    await flushPromises()

    const changeEmits = wrapper.emitted('change')
    expect(changeEmits).toBeTruthy()
    expect(changeEmits!.length).toBeGreaterThan(0)

    // The component mutates the supplied modelValue object through toRef before
    // notifying its parent with a change event.
    expect(wrapper.props('modelValue').user_id).toBe(1)
  })

  // Two typed user queries complete in reverse order. The test protects the newest
  // suggestion list from being replaced by an older response arriving later.
  it('keeps results from the latest user search when responses arrive out of order', async () => {
    const firstSearch = deferred<
      Array<{
        id: number // record numeric identifier
        email: string // user email display text
        deleted: boolean // user soft-deletion marker
      }>
    >()
    const secondSearch = deferred<
      Array<{
        id: number // record numeric identifier
        email: string // user email display text
        deleted: boolean // user soft-deletion marker
      }>
    >()
    mockSearchUsers
      .mockImplementationOnce(() => firstSearch.promise)
      .mockImplementationOnce(() => secondSearch.promise)

    const wrapper = mountFilters()
    const input = wrapper.find('input[placeholder="Search user..."]')
    await input.trigger('focus')

    await input.setValue('a')
    vi.advanceTimersByTime(300)
    await flushPromises()

    await input.setValue('ab')
    vi.advanceTimersByTime(300)
    await flushPromises()

    secondSearch.resolve([{ id: 2, email: 'ab@test.com', deleted: false }])
    await flushPromises()
    expect(wrapper.text()).toContain('ab@test.com')

    firstSearch.resolve([{ id: 1, email: 'a@test.com', deleted: false }])
    await flushPromises()
    expect(wrapper.text()).toContain('ab@test.com')
    expect(wrapper.text()).not.toContain('a@test.com')
  })

  // A user search completes after its input has been cleared. The test protects the empty
  // suggestion state from restoration by that stale response.
  it('does not restore stale user results after the search is cleared', async () => {
    const pendingSearch = deferred<
      Array<{
        id: number // record numeric identifier
        email: string // user email display text
        deleted: boolean // user soft-deletion marker
      }>
    >()
    mockSearchUsers.mockImplementationOnce(() => pendingSearch.promise)

    const wrapper = mountFilters()
    const input = wrapper.find('input[placeholder="Search user..."]')
    await input.trigger('focus')

    await input.setValue('stale')
    vi.advanceTimersByTime(300)
    await flushPromises()

    await input.setValue('')
    vi.advanceTimersByTime(300)
    await flushPromises()

    pendingSearch.resolve([{ id: 3, email: 'stale@test.com', deleted: false }])
    await flushPromises()
    expect(wrapper.text()).not.toContain('stale@test.com')
  })
})

describe('UsageFilters — model options come from prop (no dup request)', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    mockGetModelStats.mockClear()
    mockGroupsList.mockClear()
    mockTeamsList.mockClear()
  })

  // Restore the real clock after each fake-timer scenario so later cases do not inherit
  // its pending debounce or refresh schedule.
  afterEach(() => {
    vi.useRealTimers()
  })

  // The parent supplies model options when the filter panel mounts. The test protects the
  // supplied choices and checks that no separate model-statistics request runs.
  it('does not call dashboard.getModelStats on mount and renders model options from prop', async () => {
    const wrapper = mount(UsageFilters, {
      props: {
        modelValue: defaultFilters(),
        exporting: false,
        startDate: '2026-05-01',
        endDate: '2026-05-28',
        showActions: false,
        modelOptions: ['claude-3', 'gpt-4o'],
      },
      global: {
        stubs: {
          Select: true,
          Teleport: true,
        },
      },
    })
    await flushPromises()

    expect(mockGetModelStats).not.toHaveBeenCalled()

    const opts = (wrapper.vm as any).modelOptions as Array<{
      value: string | null // option selection value
      label: string // option display label
    }>
    expect(opts.map((o) => o.value)).toEqual([null, 'claude-3', 'gpt-4o'])
  })
})

describe('UsageFilters — team options', () => {
  // Team options arrive through the administrator team API during mounting. The test
  // protects their numeric values and labels in the project Select options.
  it('loads admin teams into the project Select options', async () => {
    mockTeamsList.mockResolvedValueOnce([{ id: 9, name: 'Platform Team' }])

    const wrapper = mountFilters()
    await flushPromises()

    const options = (wrapper.vm as any).teamOptions as Array<{
      value: number | null // option selection value
      label: string // option display label
    }>
    expect(options).toEqual([
      { value: null, label: 'All Teams' },
      { value: 9, label: 'Platform Team' },
    ])
  })
})

describe('UsageFilters — 未设置的条件', () => {
  // Unselected predicates arrive as undefined values in the shared filter object. The test
  // protects null dropdown presentation and writing a selected request type back into
  // filters.
  it('undefined 条件按 null 传给下拉框，显示“全部”选项；选择后写回筛选对象', async () => {
    const filters: Record<string, any> = { start_date: '', end_date: '' }
    const wrapper = mountFilters(filters as ReturnType<typeof defaultFilters>)
    await flushPromises()

    const selects = wrapper.findAllComponents({ name: 'Select' })
    expect(selects.length).toBeGreaterThan(0)
    for (const select of selects) {
      expect(select.props('modelValue')).toBeNull()
    }

    const typeSelect = selects.find((select) => select.props('options')?.[0]?.label === 'All Types')
    typeSelect!.vm.$emit('update:modelValue', 'sync')
    expect(filters.request_type).toBe('sync')
  })
})
