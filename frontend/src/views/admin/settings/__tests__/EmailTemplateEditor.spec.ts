import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import EmailTemplateEditor from '../EmailTemplateEditor.vue'

/**
 * EmailTemplateEditor.spec.ts checks the account-disabled email event in
 * Chinese and English interfaces. Mounted editor tests supply the backend
 * event identity and assert that local event descriptions override generic
 * server labels while the selected template request keeps that identity and
 * the expected locale.
 */

// Hoisted settings and notification mocks provide controlled server responses
// while allowing assertions on template identity and reported errors.
const { settings, showError } = vi.hoisted(() => ({
  settings: {
    getEmailTemplates: vi.fn(),
    getEmailTemplate: vi.fn(),
    previewEmailTemplate: vi.fn(),
  },
  showError: vi.fn(),
}))

vi.mock('@/api', () => ({ adminAPI: { settings } }))
vi.mock('@/stores', () => ({ useAppStore: () => ({ showError, showSuccess: vi.fn() }) }))

beforeEach(() => {
  vi.clearAllMocks()
  settings.getEmailTemplates.mockResolvedValue({
    events: [
      {
        value: 'content_moderation.account_disabled',
        label: 'Server fallback',
        category: 'risk_control',
      },
    ],
    locales: ['zh', 'en'],
    placeholders: [],
  })
  settings.getEmailTemplate.mockResolvedValue({
    subject: 'Subject',
    html: '<p>Body</p>',
    placeholders: [],
  })
  settings.previewEmailTemplate.mockResolvedValue({ subject: 'Subject', html: '<p>Body</p>' })
})

// Account-disabled event metadata keeps its account meaning when the editor
// selects localized labels from the backend event ID.
describe('邮件事件领域边界', () => {
  // The server supplies an account-disabled event with a generic label. Each
  // interface language must display its account-specific description and
  // request the matching template locale.
  it.each([
    ['zh', '内容审计禁用账号', '自动禁用用户账号'],
    ['en', 'Risk Control Account Disabled', 'automatically disables the user account'],
  ])('使用 %s 文案展示后端的用户封禁事件', async (locale, label, description) => {
    const i18n = createI18n({
      legacy: false,
      locale,
      messages: {},
      missingWarn: false,
      fallbackWarn: false,
    })
    const wrapper = mount(EmailTemplateEditor, {
      global: { plugins: [i18n], stubs: { Select: true } },
    })
    await flushPromises()
    expect(wrapper.text()).toContain(label)
    expect(wrapper.text()).toContain(description)
    expect(wrapper.text()).not.toContain('Server fallback')
    expect(settings.getEmailTemplate).toHaveBeenCalledWith(
      'content_moderation.account_disabled',
      locale,
    )
    expect(showError).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})
