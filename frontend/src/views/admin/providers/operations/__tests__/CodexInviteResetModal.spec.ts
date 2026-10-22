import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ref } from 'vue'
import CodexInviteResetModal from '../CodexInviteResetModal.vue'
import enLocale from '@/i18n/locales/en'
import zhLocale from '@/i18n/locales/zh'

// CodexInviteResetModal.spec.ts checks Codex invite availability and
// reset-credit selection in the operations dialog. Status responses
// distinguish invitations from usable credits, and consume spies reveal
// whether the earliest credit or an unspecified credit is requested. Shipped
// language catalogs verify that known availability reasons follow language
// changes while unknown server explanations remain visible.

// Invite status, send, consume, and notification spies keep invitation
// availability independent from usable reset credits and expose the chosen
// credit identifier.
const { getStatusMock, sendInviteMock, consumeMock, showSuccessMock, showErrorMock } = vi.hoisted(
  () => ({
    getStatusMock: vi.fn(),
    sendInviteMock: vi.fn(),
    consumeMock: vi.fn(),
    showSuccessMock: vi.fn(),
    showErrorMock: vi.fn(),
  }),
)

// testLanguage drives the known invitation message through shipped translations;
// its reactive value lets an open dialog respond to a language change.
const testLanguage = ref<'en' | 'zh'>('en')

vi.mock('@/api/admin', () => ({
  adminAPI: {
    providers: {
      getCodexInviteResetStatus: getStatusMock,
      sendCodexInviteResetInvite: sendInviteMock,
      consumeCodexInviteReset: consumeMock,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: showSuccessMock,
    showError: showErrorMock,
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.providers.inviteResetInviteUnavailable') {
          return (testLanguage.value === 'zh' ? zhLocale : enLocale).admin.providers
            .inviteResetInviteUnavailable
        }
        if (key === 'admin.providers.inviteResetEmailHint') return `max-${params?.max}`
        if (params?.time) return `${key}:${params.time}`
        return key
      },
    }),
  }
})

// provider identifies the OpenAI OAuth account shared by invitation and credit
// cases, giving consume assertions a stable provider ID to inspect.
const provider = {
  id: 42,
  name: 'OpenAI OAuth',
  platform: 'openai',
  type: 'oauth',
  status: 'active',
}

// mountModal creates a hidden invite-reset dialog for provider forty-two.
// Callers configure the status mock, then set show to true and await loading
// before interacting with credit or invitation controls.
function mountModal() {
  return mount(CodexInviteResetModal, {
    props: {
      show: false,
      provider: provider as any,
    },
    global: {
      stubs: {
        BaseDialog: {
          props: [
            'show', // dialog or menu visibility
          ],
          template: '<div v-if="show"><slot /><slot name="footer" /></div>',
        },
        Select: {
          props: [
            'modelValue', // current controlled input value
            'options', // available typed selection options
          ],
          emits: [
            'update:modelValue', // replacement controlled input value
          ],
          template: '<div class="select-stub">{{ options?.[0]?.label }}</div>',
        },
        Icon: true,
      },
    },
  })
}

