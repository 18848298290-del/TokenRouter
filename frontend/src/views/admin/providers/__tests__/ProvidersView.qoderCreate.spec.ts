import { useProtocolCatalogFixture } from '@/__tests__/helpers/protocolCatalog'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import ProvidersView from '../ProvidersView.vue'

// ProvidersView.qoderCreate.spec.ts checks the provider page creation entry
// and its initial modal platform. The action stub renders the standard create
// button and optional shortcut slot, exposing whether page wiring preselects a
// vendor.

// The list and metadata spies provide empty page results so creation-entry
// assertions can observe modal wiring without provider row interactions.
const {
  listProviders,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroupsIncludingInactive,
} = vi.hoisted(() => ({
  listProviders: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroupsIncludingInactive: vi.fn(),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      list: listProviders,
      listWithEtag,
      getBatchTodayStats,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn(),
    },
    proxies: {
      getAll: getAllProxies,
    },
    groups: {
      getAllIncludingInactive: getAllGroupsIncludingInactive,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
    showWarning: vi.fn(),
  }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token',
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

// ProviderTableActionsStub exposes the standard create event and shortcut
// slot, letting the test distinguish the default button from vendor-specific
// entries.
const ProviderTableActionsStub = {
  emits: [
    'create', // open provider creation dialog
    'refresh', // request provider list refresh
  ],
  template: `
    <div>
      <slot name="after" />
      <button data-test="default-create" type="button" @click="$emit('create')">create</button>
      <slot name="afterCreate" />
    </div>
  `,
}

// CreateProviderModalStub renders show and initialPlatform as observable
// attributes so the page creation handler can be checked without running
// authorization.
const CreateProviderModalStub = {
  props: [
    'show', // dialog or menu visibility
    'initialPlatform', // initial provider platform selection
  ],
  template: `
    <div
      data-test="create-provider-modal"
      :data-show="String(show)"
      :data-initial-platform="initialPlatform || ''"
    />
  `,
}

// mountView renders the standard creation action and observable modal props.
// Configure list metadata first and await pending requests before using the
// create button to inspect the initial platform selection.
function mountView() {
  return mount(ProvidersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template:
            '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>',
        },
        DataTable: { template: '<div />' },
        HelpTooltip: true,
        Pagination: true,
        ConfirmDialog: true,
        ProviderTableActions: ProviderTableActionsStub,
        ProviderTableFilters: { template: '<div />' },
        ProviderBulkActionsBar: true,
        ProviderActionMenu: true,
        ImportDataModal: true,
        ReAuthProviderModal: true,
        ProviderTestModal: true,
        ProviderStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        TLSFingerprintRoutersModal: true,
        CreateProviderModal: CreateProviderModalStub,
        EditProviderModal: true,
        BulkEditProviderModal: true,
        PlatformTypeBadge: true,
        ProviderCapacityCell: true,
        ProviderStatusIndicator: true,
        ProviderTodayStatsCell: true,
        ProviderGroupsCell: true,
        ProviderUsageCell: true,
        Icon: true,
      },
    },
  })
}

describe('admin ProvidersView Qoder create entry', () => {
  beforeEach(() => {
    // Discard saved page preferences before loading the empty provider list so
    // the create-entry check starts without retained table state.
    localStorage.clear()

    listProviders.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroupsIncludingInactive.mockReset()

    listProviders.mockResolvedValue({
      items: [],
      total: 0,
      page: 1,
      page_size: 20,
      pages: 0,
    })
    listWithEtag.mockResolvedValue({
      notModified: true,
      etag: null,
      data: null,
    })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroupsIncludingInactive.mockResolvedValue([])
  })

  // The page renders its standard creation control with no Qoder shortcut.
  // Clicking that control opens the creation modal with an empty initial
  // platform selection.
  it('uses the standard create entry and does not render the Qoder shortcut', async () => {
    const wrapper = mountView()
    await flushPromises()

    expect(wrapper.find('[data-test="create-qoder-provider"]').exists()).toBe(false)

    await wrapper.get('[data-test="default-create"]').trigger('click')
    await flushPromises()

    const modal = wrapper.get('[data-test="create-provider-modal"]')
    expect(modal.attributes('data-show')).toBe('true')
    expect(modal.attributes('data-initial-platform')).toBe('')
  })
})

useProtocolCatalogFixture()
