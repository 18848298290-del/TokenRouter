import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import type { AdminGroup } from '@/api/groups.types'
import GroupsView from '@/views/admin/GroupsView.vue'

// GroupsView.duplicate.spec.ts checks group duplication progress, API errors, and list refresh
// behavior. Mounted components or pure helper calls exercise the real owning implementation,
// while controlled API and display dependencies expose the observable updates asserted by each
// scenario.

// Hoisted group API and notification mocks let each scenario control request outcomes before
// the mounted page imports its dependencies.
const {
  listGroups,
  duplicateGroup,
  updateGroup,
  getAll,
  getModelsListCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  showSuccess,
  showError,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  duplicateGroup: vi.fn(),
  updateGroup: vi.fn(),
  getAll: vi.fn(),
  getModelsListCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  showSuccess: vi.fn(),
  showError: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      duplicate: duplicateGroup,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      getAll,
      create: vi.fn(),
      update: updateGroup,
      delete: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    providers: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess, showError }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// sourceGroup supplies the saved group identity and settings used as the selected duplication
// and update target across these page scenarios.
const sourceGroup: AdminGroup = {
  id: 42,
  name: 'Primary',
  description: null,
  platform: 'openai',
  scheduler_type: 'advanced',
  rate_multiplier: 1,
  rpm_limit: 0,
  is_exclusive: false,
  status: 'active',
  subscription_type: 'standard',
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  allow_image_generation: false,
  allow_batch_image_generation: false,

  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,

  web_search_price_per_call: null,
  peak_rate_enabled: false,
  peak_start: '',
  peak_end: '',
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  default_mapped_model: '',
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: '2026-07-16T00:00:00Z',
  updated_at: '2026-07-16T00:00:00Z',
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  provider_count: 1,
  active_provider_count: 1,
  rate_limited_provider_count: 0,
  models_list_config: undefined,
  sort_order: 10,
}

// The layout and table stubs expose page slots, row actions, and displayed columns so these
// tests can inspect the actual page state.
const AppLayoutStub = defineComponent({
  template: '<main><slot /></main>',
})

const TablePageLayoutStub = defineComponent({
  template:
    '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
})

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
    columns: { type: Array, default: () => [] },
    loading: { type: Boolean, default: false },
  },
  template:
    '<div><div v-for="row in data" :key="row.id"><slot name="cell-actions" :row="row" /></div></div>',
})

// BaseDialogStub and adjacent control stubs expose test-visible slots and events while keeping
// dialog layout outside the tested page behavior.
const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
})

// mountView mounts the administrator page with controlled API and display dependencies. The
// returned wrapper exposes real page actions while stubs keep unrelated layout and dialog
// rendering outside the assertions.
function mountView() {
  return mount(GroupsView, {
    global: {
      stubs: {
        Teleport: true,
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        VueDraggable: true,
      },
    },
  })
}

