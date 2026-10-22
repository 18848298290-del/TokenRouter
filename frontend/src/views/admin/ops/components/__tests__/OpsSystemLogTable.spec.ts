import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsSystemLogTable from '../OpsSystemLogTable.vue'
import enLocale from '@/i18n/locales/en'
import zhLocale from '@/i18n/locales/zh'

// OpsSystemLogTable.spec.ts checks host filters and localized failure feedback
// in the system log table. API doubles expose list and configuration requests,
// confirmation is controlled in tests, and shipped dictionaries supply the
// English and Chinese messages shown when those requests fail.

// API and notification doubles capture host filters, cleanup requests, runtime
// configuration writes, and the error messages reported by each failed operation.
const mockListSystemLogs = vi.fn()
const mockCleanupSystemLogs = vi.fn()
const mockGetSystemLogSinkHealth = vi.fn()
const mockGetRuntimeLogConfig = vi.fn()
const mockUpdateRuntimeLogConfig = vi.fn()
const mockResetRuntimeLogConfig = vi.fn()
const mockShowError = vi.fn()
const mockShowSuccess = vi.fn()

// testLanguage selects the shipped dictionary for system-log error messages;
// unrelated translations retain their keys for existing control selectors.
let testLanguage: 'en' | 'zh' = 'en'

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    listSystemLogs: (...args: any[]) => mockListSystemLogs(...args),
    cleanupSystemLogs: (...args: any[]) => mockCleanupSystemLogs(...args),
    getSystemLogSinkHealth: (...args: any[]) => mockGetSystemLogSinkHealth(...args),
    getRuntimeLogConfig: (...args: any[]) => mockGetRuntimeLogConfig(...args),
    updateRuntimeLogConfig: (...args: any[]) => mockUpdateRuntimeLogConfig(...args),
    resetRuntimeLogConfig: (...args: any[]) => mockResetRuntimeLogConfig(...args),
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
    useI18n: () => ({
      t: (key: string) => {
        const messages = (testLanguage === 'zh' ? zhLocale : enLocale).admin.ops.systemLogs
        if (key === 'admin.ops.systemLogs.loadFailed') return messages.loadFailed
        if (key === 'admin.ops.systemLogs.saveConfigFailed') return messages.saveConfigFailed
        if (key === 'admin.ops.systemLogs.resetConfigFailed') return messages.resetConfigFailed
        return key
      },
    }),
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
  // Restore browser and console spies after each case so a failed request does
  // not suppress diagnostics or carry confirmation behavior into later fixtures.
  afterEach(() => vi.restoreAllMocks())

  beforeEach(() => {
    vi.clearAllMocks()
    testLanguage = 'en'
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
    mockUpdateRuntimeLogConfig.mockResolvedValue(runtimeConfig)
    mockResetRuntimeLogConfig.mockResolvedValue(runtimeConfig)
  })

  // List, save, and reset requests fail without a server display message.
  // Each action must report its own translated toast in both supported languages.
  it.each([
    ['en', 'load', 'Failed to load system logs'],
    ['zh', 'load', '系统日志加载失败'],
    ['en', 'save', 'Failed to save logging configuration'],
    ['zh', 'save', '保存日志配置失败'],
    ['en', 'reset', 'Failed to reset logging configuration'],
    ['zh', 'reset', '回滚日志配置失败'],
  ] as const)('localizes %s %s failures', async (language, action, expected) => {
    testLanguage = language
    vi.spyOn(console, 'error').mockImplementation(() => {})
    const request = {
      load: mockListSystemLogs,
      save: mockUpdateRuntimeLogConfig,
      reset: mockResetRuntimeLogConfig,
    }[action]
    request.mockRejectedValueOnce(new Error('network unavailable'))
    const wrapper = mount(OpsSystemLogTable, {
      global: { stubs: { Select: SelectStub, Pagination: PaginationStub } },
    })
    await flushPromises()

    if (action !== 'load') {
      const label = action === 'save' ? '保存并生效' : '回滚默认值'
      const button = wrapper.findAll('button').find((item) => item.text() === label)
      expect(button).toBeDefined()
      await button!.trigger('click')
      await flushPromises()
    }

    expect(request).toHaveBeenCalled()
    expect(mockShowError).toHaveBeenLastCalledWith(expected)
    expect(console.error).toHaveBeenCalledWith(
      expect.stringMatching(/^\[OpsSystemLogTable\] Failed to /),
      expect.any(Error),
    )
    wrapper.unmount()
  })

  // A failed query carries a server-specific display detail in a Chinese session.
  // The toast must retain that detail instead of replacing it with generic translated text.
  it('retains server details for a failed log query', async () => {
    testLanguage = 'zh'
    vi.spyOn(console, 'error').mockImplementation(() => {})
    mockListSystemLogs.mockRejectedValueOnce({
      response: { data: { detail: 'Log storage is unavailable' } },
    })
    const wrapper = mount(OpsSystemLogTable, {
      global: { stubs: { Select: SelectStub, Pagination: PaginationStub } },
    })
    await flushPromises()
    expect(mockShowError).toHaveBeenLastCalledWith('Log storage is unavailable')
    wrapper.unmount()
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
