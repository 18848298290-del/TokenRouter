import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsSystemLogTable from '../OpsSystemLogTable.vue'
import enLocale from '@/i18n/locales/en'
import zhLocale from '@/i18n/locales/zh'

// OpsSystemLogTable.spec.ts checks host-filter propagation and cleanup rejection
// feedback in the system log table. API doubles expose list and cleanup
// payloads, confirmation is controlled in tests, and direct locale assertions
// verify the Host label in both languages.

// API and notification doubles capture host filtering, cleanup results, and the
// translated reason reported for rejected cleanup requests.
const mockListSystemLogs = vi.fn()
const mockCleanupSystemLogs = vi.fn()
const mockGetSystemLogSinkHealth = vi.fn()
const mockGetRuntimeLogConfig = vi.fn()
const mockShowError = vi.fn()
const mockShowSuccess = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    listSystemLogs: (...args: any[]) => mockListSystemLogs(...args),
    cleanupSystemLogs: (...args: any[]) => mockCleanupSystemLogs(...args),
    getSystemLogSinkHealth: (...args: any[]) => mockGetSystemLogSinkHealth(...args),
    getRuntimeLogConfig: (...args: any[]) => mockGetRuntimeLogConfig(...args),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({
    showError: mockShowError,
    showSuccess: mockShowSuccess,
  }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  }
})

// Selector and pagination doubles leave host inputs and action buttons available
// while isolating unrelated dropdown and paging behavior.
const SelectStub = defineComponent({
  name: 'SelectControlStub',
  props: {
    modelValue: {
      type: [String, Number],
      default: '',
    }, // selected control fixture value
  },
  emits: ['update:modelValue'],
  template: '<div class="select-stub" />',
})

const PaginationStub = defineComponent({
  name: 'PaginationStub',
  template: '<div class="pagination-stub" />',
})

// Runtime and sink fixtures let initial component reads settle while host and
// cleanup assertions focus on their own request fields.
const runtimeConfig = {
  level: 'info',
  enable_sampling: false,
  sampling_initial: 100,
  sampling_thereafter: 100,
  caller: true,
  stacktrace_level: 'error',
  retention_days: 30,
}

const sinkHealth = {
  queue_depth: 0,
  queue_capacity: 5000,
  dropped_count: 0,
  write_failed_count: 0,
  written_count: 1,
  avg_write_delay_ms: 0,
}

describe('OpsSystemLogTable host support', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    mockListSystemLogs.mockResolvedValue({
      items: [
        {
          id: 1,
          created_at: '2026-07-14T00:10:01Z',
          host: 'api-node-1',
          level: 'warn',
          component: 'app',
          message: 'request failed',
        },
      ],
      total: 1,
      page: 1,
      page_size: 20,
    })
    mockCleanupSystemLogs.mockResolvedValue({ deleted: 1 })
    mockGetSystemLogSinkHealth.mockResolvedValue(sinkHealth)
    mockGetRuntimeLogConfig.mockResolvedValue(runtimeConfig)
  })

  // A host value is displayed and a different host is entered with surrounding
  // spaces. Both list and confirmed cleanup requests must carry the trimmed
  // selected host.
  it('renders the host and sends it with list and cleanup filters', async () => {
    const wrapper = mount(OpsSystemLogTable, {
      global: {
        stubs: {
          Select: SelectStub,
          Pagination: PaginationStub,
        },
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('api-node-1')

    const hostLabel = wrapper
      .findAll('label')
      .find((label) => label.text().includes('admin.ops.systemLogs.host'))
    expect(hostLabel).toBeDefined()
    await hostLabel!.find('input').setValue(' api-node-2 ')

    const searchButton = wrapper.findAll('button').find((button) => button.text() === '查询')
    expect(searchButton).toBeDefined()
    await searchButton!.trigger('click')
    await flushPromises()

    expect(mockListSystemLogs).toHaveBeenLastCalledWith(
      expect.objectContaining({ host: 'api-node-2' }),
    )

    const cleanupButton = wrapper
      .findAll('button')
      .find((button) => button.text() === '按当前筛选清理')
    expect(cleanupButton).toBeDefined()
    await cleanupButton!.trigger('click')
    await flushPromises()

    expect(mockCleanupSystemLogs).toHaveBeenCalledWith(
      expect.objectContaining({ host: 'api-node-2' }),
    )
  })

  // The server rejects cleanup because no filtering condition was supplied. The
  // table must report the translated missing-filter reason through the
  // application error notification callback.
  it('shows the localized reason when cleanup has no filter condition', async () => {
    mockCleanupSystemLogs.mockRejectedValueOnce({
      reason: 'OPS_SYSTEM_LOG_CLEANUP_FILTER_REQUIRED',
      message: 'cleanup filter required',
    })
    const wrapper = mount(OpsSystemLogTable, {
      global: {
        stubs: {
          Select: SelectStub,
          Pagination: PaginationStub,
        },
      },
    })
    await flushPromises()

    const cleanupButton = wrapper
      .findAll('button')
      .find((button) => button.text() === '按当前筛选清理')
    expect(cleanupButton).toBeDefined()
    await cleanupButton!.trigger('click')
    await flushPromises()

    expect(mockShowError).toHaveBeenCalledWith('admin.ops.systemLogs.cleanupFilterRequired')
  })

  // Both shipped language catalogs are inspected for the system-log host label.
  // Each must provide the same Host wording at the exact key used by the
  // component.
  it.each([
    ['zh', zhLocale],
    ['en', enLocale],
  ])('defines the Host translation for %s', (_name, locale) => {
    expect(locale.admin.ops.systemLogs.host).toBe('Host')
  })
})
