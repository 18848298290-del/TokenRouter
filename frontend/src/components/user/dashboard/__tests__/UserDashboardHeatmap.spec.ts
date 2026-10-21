import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent, h, ref } from 'vue'

import UserDashboardHeatmap from '../UserDashboardHeatmap.vue'
import { provideUsageChartState, type UsageChartState } from '../usageChartState'
import { usageAPI } from '@/api/usage'
import { formatDateLocalInput } from '@/utils/format'

// UserDashboardHeatmap.spec.ts mounts the production heatmap inside a page-state provider
// with controlled daily trend responses. The suite checks sparse calendar expansion,
// placeholder and tooltip presentation, date selection, and keyboard focus against the same
// shared usage state used by the dashboard.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      locale: { value: 'en' },
    }),
  }
})

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    formatBalanceAmount: (value: number) => String(value),
  }),
}))

vi.mock('@/api/usage', () => ({
  usageAPI: {
    getDashboardTrend: vi.fn(),
    getDashboardModels: vi.fn().mockResolvedValue({ models: [], start_date: '', end_date: '' }),
  },
}))

// The requested history starts three times 364 days before today and then aligns backward to Sunday.
const FETCH_DAYS = 3 * 364
// Without a measured container width, the component displays its most recent 53 complete weeks.
const FALLBACK_WEEKS = 53

// expectedRange mirrors the component's recent calendar-history query at the current clock.
// Cloning the end and aligning the start to Sunday supplies expected API dates for request
// assertions.
const expectedRange = () => {
  const end = new Date()
  const start = new Date(end)
  start.setDate(start.getDate() - FETCH_DAYS)
  start.setDate(start.getDate() - start.getDay())
  return { start, end }
}

// The wrapper provides the page-owned usage state and exposes it so selection assertions observe the same instance consumed by the heatmap.
let usageState: UsageChartState
const selectedDays: string[] = []
const heatmapRef = ref<{
  reload: () => Promise<void> /* reload requests fresh daily usage through the mounted heatmap and resolves after its loader finishes. */
} | null>(null)
// Harness provides shared usage state and records selected-day events, allowing heatmap
// interactions to be checked against the real dashboard selection controller.
const Harness = defineComponent({
  // setup creates page-owned usage state and renders the heatmap with observable selection
  // events.
  setup() {
    usageState = provideUsageChartState()
    return () =>
      h(UserDashboardHeatmap, {
        ref: heatmapRef,
        onSelectDay: (date: string) => selectedDays.push(date),
      })
  },
})

// mountHeatmap mounts the provider harness and flushes pending promises before returning it.
// Tests can leave an API promise unresolved to inspect placeholders or resolve responses to
// inspect calendar behavior.
const mountHeatmap = async () => {
  const wrapper = mount(Harness, {
    global: {
      stubs: { LoadingSpinner: true },
    },
  })
  await flushPromises()
  return wrapper
}

// emptyTrend supplies a successful response with no recorded dates, while todayTrendPoint
// adds one deterministic used date for intensity and tooltip assertions.
const emptyTrend = {
  trend: [],
  start_date: '',
  end_date: '',
  granularity: 'day',
}

// todayTrendPoint creates one used date with distinguishable requests, tokens, and charged
// cost.
const todayTrendPoint = (date: string) => ({
  date,
  requests: 3,
  input_tokens: 10,
  output_tokens: 20,
  cache_creation_tokens: 0,
  cache_read_tokens: 0,
  total_tokens: 30,
  cost: 0.1,
  actual_cost: 0.2,
})

