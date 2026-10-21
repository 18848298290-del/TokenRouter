import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import KeyActionMenu from '../KeyActionMenu.vue'
import type { ApiKey } from '@/api/keys.types'
import { finishMotion, mockMotionEnvironment } from '@/__tests__/helpers/motion'

// KeyActionMenu.spec.ts mounts the key overflow menu with a fixed team-owned key and real
// event handling. The suite checks action payloads, conditional import visibility, and the
// overlay behavior while a closing panel completes its motion.

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

// The fixed team key supplies identity, secret, status, and zero usage fields shared by
// every menu action assertion.
const apiKey: ApiKey = {
  id: 7,
  user_id: 3,
  team_id: 2,
  scope: 'team',
  key: 'sk-test',
  name: 'team-key',
  group_id: 1,
  status: 'active',
  fast_mode_policy: 'follow_request',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-07-01T00:00:00Z',
  updated_at: '2026-07-01T00:00:00Z',
  current_concurrency: 0,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
}

describe('KeyActionMenu', () => {
  afterEach(() => {
    document.body.innerHTML = ''
    vi.restoreAllMocks()
  })

  // The parent clears the selected key as the menu closes during its exit animation. The
  // test protects immediate overlay removal and retention of inert leaving content until
  // motion completes.
  it('关闭时立即移除捕获层，清空选中 Key 后仍能完成面板退出', async () => {
    mockMotionEnvironment()
    const wrapper = mount(KeyActionMenu, {
      attachTo: document.body,
      props: { show: true, apiKey, position: { top: 80, left: 100 }, allowImport: true },
      global: { stubs: { transition: false, Icon: true } },
    })
    try {
      const menu = document.body.querySelector('[role="menu"]')!
      await wrapper.setProps({ show: false, apiKey: null, position: null })
      expect(document.body.querySelector('.z-menu-overlay')).toBeNull()
      expect(menu.id).toBe('key-action-menu-7')
      expect(menu.hasAttribute('inert')).toBe(true)
      expect(menu.textContent).toContain('keys.useKey')
      await finishMotion(menu)
      expect(document.body.querySelector('[role="menu"]')).toBeNull()
    } finally {
      wrapper.unmount()
    }
  })

  // A selected key exposes setup, imports, rotation, and deletion in its overflow menu. The
  // test checks action payload identity, the accumulated close-event count after each selected
  // action, and distinct import icon colors.
  it('将使用、tf/CCS 导入、轮换和删除收纳到更多菜单', async () => {
    const wrapper = mount(KeyActionMenu, {
      props: {
        show: true,
        apiKey,
        position: { top: 80, left: 100 },
        allowImport: true,
      },
      attachTo: document.body,
    })

    const menu = document.body.querySelector('[role="menu"]')
    expect(menu?.id).toBe('key-action-menu-7')
    expect(menu?.querySelectorAll('[role="menuitem"]')).toHaveLength(5)
    expect(document.body.textContent).toContain('keys.useKey')
    expect(document.body.textContent).toContain('keys.importToTf')
    expect(document.body.textContent).toContain('keys.importToCcSwitch')
    expect(document.body.textContent).toContain('common.delete')
    expect(document.body.textContent).toContain('keys.rotateKey')

    const tfButton = Array.from(document.body.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('keys.importToTf'),
    )
    const ccsButton = Array.from(document.body.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('keys.importToCcSwitch'),
    )
    expect(tfButton?.querySelector('path')?.getAttribute('d')).toBe(
      ccsButton?.querySelector('path')?.getAttribute('d'),
    )
    expect(tfButton?.querySelector('svg')?.classList.contains('text-blue-500')).toBe(true)
    expect(ccsButton?.querySelector('svg')?.classList.contains('text-violet-500')).toBe(true)
    tfButton?.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('import-tf')?.[0]).toEqual([apiKey])
    expect(wrapper.emitted('close')).toHaveLength(1)

    await wrapper.setProps({ show: true })
    const deleteButton = Array.from(document.body.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('common.delete'),
    )
    deleteButton?.click()
    await wrapper.vm.$nextTick()

    expect(wrapper.emitted('delete')?.[0]).toEqual([apiKey])
    expect(wrapper.emitted('close')).toHaveLength(2)

    const rotateButton = Array.from(document.body.querySelectorAll('button')).find((button) =>
      button.textContent?.includes('keys.rotateKey'),
    )
    rotateButton?.click()
    await wrapper.vm.$nextTick()
    expect(wrapper.emitted('rotate')?.[0]).toEqual([apiKey])
    expect(wrapper.emitted('close')).toHaveLength(3)
    wrapper.unmount()
  })

  // The optional client-import setting is disabled for an open menu. The test checks that
  // CCS import disappears while TokenFlux CLI import remains available to the user.
  it('按设置隐藏 CCS 导入入口', () => {
    const wrapper = mount(KeyActionMenu, {
      props: {
        show: true,
        apiKey,
        position: { top: 80, left: 100 },
        allowImport: false,
      },
      attachTo: document.body,
    })

    expect(document.body.textContent).not.toContain('keys.importToCcSwitch')
    expect(document.body.textContent).toContain('keys.importToTf')
    wrapper.unmount()
  })
})