describe('GroupsView duplicate action', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.spyOn(console, 'error').mockImplementation(() => {})
    for (const fn of [
      listGroups,
      duplicateGroup,
      updateGroup,
      getAll,
      getModelsListCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      showSuccess,
      showError,
    ]) {
      fn.mockReset()
    }
    getAll.mockResolvedValue([])

    listGroups.mockResolvedValue({
      items: [sourceGroup],
      total: 1,
      page: 1,
      page_size: 20,
      pages: 1,
    })
    duplicateGroup.mockResolvedValue({
      ...sourceGroup,
      id: 43,
      name: 'Primary (Copy)',
      status: 'inactive',
    })
    getModelsListCandidates.mockResolvedValue([])
    getUsageSummary.mockResolvedValue([])
    getCapacitySummary.mockResolvedValue([])
    getLiveCapability.mockResolvedValue({ supported: false, reason: 'test server unsupported' })
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  // A group duplication request resolves successfully for the chosen row. The page reports the
  // duplicate name and refreshes the list so the resulting group becomes discoverable.
  it('duplicates the selected group, reports success, and refreshes the list', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-testid="group-duplicate"]').exists()).toBe(false)
    await wrapper.get('[data-testid="group-more"]').trigger('click')
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(duplicateGroup).toHaveBeenCalledTimes(1)
    expect(duplicateGroup).toHaveBeenCalledWith(42)
    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
    expect(listGroups).toHaveBeenCalledTimes(2)
    expect(wrapper.find('[role="menu"]').exists()).toBe(false)
    wrapper.unmount()
  })

  // A duplicate request remains pending while the same action is clicked again. The page keeps
  // that group's duplicate action disabled and submits only one request for its ID.
  it('ignores repeated clicks while the duplicate request is in flight', async () => {
    let resolveDuplicate!: (value: AdminGroup) => void
    duplicateGroup.mockImplementationOnce(
      () =>
        new Promise<AdminGroup>((resolve) => {
          resolveDuplicate = resolve
        }),
    )
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-more"]').trigger('click')
    const button = wrapper.get('[data-testid="group-duplicate"]')
    void button.trigger('click')
    void button.trigger('click')
    await wrapper.vm.$nextTick()

    expect(duplicateGroup).toHaveBeenCalledTimes(1)
    // The duplicate action stays disabled while its request is pending, including when the
    // action menu is opened again.
    await wrapper.get('[data-testid="group-more"]').trigger('click')
    const pendingButton = wrapper.get('[data-testid="group-duplicate"]')
    expect(pendingButton.attributes('disabled')).toBeDefined()
    expect(pendingButton.attributes('title')).toBe('admin.groups.duplicating')

    resolveDuplicate({ ...sourceGroup, id: 43, name: 'Primary (Copy)', status: 'inactive' })
    await flushPromises()
    expect(wrapper.get('[data-testid="group-duplicate"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  // The duplicate endpoint rejects with an API error. The page displays the message and
  // releases its per-group pending state so a later action can retry duplication.
  it('shows the API error and restores the action when duplication fails', async () => {
    duplicateGroup.mockRejectedValueOnce(new Error('duplicate failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-more"]').trigger('click')
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(showError).toHaveBeenCalledWith('duplicate failed')
    await wrapper.get('[data-testid="group-more"]').trigger('click')
    expect(wrapper.get('[data-testid="group-duplicate"]').attributes('disabled')).toBeUndefined()
    wrapper.unmount()
  })

  // Duplication succeeds but the subsequent list refresh fails. The page retains the
  // duplication success result and reports loading separately instead of reclassifying the
  // saved copy as failed.
  it('does not report a successful duplicate as failed when the refresh fails', async () => {
    listGroups
      .mockResolvedValueOnce({
        items: [sourceGroup],
        total: 1,
        page: 1,
        page_size: 20,
        pages: 1,
      })
      .mockRejectedValueOnce(new Error('refresh failed'))
    const wrapper = mountView()
    await flushPromises()

    await wrapper.get('[data-testid="group-more"]').trigger('click')
    await wrapper.get('[data-testid="group-duplicate"]').trigger('click')
    await flushPromises()

    expect(showSuccess).toHaveBeenCalledWith('admin.groups.duplicateSuccess')
    expect(showError).toHaveBeenCalledWith('admin.groups.failedToLoad')
    expect(showError).not.toHaveBeenCalledWith('admin.groups.duplicateFailed')
    wrapper.unmount()
  })

  // Updating a group rejects with the standardized API error representation. The editor
  // displays the extracted message so administrators receive the server's useful explanation of
  // the failed save.
  it('shows the standardized API message when updating a group fails', async () => {
    updateGroup.mockRejectedValueOnce({
      status: 409,
      code: 409,
      message: 'group name already exists',
      reason: 'GROUP_EXISTS',
    })
    const wrapper = mountView()
    await flushPromises()

    const editButton = wrapper.findAll('button').find((button) => button.text() === 'common.edit')
    expect(editButton).toBeTruthy()
    await editButton!.trigger('click')
    await flushPromises()
    await wrapper.get('#edit-group-form').trigger('submit')
    await flushPromises()

    expect(updateGroup).toHaveBeenCalledTimes(1)
    expect(showError).toHaveBeenCalledWith('group name already exists')
    wrapper.unmount()
  })
})

useProtocolCatalogFixture()
