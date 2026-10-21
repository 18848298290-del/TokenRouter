import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import TeamActionMenu from '../TeamActionMenu.vue'
import type { AdminTeam } from '@/api/admin/teams'

// TeamActionMenu.spec.ts checks the administrator team menu through its teleported
// buttons and emitted events. A fixed team fixture supplies the action payload, and the
// test clicks statistics to verify that the parent receives the selected team and a
// close request.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key, // Translation key passthrough.
    }), // Translation composable substitute.
  }
})

// team provides the administrator menu's selected record, including owner and member
// metadata required by the shared AdminTeam fixture type.
const team: AdminTeam = {
  id: 7, // Fixture record identifier.
  name: '平台团队', // Fixture display name.
  status: 'active', // Team availability status.
  member_limit: 10, // Configured membership limit.
  member_count: 2, // Current member count.
  created_at: '2026-07-01T00:00:00Z', // Record creation timestamp.
  updated_at: '2026-07-01T00:00:00Z', // Record update timestamp.
  owner_user_id: 1, // Team owner identifier.
  owner_email: 'owner@example.com', // Team owner email.
}

describe('TeamActionMenu', () => {
  afterEach(() => {
    document.body.innerHTML = ''
  })

  // A visible menu exposes details, statistics, and dissolve actions for one selected
  // team. Clicking statistics must emit that exact team followed by one close request.
  it('提供详情、统计和解散三个更多操作', async () => {
    const wrapper = mount(TeamActionMenu, {
      props: {
        show: true, // Parent-controlled dialog visibility.
        team, // Selected team record.
        position: {
          top: 80, // Viewport top offset in pixels.
          left: 100, // Viewport left offset in pixels.
        }, // Viewport menu coordinates.
      }, // Accepted component props.
      attachTo: document.body, // Mounted DOM container.
    })

    const buttons = Array.from(document.body.querySelectorAll('button'))
    expect(document.body.textContent).toContain('team.viewDetails')
    expect(document.body.textContent).toContain('team.viewStatistics')
    expect(document.body.textContent).toContain('team.dissolve')

    buttons.find((button) => button.textContent?.includes('team.viewStatistics'))?.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('statistics')?.[0]).toEqual([team])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })
})
