import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import RedeemView from '../RedeemView.vue'

// RedeemView.batchUpdate.spec.ts exercises redeem-code batch payloads and generation
// validation through mounted form controls. Endpoint mocks expose submitted identifiers
// and fields, while table and select stubs retain slot events and selected-value types
// needed to test production form behavior.

// Hoisted redeem endpoints and message spies expose form submissions and validation
// feedback while plan loading uses predictable test responses.
const {
  listRedeemCodes,
  generateRedeemCodes,
  batchUpdateRedeemCodes,
  getPlans,
  showSuccess,
  showError,
  showInfo,
} = vi.hoisted(() => ({
  listRedeemCodes: vi.fn(),
  generateRedeemCodes: vi.fn(),
  batchUpdateRedeemCodes: vi.fn(),
  getPlans: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
  showInfo: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    redeem: {
      list: listRedeemCodes,
      generate: generateRedeemCodes,
      update: vi.fn(),
      delete: vi.fn(),
      batchDelete: vi.fn(),
      batchUpdate: batchUpdateRedeemCodes,
      exportCodes: vi.fn(),
    },
    payment: {
      getPlans,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess,
    showError,
    showInfo,
  }),
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard: vi.fn(),
  }),
}))

vi.mock('@/composables/useBalanceDisplay', () => ({
  useBalanceDisplay: () => ({
    balanceUnitName: 'Credits',
    formatBalanceAmount: (value: number) => `$${value.toFixed(2)}`,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string>) =>
        key === 'admin.redeem.amount' ? `Amount (${params?.unitName})` : key,
    }),
  }
})

// DataTableStub renders production header and cell slots, allowing row selection and
// batch form controls to run against mounted code rows.
const DataTableStub = {
  // columns names the production slots and data supplies their rows; the fixture
  // exposes both so assertions exercise the parent cell renderers.
  props: ['columns', 'data'],
  template: `
    <table>
      <thead>
        <tr>
          <th v-for="column in columns" :key="column.key">
            <slot :name="'header-' + column.key" :column="column">{{ column.label }}</slot>
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="row in data" :key="row.id">
          <td v-for="column in columns" :key="column.key">
            <slot :name="'cell-' + column.key" :row="row" :value="row[column.key]">
              {{ row[column.key] }}
            </slot>
          </td>
        </tr>
      </tbody>
    </table>
  `,
}

// SelectStub keeps option values typed when native change events emit model updates and
// selection events to the production form.
const SelectStub = {
  // modelValue selects the current native option and options retains typed values;
  // onChange restores the selected value before forwarding production-compatible
  // events.
  props: ['modelValue', 'options'],
  // Model updates replace the bound draft value, and change also supplies the
  // matching option so parent handlers receive both selection outputs.
  emits: ['update:modelValue', 'change'],
  // setup binds a select change handler that restores the selected option's value type.
  // Both model update and change events are emitted so the production draft responds
  // like it does to Select.
  setup(
    props: {
      options: Array<{
        value: unknown // selected option value
        label: string // displayed option label
      }> // available selection options
    },
    {
      emit,
    }: {
      emit: (event: string, ...args: unknown[]) => void // selection event emitter
    },
  ) {
    // onChange resolves the native select's string back to its configured option value.
    // It emits the restored value through model update and change, retaining numeric or
    // boolean choices used by the forms.
    const onChange = (event: Event) => {
      const raw = (event.target as HTMLSelectElement).value
      const option = props.options.find((item) => String(item.value ?? '') === raw)
      const value = option ? option.value : raw
      emit('update:modelValue', value)
      emit('change', value, option ?? null)
    }
    return { onChange }
  },
  template: `
    <select v-bind="$attrs" :value="modelValue ?? ''" @change="onChange">
      <option v-for="option in options" :key="String(option.value ?? '')" :value="option.value ?? ''">
        {{ option.label }}
      </option>
    </select>
  `,
}

