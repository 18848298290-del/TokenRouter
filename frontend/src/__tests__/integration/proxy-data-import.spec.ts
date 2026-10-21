import { describe, it, expect, vi, beforeEach } from 'vitest'
import { mount } from '@vue/test-utils'
import ImportDataModal from '@/components/admin/proxy/ImportDataModal.vue'

// proxy-data-import.spec.ts checks proxy import validation through the
// mounted form. A dialog shell and mocked application notifications keep
// submission observable while missing-file and malformed-JSON cases verify
// the error keys presented to the user.

// Notification spies record form feedback through the mocked application
// store and are cleared before each test.
const showError = vi.fn()
const showSuccess = vi.fn()

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError,
    showSuccess,
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    proxies: {
      importData: vi.fn(),
    },
  },
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key,
  }),
}))

describe('Proxy ImportDataModal', () => {
  beforeEach(() => {
    showError.mockReset()
    showSuccess.mockReset()
  })

  // The proxy import form is submitted before a file is selected. The
  // application receives the selection error key so the user can supply an
  // input.
  it('未选择文件时提示错误', async () => {
    const wrapper = mount(ImportDataModal, {
      props: { show: true },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        },
      },
    })

    await wrapper.find('form').trigger('submit')
    expect(showError).toHaveBeenCalledWith('admin.proxies.dataImportSelectFile')
  })

  // The selected proxy import file contains invalid JSON text. Submitting
  // the form reports the parse failure key through the application error
  // notification.
  it('无效 JSON 时提示解析失败', async () => {
    const wrapper = mount(ImportDataModal, {
      props: { show: true },
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        },
      },
    })

    const input = wrapper.find('input[type="file"]')
    const file = new File(['invalid json'], 'data.json', { type: 'application/json' })
    Object.defineProperty(file, 'text', {
      value: () => Promise.resolve('invalid json'),
    })
    Object.defineProperty(input.element, 'files', {
      value: [file],
    })

    await input.trigger('change')
    await wrapper.find('form').trigger('submit')
    await Promise.resolve()

    expect(showError).toHaveBeenCalledWith('admin.proxies.dataImportParseFailed')
  })
})
