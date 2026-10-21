import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

import PricingView from '../PricingView.vue'
import { adminAPI } from '@/api/admin'
import { defaultBillingSettings } from '@/components/admin/pricing/billingSettings'

// PricingView.spec.ts checks billing model selection and shared pricing settings submission.
// Mounted components or pure helper calls exercise the real owning implementation, while
// controlled API and display dependencies expose the observable updates asserted by each
// scenario.

// Hoisted pricing, group, and settings mocks supply the page data needed to exercise shared
// billing configuration through the real editor.
const { listPricingConfigs, getGroups, getWebSearchEmulationConfig } = vi.hoisted(() => ({
  listPricingConfigs: vi.fn(),
  getGroups: vi.fn(),
  getWebSearchEmulationConfig: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    pricing: {
      list: listPricingConfigs,
      create: vi.fn(),
      update: vi.fn(),
      remove: vi.fn(),
      getModelDefaultPricing: vi.fn(),
    },
    groups: {
      getAll: getGroups,
    },
    settings: {
      getWebSearchEmulationConfig,
    },
    providers: {
      list: vi.fn().mockResolvedValue({ items: [], total: 0 }),
      getById: vi.fn(),
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
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

// BaseDialogStub and adjacent control stubs expose test-visible slots and events while keeping
// dialog layout outside the tested page behavior.
const BaseDialogStub = defineComponent({
  props: {
    // show gates both dialog slots. Its false default keeps editor controls absent until the
    // page opens the dialog in the test.
    show: {
      type: Boolean,
      default: false,
    },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

// SelectStub forwards selection updates through a compact test control so assertions exercise
// the owning component data flow.
const SelectStub = defineComponent({
  props: {
    // modelValue accepts string or numeric selections from the page. An omitted value defaults
    // to the empty string for this selector fixture.
    modelValue: {
      type: [String, Number],
      default: '',
    },
    // options supplies the displayed option buttons. Its default factory gives each stub a
    // fresh empty array when the page supplies no choices.
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  template: `
    <div>
      <button
        v-for="option in options"
        :key="option.value"
        type="button"
        :data-option="option.value"
        @click="$emit('update:modelValue', option.value)"
      >
        {{ option.label }}
      </button>
    </div>
  `,
})

// mountView mounts the administrator page with controlled API and display dependencies. The
// returned wrapper exposes real page actions while stubs keep unrelated layout and dialog
// rendering outside the assertions.
function mountView() {
  return mount(PricingView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: true,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: SelectStub,
        Icon: true,
        PlatformIcon: true,
        Toggle: true,
        DefaultPricingPanel: true,
        PricingEntryCard: true,
      },
    },
  })
}

describe('PricingView model routing copy', () => {
  beforeEach(() => {
    listPricingConfigs.mockReset()
    getGroups.mockReset()
    getWebSearchEmulationConfig.mockReset()
    listPricingConfigs.mockResolvedValue({ items: [], total: 0 })
    getGroups.mockResolvedValue([])
    getWebSearchEmulationConfig.mockResolvedValue({ enabled: false, providers: [] })
  })

  // The editor changes among requested, group-mapped, and upstream billing model sources. Each
  // selection updates its explanatory hint so administrators can see which model identity
  // selects the price.
  it('updates the billing hint for all three price sources', async () => {
    const wrapper = mountView()
    await flushPromises()

    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.pricing.createPricingConfig'))
    expect(createButton).toBeTruthy()
    await createButton!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).not.toContain('admin.pricing.form.applyPricingToProviderStats')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe(
      'admin.pricing.form.billingModelSourceHintGroupMapped',
    )

    await wrapper.get('[data-option="requested"]').trigger('click')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe(
      'admin.pricing.form.billingModelSourceHintRequested',
    )

    await wrapper.get('[data-option="upstream"]').trigger('click')
    expect(wrapper.get('[data-testid="billing-model-source-hint"]').text()).toBe(
      'admin.pricing.form.billingModelSourceHintUpstream',
    )
  })

  // The pricing dialog is opened and its settings are inspected. Routing controls are absent so
  // the price configuration form exposes the pricing responsibilities owned by this page.
  it('keeps routing settings out of price configuration', async () => {
    const wrapper = mountView()
    await flushPromises()
    const createButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.pricing.createPricingConfig'))
    await createButton!.trigger('click')
    await flushPromises()

    expect(wrapper.text()).not.toContain('admin.pricing.form.platformConfig')
    const pricingTab = wrapper
      .findAll('button')
      .find((button) => button.text() === 'admin.pricing.form.modelPricing')!
    await pricingTab.trigger('click')

    expect(wrapper.find('[data-testid="channel-model-mapping-hint"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('admin.pricing.form.restrictModels')
  })
})

describe('PricingView billing settings', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    listPricingConfigs.mockResolvedValue({ items: [], total: 0 })
    getGroups.mockResolvedValue([])
  })

  // A new configuration has shared billing settings and selected groups but no model price
  // cards. Submission includes those settings and accepts the configuration through the create
  // endpoint.
  it('creates shared settings even without model pricing entries', async () => {
    const wrapper = mountView()
    await flushPromises()
    // Selected groups satisfy the association prerequisite while the test edits price settings
    // through the real billing panel.
    const vm = wrapper.vm as any
    await vm.openCreateDialog()
    vm.form.name = 'Shared settings'
    vm.form.sections[0].group_ids = [7]
    await wrapper.get('#pricing-tab-billing').trigger('click')
    expect(wrapper.get('[data-testid="billing-settings"]').isVisible()).toBe(true)
    await wrapper.get('#pricing-search_price_per_1k').setValue('0')
    await wrapper.get('#pricing-batch_image_discount_multiplier').setValue('0.4')
    await wrapper.get('#pricing-form').trigger('submit')
    await flushPromises()
    expect(adminAPI.pricing.create).toHaveBeenCalledWith(
      expect.objectContaining({
        group_ids: [7],
        model_pricing: [],
        search_price_per_1k: 0,
        batch_image_discount_multiplier: 0.4,
        long_context_pricing_enabled: true,
      }),
    )
    wrapper.unmount()
  })

  // An existing configuration is opened, restored, and edited to clear a service price. The
  // update retains other settings and serializes the cleared price as null.
  it('restores settings and clears a price with null on update', async () => {
    const wrapper = mountView()
    await flushPromises()
    const vm = wrapper.vm as any
    await vm.openEditDialog({
      ...defaultBillingSettings(),
      id: 9,
      name: 'Shared',
      status: 'active',
      billing_model_source: 'group_mapped',
      group_ids: [7],
      model_pricing: [],
      provider_stats_pricing_rules: [],
      web_search_price_per_call: 0.2,
      free_openai_fast: true,
    })
    await wrapper.get('#pricing-tab-billing').trigger('click')
    expect(
      (wrapper.get('#pricing-web_search_price_per_call').element as HTMLInputElement).value,
    ).toBe('0.2')
    await wrapper.get('#pricing-web_search_price_per_call').setValue('')
    await wrapper.get('#pricing-tab-billing').trigger('keydown', { key: 'Home' })
    expect(wrapper.get('#pricing-tab-basic').attributes('aria-selected')).toBe('true')
    await wrapper.get('#pricing-form').trigger('submit')
    await flushPromises()
    expect(adminAPI.pricing.update).toHaveBeenCalledWith(
      9,
      expect.objectContaining({
        web_search_price_per_call: null,
        free_openai_fast: true,
      }),
    )
    wrapper.unmount()
  })
})
