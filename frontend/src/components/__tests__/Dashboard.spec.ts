import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { setActivePinia, createPinia } from 'pinia'
import { defineComponent, ref, onMounted, nextTick } from 'vue'

// Dashboard.spec.ts exercises a small dashboard-statistics loading fixture with controlled
// API responses. Its assertions cover mount loading, error and empty results, refresh
// replacement, and loading-state transitions in that harness, while production dashboard
// behavior is covered by separate component suites.

// The dashboard-statistics mock controls successful, pending, empty, and failed fixture
// loads independently of the production dashboard component tree.
const mockGetDashboardStats = vi.fn()

vi.mock('@/api', () => ({
  authAPI: {
    getCurrentUser: vi.fn().mockResolvedValue({
      data: {
        id: 1,
        username: 'test',
        email: 'test@example.com',
        role: 'user',
        balance: 100,
        concurrency: 5,
        status: 'active',
        allowed_groups: null,
        created_at: '',
        updated_at: '',
      },
    }),
    logout: vi.fn(),
    refreshToken: vi.fn(),
  },
  isTotp2FARequired: () => false,
}))

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardStats: (...args: any[]) => mockGetDashboardStats(...args),
  },
}))

vi.mock('@/api/admin/system', () => ({
  checkUpdates: vi.fn(),
}))

vi.mock('@/api/auth', () => ({
  getPublicSettings: vi.fn().mockResolvedValue({}),
}))

// DashboardStats describes the account summary fields consumed by this isolated loading
// fixture.
interface DashboardStats {
  balance: number // Available account balance amount.
  api_key_count: number // Total account key count.
  active_api_key_count: number // Active account key count.
  today_requests: number // Current-day completed request count.
  today_cost: number // Current-day charged cost amount.
  today_tokens: number // Current-day recorded token count.
  total_tokens: number // Lifetime recorded token count.
}

/* The fixture reproduces dashboard data loading and loading-state transitions for isolated API-response scenarios. */
// DashboardTestComponent reproduces statistics loading and a refresh button so promise-state
// assertions can run independently from production dashboard sections.
const DashboardTestComponent = defineComponent({
  // setup retains fixture statistics, loading, and error state and starts loading at mount.
  setup() {
    const stats = ref<DashboardStats | null>(null)
    const loading = ref(false)
    const error = ref('')

    // loadStats requests fixture statistics and replaces its error feedback for each
    // attempt. It retains any previous result on failure and resets loading when the mocked
    // request settles.
    const loadStats = async () => {
      loading.value = true
      error.value = ''
      try {
        stats.value = await mockGetDashboardStats()
      } catch (e: any) {
        error.value = e.message || '加载失败'
      } finally {
        loading.value = false
      }
    }

    onMounted(loadStats)

    return { stats, loading, error, loadStats }
  },
  template: `
    <div>
      <div v-if="loading" class="loading">加载中...</div>
      <div v-if="error" class="error">{{ error }}</div>
      <div v-if="stats" class="stats">
        <span class="balance">{{ stats.balance }}</span>
        <span class="api-keys">{{ stats.api_key_count }}</span>
        <span class="today-requests">{{ stats.today_requests }}</span>
        <span class="today-cost">{{ stats.today_cost }}</span>
      </div>
      <button class="refresh" @click="loadStats">刷新</button>
    </div>
  `,
})

describe('Dashboard 数据加载', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  const fakeStats: DashboardStats = {
    balance: 100.5,
    api_key_count: 3,
    active_api_key_count: 2,
    today_requests: 150,
    today_cost: 2.5,
    today_tokens: 50000,
    total_tokens: 1000000,
  }

  // The fixture mounts with a successful statistics response ready to resolve. The test
  // protects automatic loading and display of the balance, key count, requests, and cost.
  it('挂载后自动加载数据', async () => {
    mockGetDashboardStats.mockResolvedValue(fakeStats)

    const wrapper = mount(DashboardTestComponent)
    await flushPromises()

    expect(mockGetDashboardStats).toHaveBeenCalledTimes(1)
    expect(wrapper.find('.balance').text()).toBe('100.5')
    expect(wrapper.find('.api-keys').text()).toBe('3')
    expect(wrapper.find('.today-requests').text()).toBe('150')
    expect(wrapper.find('.today-cost').text()).toBe('2.5')
  })

  // The initial fixture statistics promise remains pending before completing successfully.
  // The test protects loading visibility and its replacement with statistics after the
  // request settles.
  it('加载中显示 loading 状态', async () => {
    let resolveStats: (v: any) => void
    mockGetDashboardStats.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveStats = resolve
        }),
    )

    const wrapper = mount(DashboardTestComponent)
    await nextTick()

    expect(wrapper.find('.loading').exists()).toBe(true)

    resolveStats!(fakeStats)
    await flushPromises()

    expect(wrapper.find('.loading').exists()).toBe(false)
    expect(wrapper.find('.stats').exists()).toBe(true)
  })

  // The fixture statistics API rejects with a network error. The test protects the displayed
  // error message and omission of statistics when no successful result exists.
  it('加载失败时显示错误信息', async () => {
    mockGetDashboardStats.mockRejectedValue(new Error('Network error'))

    const wrapper = mount(DashboardTestComponent)
    await flushPromises()

    expect(wrapper.find('.error').text()).toBe('Network error')
    expect(wrapper.find('.stats').exists()).toBe(false)
  })

  // The fixture receives changed statistics after its refresh button is activated. The test
  // protects another API call and replacement of the displayed request total.
  it('点击刷新按钮重新加载数据', async () => {
    mockGetDashboardStats.mockResolvedValue(fakeStats)

    const wrapper = mount(DashboardTestComponent)
    await flushPromises()

    expect(mockGetDashboardStats).toHaveBeenCalledTimes(1)

    // Apply the settled fixture data to the displayed metric state.
    const updatedStats = { ...fakeStats, today_requests: 200 }
    mockGetDashboardStats.mockResolvedValue(updatedStats)

    await wrapper.find('.refresh').trigger('click')
    await flushPromises()

    expect(mockGetDashboardStats).toHaveBeenCalledTimes(2)
    expect(wrapper.find('.today-requests').text()).toBe('200')
  })

  // The fixture receives a successful null statistics result. The test protects omission of
  // the statistics panel when the response contains no account summary to render.
  it('数据为空时不显示统计信息', async () => {
    mockGetDashboardStats.mockResolvedValue(null)

    const wrapper = mount(DashboardTestComponent)
    await flushPromises()

    expect(wrapper.find('.stats').exists()).toBe(false)
  })
})
