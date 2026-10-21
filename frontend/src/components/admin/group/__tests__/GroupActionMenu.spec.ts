import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import GroupActionMenu from '../GroupActionMenu.vue'
import type { AdminGroup } from '@/api/groups.types'

// GroupActionMenu.spec.ts checks group action forwarding and menu dismissal using a mounted
// menu and selected group fixture. The parent-facing events retain the group object, while
// duplication state controls that action independently from the remaining menu entries.

vi.mock('vue-i18n', async () => ({
  ...(await vi.importActual('vue-i18n')),
  useI18n: () => ({ t: (key: string) => key }),
}))

// group supplies the selected administrator record whose object identity must survive action
// forwarding through the menu events.
const group = { id: 42, name: 'Primary' } as AdminGroup
// mountMenu mounts the shared group in a visible positioned action menu. Duplication defaults
// to idle, and callers pass true when testing how a pending duplicate request changes available
// actions.
const mountMenu = (duplicating = false) =>
  mount(GroupActionMenu, {
    props: { show: true, group, position: { top: 80, left: 8 }, duplicating },
    global: { stubs: { Teleport: true } },
  })

describe('GroupActionMenu', () => {
  // Each action is clicked with a selected group in the open menu. The emitted action retains
  // that group object and the menu also requests closure.
  it.each([
    ['admin.groups.duplicate', 'duplicate'],
    ['admin.groups.rateMultipliers', 'rate-multipliers'],
    ['admin.groups.rpmOverrides', 'rpm-overrides'],
    ['common.delete', 'delete'],
  ])('%s 传递所选分组并关闭菜单', async (label, event) => {
    const wrapper = mountMenu()
    expect(wrapper.findAll('[role="menuitem"]')).toHaveLength(4)
    const button = wrapper.findAll('button').find((item) => item.text() === label)!
    await button.trigger('click')
    expect(wrapper.emitted(event)?.[0]).toEqual([group])
    expect(wrapper.emitted('close')).toHaveLength(1)
    wrapper.unmount()
  })

  // A duplicate request is already pending when the menu renders. Duplication is disabled while
  // the other group actions remain available for the same selected record.
  it('复制进行中禁用入口，其他菜单项仍然可用', async () => {
    const wrapper = mountMenu(true)
    const button = wrapper.get('[data-testid="group-duplicate"]')
    expect(button.attributes('disabled')).toBeDefined()
    await button.trigger('click')
    expect(wrapper.emitted('duplicate')).toBeUndefined()
    expect(wrapper.findAll('button:enabled')).toHaveLength(3)
    wrapper.unmount()
  })

  // The menu receives Escape and a backdrop click in separate interactions. Both dismissal
  // paths emit close so the parent can hide the floating action menu.
  it('按 Escape 或点击遮罩关闭', async () => {
    const wrapper = mountMenu()
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
    await wrapper.get('[aria-hidden="true"]').trigger('click')
    expect(wrapper.emitted('close')).toHaveLength(2)
    wrapper.unmount()
  })
})