describe('admin RedeemView batch update', () => {
  beforeEach(() => {
    localStorage.clear()
    document.body.innerHTML = ''

    listRedeemCodes.mockReset()
    generateRedeemCodes.mockReset()
    batchUpdateRedeemCodes.mockReset()
    getPlans.mockReset()
    showSuccess.mockReset()
    showError.mockReset()
    showInfo.mockReset()

    listRedeemCodes.mockResolvedValue({
      items: [
        {
          id: 1,
          code: 'CODE-1',
          type: 'balance',
          value: 10,
          status: 'unused',
          max_uses: 1,
          used_count: 0,
          used_by: null,
          used_at: null,
          created_at: '2026-01-01T00:00:00Z',
          expires_at: null,
        },
        {
          id: 2,
          code: 'CODE-2',
          type: 'balance',
          value: 20,
          status: 'unused',
          max_uses: 1,
          used_count: 0,
          used_by: null,
          used_at: null,
          created_at: '2026-01-01T00:00:00Z',
          expires_at: null,
        },
      ],
      total: 2,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    generateRedeemCodes.mockResolvedValue([
      {
        id: 3,
        code: 'CODE-3',
        type: 'balance',
        value: 10,
        status: 'unused',
        max_uses: 5,
        used_count: 0,
        used_by: null,
        used_at: null,
        created_at: '2026-01-01T00:00:00Z',
        expires_at: null,
      },
    ])
    batchUpdateRedeemCodes.mockResolvedValue({ updated: 1, message: 'ok' })
    getPlans.mockResolvedValue({ data: [] })
  })

  // One code is selected and only status and notes are enabled for batch editing.
  // Submission must include that identifier and those fields while leaving expiry
  // absent.
  it('submits only checked fields for selected redeem codes', async () => {
    const wrapper = mount(RedeemView, {
      attachTo: document.body,
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template:
              '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          Select: SelectStub,
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>',
          },
          Icon: true,
          Teleport: true,
        },
      },
    })

    await flushPromises()
    await wrapper.findAll('[data-testid="select-code"]')[0].setValue(true)
    await wrapper.get('[data-testid="batch-update-open"]').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="batch-field-status"]').setValue(true)
    await wrapper.get('[data-testid="batch-status-select"]').setValue('disabled')
    await wrapper.get('[data-testid="batch-field-notes"]').setValue(true)
    await wrapper.get('[data-testid="batch-notes-input"]').setValue('maintenance')
    await wrapper.get('#batch-update-redeem-form').trigger('submit')
    await flushPromises()

    expect(batchUpdateRedeemCodes).toHaveBeenCalledWith([1], {
      status: 'disabled',
      notes: 'maintenance',
    })
    expect(showSuccess).toHaveBeenCalledWith('admin.redeem.batchUpdateSuccess')
  })

  // Custom code fixtures include common and supplementary Unicode characters around the
  // length limit. Accepted values must submit trimmed text, while overlong values
  // remain editable and never reach generation.
  it.each([
    { name: '32 个常用汉字', code: '兑'.repeat(32), tooLong: false },
    { name: '32 个生僻汉字', code: '𠮷'.repeat(32), tooLong: false },
    { name: '32 个混合字符', code: '兑𠮷a🎁'.repeat(8), tooLong: false },
    { name: '首尾空白', code: ` \t${'𠮷'.repeat(32)}\n `, tooLong: false },
    { name: '33 个常用汉字', code: '兑'.repeat(33), tooLong: true },
    { name: '33 个生僻汉字', code: '𠮷'.repeat(33), tooLong: true },
    { name: '33 个英文字母', code: 'a'.repeat(33), tooLong: true },
  ])('按字符校验自定义兑换码：$name', async ({ code, tooLong }) => {
    const wrapper = mount(RedeemView, {
      attachTo: document.body,
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template:
              '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          Select: SelectStub,
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>',
          },
          Icon: true,
          Teleport: true,
        },
      },
    })

    try {
      await flushPromises()
      await wrapper.get('[data-testid="generate-open"]').trigger('click')
      expect(wrapper.text()).toContain('Amount (Credits)')
      const input = wrapper.get<HTMLInputElement>(
        '[placeholder="admin.redeem.customCodePlaceholder"]',
      )
      await input.setValue(code)
      await wrapper.get('[data-testid="generate-form"]').trigger('submit')
      await flushPromises()

      if (tooLong) {
        expect(generateRedeemCodes).not.toHaveBeenCalled()
        expect(showError).toHaveBeenCalledWith('admin.redeem.customCodeTooLong')
        expect(input.element.value).toBe(code)
        expect(wrapper.find('[data-testid="generate-form"]').exists()).toBe(true)
        return
      }
      expect(showError).not.toHaveBeenCalled()
      expect(generateRedeemCodes).toHaveBeenCalledTimes(1)
      expect(generateRedeemCodes.mock.calls[0][6]).toBe(code.trim())
      // jsdom does not apply native maxlength truncation, so inspect the attribute
      // itself to protect supplementary characters from browser UTF-16 length limits.
      expect(input.attributes('maxlength')).toBeUndefined()
    } finally {
      wrapper.unmount()
    }
  })

  // The generation form is submitted with each supported payment-condition flag. The
  // endpoint must receive the selected boolean and requested maximum uses through the
  // production form controls.
  it.each([false, true])('提交生成兑换码的付款条件：%s', async (requiresPayment) => {
    const wrapper = mount(RedeemView, {
      attachTo: document.body,
      global: {
        stubs: {
          AppLayout: { template: '<div><slot /></div>' },
          TablePageLayout: {
            template:
              '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
          },
          DataTable: DataTableStub,
          Pagination: true,
          ConfirmDialog: true,
          Select: SelectStub,
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>',
          },
          Icon: true,
          Teleport: true,
        },
      },
    })

    await flushPromises()
    await wrapper.get('[data-testid="generate-open"]').trigger('click')
    expect(wrapper.text()).toContain('Amount (Credits)')
    await flushPromises()

    const maxUsesInput = wrapper.get('[data-testid="generate-max-uses"]')
    expect(maxUsesInput.exists()).toBe(true)

    expect(wrapper.get('#generateForm-requires-payment').attributes('aria-checked')).toBe('false')
    if (requiresPayment) {
      await wrapper.get('#generateForm-requires-payment').trigger('click')
    }
    await maxUsesInput.setValue('5')
    await wrapper.get('[data-testid="generate-form"]').trigger('submit')
    await flushPromises()

    expect(generateRedeemCodes).toHaveBeenCalledWith(
      1,
      'balance',
      10,
      undefined,
      5,
      null,
      undefined,
      undefined,
      requiresPayment,
    )
  })
})
