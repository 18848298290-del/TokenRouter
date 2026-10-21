import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'

// Localized fixture messages keep invitation labels readable while assertions inspect
// preview values and accepted or declined event payloads.
const messages = vi.hoisted<Record<string, string>>(() => ({
  'team.inviteActionTitle': '团队邀请',
  'team.inviteActionDescription': '请确认以下邀请信息后选择是否加入团队。',
  'team.invitedTeam': '团队名称',
  'team.inviter': '邀请人',
  'team.inviterEmail': '邀请人邮箱',
  'team.invitationExpiresAt': '邀请有效期',
  'team.decline': '拒绝',
  'team.accept': '接受',
  'common.close': '关闭',
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => messages[key] ?? key,
    }),
  }
})

import TeamInvitationDialog from '../TeamInvitationDialog.vue'

// TeamInvitationDialog.spec.ts mounts the invitation dialog with fixed preview data and a
// slot-preserving dialog shell. The suite checks invitation identity, acceptance and decline
// payloads, and the transition from loading placeholders to details or errors.

// BaseDialogStub retains visibility, heading, body, and footer slots so tests inspect the
// invitation component's own content and actions.
const BaseDialogStub = defineComponent({
  props: {
    show: Boolean,
    title: String,
  },
  emits: ['close'],
  template:
    '<div v-if="show" data-testid="dialog"><h2>{{ title }}</h2><slot /><slot name="footer" /></div>',
})

// mountDialog mounts a loaded invitation with optional prop overrides for loading or failure
// cases. The caller can replace preview state while retaining the same production dialog
// handlers.
const mountDialog = (overrides: Record<string, unknown> = {}) =>
  mount(TeamInvitationDialog, {
    props: {
      show: true,
      loading: false,
      resolving: false,
      error: '',
      preview: {
        team_name: '词元流动',
        inviter_name: '喵窝',
        inviter_email: 'owner@example.com',
        expires_at: '2026-08-04T00:22:40+08:00',
      },
      ...overrides,
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Icon: true,
      },
    },
  })

describe('TeamInvitationDialog', () => {
  // A loaded invitation includes its team, inviter identity, email, and expiry. The test
  // checks that the dialog exposes those details so the user can identify the invitation.
  it('展示团队、邀请人、邀请邮箱和有效期', () => {
    const wrapper = mountDialog()

    expect(wrapper.get('[data-testid="invitation-details"]').text()).toContain('词元流动')
    expect(wrapper.text()).toContain('喵窝')
    expect(wrapper.text()).toContain('owner@example.com')
    expect(wrapper.text()).toContain('2026')
  })

  // The user selects each resolution action in the loaded invitation dialog. The test
  // protects distinct declined and accepted event payloads sent to the page owner.
  it('分别发送拒绝和接受事件', async () => {
    const wrapper = mountDialog()
    const buttons = wrapper.findAll('button')

    await buttons[0].trigger('click')
    await buttons[1].trigger('click')

    expect(wrapper.emitted('resolve')).toEqual([['declined'], ['accepted']])
  })

  // Invitation preview loading completes with either details or an error. The test protects
  // skeleton removal and the correct resulting content for both completion paths.
  it('加载期间用详情骨架占位，结束后恢复详情或错误提示', async () => {
    const wrapper = mountDialog({ loading: true })
    expect(wrapper.get('[data-testid="invitation-loading"]').attributes('aria-busy')).toBe('true')
    expect(wrapper.findAll('.skeleton').length).toBeGreaterThan(0)
    expect(wrapper.find('[data-testid="invitation-details"]').exists()).toBe(false)

    await wrapper.setProps({ loading: false })
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="invitation-details"]').text()).toContain('词元流动')

    await wrapper.setProps({ loading: true })
    await wrapper.setProps({ loading: false, preview: null, error: '邀请已失效' })
    expect(wrapper.find('[data-loading-skeleton]').exists()).toBe(false)
    expect(wrapper.get('[data-testid="invitation-error"]').text()).toContain('邀请已失效')
    wrapper.unmount()
  })
})