describe('CodexInviteResetModal', () => {
  beforeEach(() => {
    testLanguage.value = 'en'
    getStatusMock.mockReset()
    sendInviteMock.mockReset()
    consumeMock.mockReset()
    showSuccessMock.mockReset()
    showErrorMock.mockReset()
  })

  // The invitation service is unavailable while one reset credit remains
  // usable. Sending invitations stays disabled, existing credit details remain
  // visible, and consuming a reset stays enabled. A known reason uses the selected
  // language even when the server supplies an English diagnostic message.
  it.each(['en', 'zh'] as const)(
    'localizes unavailable invitations in %s while keeping resets usable',
    async (language) => {
      testLanguage.value = language
      getStatusMock.mockResolvedValue({
        referral_key: 'codex_referral_persistent_invite',
        invite_available: false,
        invite_unavailable_reason: 'CODEX_INVITE_RESET_REFERRAL_UNAVAILABLE',
        invite_unavailable_message:
          'Codex referral invitations are temporarily unavailable; existing resets can still be used',
        requires_consent: true,
        available_count: 1,
        credits: [
          {
            id: 'credit-1',
            status: 'available',
            title: 'Reset',
            expires_at: '2026-07-03T04:05:06Z',
          },
        ],
      })

      const wrapper = mountModal()
      await wrapper.setProps({ show: true })
      await flushPromises()

      const expected = (language === 'zh' ? zhLocale : enLocale).admin.providers
        .inviteResetInviteUnavailable
      expect(wrapper.text()).toContain(expected)
      expect(wrapper.text()).not.toContain(
        'Codex referral invitations are temporarily unavailable; existing resets can still be used',
      )

      testLanguage.value = language === 'zh' ? 'en' : 'zh'
      await flushPromises()
      const translated = (testLanguage.value === 'zh' ? zhLocale : enLocale).admin.providers
        .inviteResetInviteUnavailable
      expect(wrapper.text()).toContain(translated)

      const buttons = wrapper.findAll('button')
      const consumeButton = buttons.find((button) =>
        button.text().includes('admin.providers.inviteResetUseReset'),
      )
      const sendButton = buttons.find((button) =>
        button.text().includes('admin.providers.inviteResetSendInvite'),
      )

      expect(consumeButton?.attributes('disabled')).toBeUndefined()
      expect(sendButton?.attributes('disabled')).toBeDefined()
      expect(wrapper.text()).toContain('admin.providers.inviteResetCreditExpirations')
      expect(wrapper.text()).toContain('admin.providers.inviteResetCreditExpiresAtFull:')

      await sendButton!.trigger('click')
      expect(sendInviteMock).not.toHaveBeenCalled()
      wrapper.unmount()
    },
  )

  // Status retrieval, invitation submission, and credit consumption can reject with
  // the known availability reason. Each action must display its localized explanation
  // even when the rejection carries the backend's English message.
  it.each([
    ['en', 'load'],
    ['zh', 'load'],
    ['en', 'send'],
    ['zh', 'send'],
    ['en', 'consume'],
    ['zh', 'consume'],
  ] as const)('localizes %s %s API rejections', async (language, action) => {
    testLanguage.value = language
    getStatusMock.mockResolvedValue({
      invite_available: true,
      requires_consent: false,
      available_count: 1,
      credits: [],
    })
    const request = { load: getStatusMock, send: sendInviteMock, consume: consumeMock }[action]
    request.mockRejectedValueOnce({
      reason: 'CODEX_INVITE_RESET_REFERRAL_UNAVAILABLE',
      message:
        'Codex referral invitations are temporarily unavailable; existing resets can still be used',
    })
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()
    if (action !== 'load') {
      if (action === 'send') {
        await wrapper.find('textarea').setValue('invitee@example.com')
      }
      const key =
        action === 'send'
          ? 'admin.providers.inviteResetSendInvite'
          : 'admin.providers.inviteResetUseReset'
      const button = wrapper.findAll('button').find((item) => item.text().includes(key))
      expect(button).toBeDefined()
      await button!.trigger('click')
      await flushPromises()
    }
    const expected = (language === 'zh' ? zhLocale : enLocale).admin.providers
      .inviteResetInviteUnavailable
    expect(request).toHaveBeenCalled()
    expect(wrapper.text()).toContain(expected)
    expect(wrapper.text()).not.toContain(
      'Codex referral invitations are temporarily unavailable; existing resets can still be used',
    )
    if (action === 'load') expect(showErrorMock).toHaveBeenLastCalledWith(expected)
    wrapper.unmount()
  })

  // An unrecognized reason has a server explanation or no display text.
  // The dialog retains supplied detail and uses its translated default for an empty message.
  it.each([
    ['en', 'A provider-specific invitation restriction'],
    ['zh', 'A provider-specific invitation restriction'],
    ['en', ''],
    ['zh', ''],
  ] as const)(
    'handles unknown invitation reasons in %s with message %j',
    async (language, message) => {
      testLanguage.value = language
      getStatusMock.mockResolvedValue({
        invite_available: false,
        invite_unavailable_reason: 'PROVIDER_SPECIFIC_INVITATION_RESTRICTION',
        invite_unavailable_message: message,
        available_count: 0,
        credits: [],
      })
      const wrapper = mountModal()
      await wrapper.setProps({ show: true })
      await flushPromises()
      const expected =
        message ||
        (language === 'zh' ? zhLocale : enLocale).admin.providers.inviteResetInviteUnavailable
      expect(wrapper.text()).toContain(expected)
      wrapper.unmount()
    },
  )

  // Status reports an invitation with no reward grant. The dialog labels that
  // grant as none and does not describe it as workspace credits.
  it('无奖励邀请显示无奖励而不是 Workspace credits', async () => {
    getStatusMock.mockResolvedValue({
      referral_key: 'codex_referral_persistent_invite',
      invite_available: true,
      requires_consent: true,
      has_rewards: false,
      grant_type: 'none',
      available_count: 0,
      credits: [],
    })

    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.providers.inviteResetGrantTypeNone')
    expect(wrapper.text()).not.toContain('admin.providers.inviteResetGrantTypeWorkspaceCredits')
  })

  // Available reset credits arrive with the later expiration first. Consuming
  // the default selection sends the earliest-expiring credit ID with the
  // current provider ID.
  it('按过期时间升序选择默认重置机会', async () => {
    getStatusMock.mockResolvedValue({
      referral_key: 'codex_referral_persistent_invite',
      invite_available: true,
      requires_consent: true,
      available_count: 2,
      credits: [
        { id: 'credit-late', status: 'available', expires_at: '2026-07-05T04:05:06Z' },
        { id: 'credit-early', status: 'available', expires_at: '2026-07-03T04:05:06Z' },
      ],
    })
    consumeMock.mockResolvedValue({
      code: 'reset',
      redeem_request_id: 'redeem-1',
      windows_reset: 1,
    })

    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const consumeButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.providers.inviteResetUseReset'))
    await consumeButton!.trigger('click')
    await flushPromises()

    expect(consumeMock).toHaveBeenCalledWith(42, 'credit-early')
  })

  // Status reports a usable count without individual credit records. The
  // dialog explains the missing details and consumes a generic reset with an
  // undefined credit ID.
  it('usage 有次数但没有明细时使用通用重置', async () => {
    getStatusMock.mockResolvedValue({
      referral_key: 'codex_referral_persistent_invite',
      invite_available: false,
      invite_unavailable_message: '邀请暂不可用',
      requires_consent: true,
      available_count: 1,
      credits: [],
    })
    consumeMock.mockResolvedValue({
      code: 'reset',
      redeem_request_id: 'redeem-2',
      windows_reset: 1,
    })

    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.text()).toContain('admin.providers.inviteResetCreditDetailsUnavailable')
    const consumeButton = wrapper
      .findAll('button')
      .find((button) => button.text().includes('admin.providers.inviteResetUseReset'))
    expect(consumeButton?.attributes('disabled')).toBeUndefined()

    await consumeButton!.trigger('click')
    await flushPromises()

    expect(consumeMock).toHaveBeenCalledWith(42, undefined)
  })
})
