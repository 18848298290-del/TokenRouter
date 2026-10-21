import { flushPromises, mount } from '@vue/test-utils'
import { createPinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import AdminPaymentPlansView from '../AdminPaymentPlansView.vue'

// AdminPaymentPlansView.spec.ts checks currency rendering for current and legacy
// subscription plan prices. Table slot replacements render supplied plan rows, allowing
// assertions to observe configured currency symbols, the empty-currency USD behavior, and
// the shared table layout.

// Hoisted request replacements are shared with imported module mocks so each test can
// control API outcomes and inspect calls.
const { getPlans, getConfig } = vi.hoisted(() => ({
  getPlans: vi.fn(),
  getConfig: vi.fn(),
}))

vi.mock('@/api/admin/payment', () => {
  const paymentAPI = {
    getPlans,
    getConfig,
  }
  return {
    adminPaymentAPI: paymentAPI,
    default: paymentAPI,
  }
})

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

// DataTableStub renders each price slot with its corresponding plan row so assertions
// observe the page currency formatting.
const DataTableStub = {
  props: ['data'],
  template: `
    <div>
      <div v-for="row in data" :key="row.id">
        <slot name="cell-price" :value="row.price" :row="row" />
      </div>
    </div>
  `,
}

// TablePageLayoutStub renders the table slot used by the plan page and exposes the layout
// component for assertions.
const TablePageLayoutStub = {
  template: '<div class="table-page-layout"><slot name="table" /></div>',
}

describe('AdminPaymentPlansView', () => {
  beforeEach(() => {
    getConfig.mockResolvedValue({ data: {} })
    getPlans.mockResolvedValue({
      data: [
        {
          id: 1,
          name: 'CNY plan',
          group_id: 1,
          price: 499,
          original_price: 599,
          currency: 'CNY',
          validity_days: 30,
          validity_unit: 'day',
          sort_order: 0,
          for_sale: true,
          features: [],
        },
        {
          id: 2,
          name: 'Legacy plan',
          group_id: 1,
          price: 10,
          original_price: 0,
          currency: '',
          validity_days: 30,
          validity_unit: 'day',
          sort_order: 0,
          for_sale: true,
          features: [],
        },
      ],
    })
  })

  // The list includes a CNY plan and a legacy plan with an empty currency. Prices must
  // display their corresponding currency symbols while retaining the shared table layout.
  it('uses the configured currency symbol and keeps legacy prices in USD', async () => {
    const wrapper = mount(AdminPaymentPlansView, {
      global: {
        plugins: [createPinia()],
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: TablePageLayoutStub,
          DataTable: DataTableStub,
          ConfirmDialog: true,
          Icon: true,
          PlanEditDialog: true,
        },
      },
    })

    await flushPromises()

    expect(wrapper.text()).toContain('¥499.00CNY')
    expect(wrapper.text()).toContain('¥599.00')
    expect(wrapper.text()).toContain('$10.00')
    expect(wrapper.findComponent(TablePageLayoutStub).exists()).toBe(true)
  })
})
