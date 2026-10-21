import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import OpsErrorDistributionChart from '../OpsErrorDistributionChart.vue'
import OpsErrorTrendChart from '../OpsErrorTrendChart.vue'

// OpsErrorScopeCharts.spec.ts checks the distinction between SLA errors and
// business-limit events in error charts. Chart components are stubbed to expose
// dataset props, while real aggregation and detail-button conditions decide what
// the dashboard can display.

vi.mock('chart.js', () => ({
  Chart: { register: vi.fn() },
  ArcElement: {},
  CategoryScale: {},
  Filler: {},
  Legend: {},
  LineElement: {},
  LinearScale: {},
  PointElement: {},
  Title: {},
  Tooltip: {},
}))

vi.mock('vue-chartjs', async () => {
  const { defineComponent } = await import('vue')

  return {
    Doughnut: defineComponent({
      name: 'Doughnut',
      props: {
        data: { type: Object, required: true }, // chart dataset fixture input
        options: { type: Object, default: () => ({}) }, // Chart.js configuration fixture input
      },
      template: '<div class="doughnut-stub" />',
    }),
    Line: defineComponent({
      name: 'LineChartStub',
      props: {
        data: { type: Object, required: true }, // chart dataset fixture input
        options: { type: Object, default: () => ({}) }, // Chart.js configuration fixture input
      },
      template: '<div class="line-stub" />',
    }),
  }
})

vi.mock('../../utils/opsFormatters', () => ({
  formatHistoryLabel: (date: string | undefined) => date ?? '',
  sumNumbers: (values: Array<number | null | undefined>) =>
    values.reduce<number>(
      (total, value) => total + (typeof value === 'number' && Number.isFinite(value) ? value : 0),
      0,
    ),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()

  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// Tooltip and empty-state doubles expose the chart decision while keeping
// overlay behavior outside these aggregation tests.
const HelpTooltipStub = defineComponent({
  name: 'HelpTooltip',
  props: {
    content: { type: String, default: '' }, // tooltip fixture description text
  },
  template: '<span class="help-tooltip-stub" />',
})

const EmptyStateStub = defineComponent({
  name: 'EmptyState',
  props: {
    title: { type: String, default: '' }, // displayed stub heading text
    description: { type: String, default: '' }, // displayed stub explanatory text
  },
  template: '<div class="empty-state-stub" />',
})

// globalStubs shares presentation doubles between distribution and trend
// fixtures so assertions exercise each real chart aggregation.
const globalStubs = {
  stubs: {
    HelpTooltip: HelpTooltipStub,
    EmptyState: EmptyStateStub,
  },
}

describe('Ops SLA-scoped error charts', () => {
  // A status distribution mixes SLA failures with business-limit counts. The
  // doughnut dataset must include the SLA subtotal and exclude the
  // business-limited observations from its categories.
  it('错误分布图按 SLA 错误数统计，不把业务限制错误算进请求错误分布', () => {
    const wrapper = mount(OpsErrorDistributionChart, {
      props: {
        loading: false,
        data: {
          total: 10,
          items: [
            { status_code: 400, total: 7, sla: 2, business_limited: 5 },
            { status_code: 503, total: 3, sla: 0, business_limited: 3 },
          ],
        }, // chart dataset fixture input
      },
      global: globalStubs,
    })

    const doughnut = wrapper.findComponent({ name: 'Doughnut' })
    expect(doughnut.exists()).toBe(true)
    expect(doughnut.props('data')).toMatchObject({
      labels: ['admin.ops.client'],
      datasets: [{ data: [2] }],
    })
  })

  // A distribution contains recorded errors entirely classified as business
  // limits. The component must render its empty state and omit the doughnut
  // because no SLA errors remain.
  it('错误分布图在只有业务限制错误时显示为空态', () => {
    const wrapper = mount(OpsErrorDistributionChart, {
      props: {
        loading: false,
        data: {
          total: 4,
          items: [{ status_code: 500, total: 4, sla: 0, business_limited: 4 }],
        }, // chart dataset fixture input
      },
      global: globalStubs,
    })

    expect(wrapper.findComponent({ name: 'Doughnut' }).exists()).toBe(false)
    expect(wrapper.find('.empty-state-stub').exists()).toBe(true)
  })

  // A trend contains business-limited events but no SLA-counted request errors.
  // Its request-error detail button must remain disabled even though the
  // displayed business-limit series has observations.
  it('错误趋势图的请求错误详情按钮只按 SLA 错误启用', () => {
    const wrapper = mount(OpsErrorTrendChart, {
      props: {
        loading: false,
        timeRange: '1h',
        points: [
          {
            bucket_start: '2026-05-18T00:00:00Z',
            error_count_total: 5,
            business_limited_count: 5,
            error_count_sla: 0,
            upstream_error_count_excl_429_529: 0,
            upstream_429_count: 0,
            upstream_529_count: 0,
          },
        ],
      },
      global: globalStubs,
    })

    const requestErrorsButton = wrapper.findAll('button')[0]
    expect(requestErrorsButton.attributes('disabled')).toBeDefined()
  })
})
