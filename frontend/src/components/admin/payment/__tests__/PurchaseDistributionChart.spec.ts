import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import PurchaseDistributionChart from '../PurchaseDistributionChart.vue'
import Select from '@/components/common/Select.vue'

// PurchaseDistributionChart.spec.ts checks the rendered purchase distribution sections for
// populated and empty data. Chart replacements expose doughnut instances through simple
// elements, allowing the tests to count charts and inspect translated category labels
// without canvas rendering.
vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) =>
      ({
        'payment.admin.purchaseDistribution': '购买情况',
        'payment.admin.amountShare': '金额占比',
        'payment.admin.countShare': '订单数占比',
        'payment.admin.purchaseItem': '购买项',
        'payment.admin.revenue': '收入',
        'payment.admin.orderCount': '订单数',
        'payment.admin.payAsYouGo': '按量支付',
        'payment.admin.noData': '暂无数据',
      })[key] || key,
  }),
}))

vi.mock('vue-chartjs', () => ({
  Doughnut: {
    name: 'Doughnut',
    props: ['data', 'options'],
    template: '<div class="doughnut-chart"></div>',
  },
}))

vi.mock('chart.js', () => ({
  Chart: {
    register: vi.fn(),
  },
  ArcElement: {},
  Tooltip: {},
  Legend: {},
}))

describe('PurchaseDistributionChart', () => {
  // Purchase distribution contains balance and subscription entries with amounts and order
  // counts. The component must render two doughnut charts and detail rows for both
  // categories.
  it('renders amount and count doughnut charts with detail rows', () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: [
          { type: 'balance', currency: 'CNY', label: 'balance', amount: 50, count: 2 },
          {
            type: 'subscription',
            currency: 'CNY',
            label: '专业版',
            plan_id: 7,
            amount: 30,
            count: 1,
          },
        ],
      },
    })

    expect(wrapper.text()).toContain('购买情况')
    expect(wrapper.text()).toContain('金额占比')
    expect(wrapper.text()).toContain('订单数占比')
    expect(wrapper.text()).toContain('按量支付')
    expect(wrapper.text()).toContain('专业版')
    expect(wrapper.findAll('.doughnut-chart')).toHaveLength(2)
  })

  // Mixed-currency purchases share categories but must never share an amount denominator.
  // Switching currencies updates both datasets, detail amounts, and the rendered tooltip currency.
  it('keeps chart shares and details within the selected payment currency', async () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: [
          { type: 'balance', currency: 'CNY', label: 'balance', amount: 70, count: 7 },
          { type: 'balance', currency: 'USD', label: 'balance', amount: 10, count: 1 },
          { type: 'subscription', currency: 'USD', label: 'Pro', amount: 30, count: 3 },
        ],
      },
      global: { stubs: { Select: true } },
    })
    expect(wrapper.text()).toContain('CNY 70.00')
    expect(wrapper.text()).not.toContain('USD 10.00')
    wrapper.findComponent(Select).vm.$emit('update:modelValue', 'USD')
    await wrapper.vm.$nextTick()
    const charts = wrapper.findAllComponents({ name: 'Doughnut' })
    expect(charts[0].props('data').datasets[0].data).toEqual([30, 10])
    expect(charts[1].props('data').datasets[0].data).toEqual([3, 1])
    const tooltip = charts[0].props('options').plugins.tooltip.callbacks.label
    expect(tooltip({ raw: 10, label: 'Balance', dataset: { data: [30, 10] } })).toBe(
      'Balance: USD 10.00 (25.0%)',
    )
    expect(wrapper.text()).toContain('USD 10.00')
    expect(wrapper.text()).not.toContain('CNY 70.00')
    await wrapper.setProps({
      items: [{ type: 'balance', currency: 'CNY', label: 'balance', amount: 50, count: 1 }],
    })
    expect(wrapper.text()).toContain('CNY 50.00')
  })

  // Purchase distribution receives an empty collection from the dashboard. The component
  // must render its empty-state message and omit the doughnut charts until purchase data
  // exists.
  it('renders empty state when there is no purchase data', () => {
    const wrapper = mount(PurchaseDistributionChart, {
      props: {
        items: [],
      },
    })

    expect(wrapper.text()).toContain('暂无数据')
    expect(wrapper.findAll('.doughnut-chart')).toHaveLength(0)
  })
})