describe('UserDashboardHeatmap', () => {
  beforeEach(() => {
    vi.mocked(usageAPI.getDashboardTrend).mockReset()
    selectedDays.length = 0
    localStorage.clear()
  })

  // The initial daily-trend promise remains pending before returning an empty result. The
  // test protects date-shaped placeholders and their replacement with selectable calendar
  // cells after loading completes.
  it('首次请求未返回时也有占位格，成功后替换为日期数据', async () => {
    let resolveTrend!: (value: typeof emptyTrend) => void
    vi.mocked(usageAPI.getDashboardTrend).mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveTrend = resolve
        }),
    )
    const wrapper = await mountHeatmap()
    expect(wrapper.findAll('[data-testid="heatmap-skeleton-cell"]').length).toBeGreaterThan(0)
    expect(wrapper.find('[data-testid="heatmap-cell"]').exists()).toBe(false)
    expect(wrapper.attributes('aria-busy')).toBe('true')

    resolveTrend(emptyTrend)
    await flushPromises()
    expect(wrapper.find('[data-testid="heatmap-skeleton-cell"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="heatmap-cell"]').exists()).toBe(true)
    expect(wrapper.attributes('aria-busy')).toBe('false')
    wrapper.unmount()
  })

  // The component mounts with an empty successful daily trend response. The test protects
  // the recent-history request range aligned backward to Sunday and its daily aggregation
  // parameter.
  it('按近三年整周范围请求按日趋势数据', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    await mountHeatmap()

    const { start, end } = expectedRange()
    expect(usageAPI.getDashboardTrend).toHaveBeenCalledWith({
      start_date: formatDateLocalInput(start),
      end_date: formatDateLocalInput(end),
      granularity: 'day',
    })
  })

  // Only today has recorded usage and container measurement is unavailable. The test
  // protects the fifty-three-week fallback, zero-filled past dates, and hidden future
  // padding.
  it('回退时渲染 53 周网格，接口未返回的日期补零为无用量档', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()

    // The visible fallback grid contains 53 complete weeks.
    const cells = wrapper.findAll('[data-testid="heatmap-cell"]')
    expect(cells.length).toBe(FALLBACK_WEEKS * 7)

    // Today is the only used date and receives the highest band; future padding remains invisible.
    const greenCells = cells.filter((c) => c.classes().includes('bg-green-700'))
    expect(greenCells.length).toBe(1)
    const futureCount = cells.filter((c) => c.classes().includes('invisible')).length
    expect(cells.filter((c) => c.classes().includes('bg-gray-100')).length).toBe(
      cells.length - futureCount - 1,
    )
  })

  // The pointer visits a used date, leaves the grid, and then visits an unused date. The
  // test protects detailed usage, visibility changes, and the empty-day explanation.
  it('悬停格子时显示当天用量，无用量日期显示无用量', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()
    const cells = wrapper.findAll('[data-testid="heatmap-cell"]')
    const todayIndex = cells.findIndex((c) => c.classes().includes('bg-green-700'))
    expect(todayIndex).toBeGreaterThan(0)

    await cells[todayIndex].trigger('mouseenter')
    const tooltip = wrapper.get('[data-testid="heatmap-tooltip"]')
    expect(tooltip.text()).toContain('dashboard.requests')
    expect(tooltip.text()).toContain('dashboard.tokens')
    expect(tooltip.text()).toContain('dashboard.heatmapCost')
    expect(tooltip.text()).toContain('0.2')
    expect(tooltip.attributes('aria-hidden')).toBe('false')
    expect(tooltip.attributes('style')).toContain('opacity var(--motion-fast) var(--motion-ease)')
    expect(tooltip.attributes('style')).not.toMatch(/transition:.*(?:width|left) /)

    await wrapper.get('[data-testid="heatmap-grid-wrap"]').trigger('mouseleave')
    expect(wrapper.get('[data-testid="heatmap-tooltip"]').attributes('aria-hidden')).toBe('true')

    // The preceding date has no recorded usage.
    await cells[todayIndex - 1].trigger('mouseenter')
    expect(wrapper.get('[data-testid="heatmap-tooltip"]').text()).toContain(
      'dashboard.heatmapNoUsage',
    )
  })

  // The pointer moves directly between neighboring dates with different usage. The test
  // protects reuse of the same visible tooltip element so switching cells does not introduce
  // a hide transition.
  it('在相邻格子之间移动时保持 tooltip 可见，不触发闪烁隐藏', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()
    const cells = wrapper.findAll('[data-testid="heatmap-cell"]')
    const todayIndex = cells.findIndex((c) => c.classes().includes('bg-green-700'))
    await cells[todayIndex].trigger('mouseenter')
    const tooltipElement = wrapper.get('[data-testid="heatmap-tooltip"]').element

    await cells[todayIndex - 1].trigger('mouseenter')

    expect(wrapper.get('[data-testid="heatmap-tooltip"]').element).toBe(tooltipElement)
    expect(wrapper.get('[data-testid="heatmap-tooltip"]').attributes('aria-hidden')).toBe('false')
  })

  // The calendar renders narrow weekday names along its first column. The test checks
  // Monday, Wednesday, and Friday labels at the matching calendar row coordinates.
  it('左侧渲染周一/周三/周五的星期标签', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    const wrapper = await mountHeatmap()
    // Narrow weekday labels occupy the first grid column at the Monday, Wednesday, and Friday rows.
    const labels = [1, 3, 5].map((d) =>
      new Date(2024, 0, d).toLocaleDateString('en', { weekday: 'narrow' }),
    )
    for (const [i, dayOfWeek] of [1, 3, 5].entries()) {
      const el = wrapper.find(`[style*="grid-column: 1"][style*="grid-row: ${dayOfWeek + 2}"]`)
      expect(el.exists()).toBe(true)
      expect(el.text()).toBe(labels[i])
    }
  })

  // The visible calendar crosses multiple month boundaries within narrow week columns. The
  // test protects minimum label spacing so adjacent month headings do not overlap.
  it('月份标签间隔不足 3 列时跳过显示', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    const wrapper = await mountHeatmap()
    // Month labels occupy the first grid row, with week columns beginning after the weekday-label column.
    const labelCols = wrapper
      .findAll('[style*="grid-row: 1"]')
      .map((el) => Number(/grid-column:\s*(\d+)/.exec(el.attributes('style') || '')?.[1]))
      .filter((n) => Number.isFinite(n))
    expect(labelCols.length).toBeGreaterThan(0)
    for (let i = 1; i < labelCols.length; i++) {
      expect(labelCols[i] - labelCols[i - 1]).toBeGreaterThanOrEqual(3)
    }
  })

  // The page invokes the component's exposed reload operation after initial loading. The
  // test checks that another daily trend request is issued through the same production
  // loader.
  it('reload 会重新请求数据', async () => {
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    await mountHeatmap()
    expect(usageAPI.getDashboardTrend).toHaveBeenCalledTimes(1)

    await heatmapRef.value?.reload()
    await flushPromises()
    expect(usageAPI.getDashboardTrend).toHaveBeenCalledTimes(2)
  })

  // The user selects today from the loaded grid. The test protects emitted date identity,
  // hourly custom-range selection, and matching pressed and selected styling on that cell.
  it('点击格子把趋势图切到当天按小时查看，并标记为选中', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue({
      ...emptyTrend,
      trend: [todayTrendPoint(today)],
    })

    const wrapper = await mountHeatmap()
    const cell = wrapper.get(`[data-date="${today}"]`)
    expect(cell.attributes('role')).toBe('button')
    expect(cell.attributes('tabindex')).toBe('0')
    expect(cell.classes()).toContain('heatmap-cell-today')

    await cell.trigger('click')
    await flushPromises()

    expect(selectedDays).toEqual([today])
    expect(usageState.rangePreset.value).toBe('custom')
    expect(usageState.activeRange.value.granularity).toBe('hour')
    expect(wrapper.get(`[data-date="${today}"]`).attributes('aria-pressed')).toBe('true')
    expect(wrapper.get(`[data-date="${today}"]`).classes()).toContain('heatmap-cell-selected')
  })

  // Keyboard arrows move from today to yesterday and back before attempting a future date.
  // The test protects the roving tab stop and prevents focus entering future padding.
  it('方向键在日期之间移动焦点，不会移到未来日期', async () => {
    const { end } = expectedRange()
    const today = formatDateLocalInput(end)
    vi.mocked(usageAPI.getDashboardTrend).mockResolvedValue(emptyTrend)

    const wrapper = await mountHeatmap()
    const yesterday = new Date(end)
    yesterday.setDate(yesterday.getDate() - 1)
    const yesterdayKey = formatDateLocalInput(yesterday)

    await wrapper.get(`[data-date="${today}"]`).trigger('keydown', { key: 'ArrowUp' })
    expect(wrapper.get(`[data-date="${yesterdayKey}"]`).attributes('tabindex')).toBe('0')
    expect(wrapper.get(`[data-date="${today}"]`).attributes('tabindex')).toBe('-1')

    await wrapper.get(`[data-date="${yesterdayKey}"]`).trigger('keydown', { key: 'ArrowDown' })
    await wrapper.get(`[data-date="${today}"]`).trigger('keydown', { key: 'ArrowDown' })
    expect(wrapper.get(`[data-date="${today}"]`).attributes('tabindex')).toBe('0')
  })
})
