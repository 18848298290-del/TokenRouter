import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createPinia, setActivePinia } from 'pinia'
import { createMemoryHistory, createRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import type { User } from '@/api/identity.types'
import RequestIdLink from '../RequestIdLink.vue'

/**
 * RequestIdLink.spec.ts checks request diagnostic navigation and copy actions under
 * changing authentication roles. A memory router and fresh Pinia store provide
 * realistic administrator links, while a clipboard substitute records copy requests
 * and automatic unmount cleans up feedback timers.
 */

/**
 * The hoisted clipboard spy records requested identifiers and supplies the result
 * consumed by the mounted request link copy feedback.
 */
const { copyToClipboard } = vi.hoisted(() => ({ copyToClipboard: vi.fn() }))
vi.mock('vue-i18n', async (original) => ({
  ...(await original<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key }),
}))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard }) }))
enableAutoUnmount(afterEach)
afterEach(() => setActivePinia(undefined))

/**
 * render mounts a request identifier with an authenticated role and memory router. It
 * waits for router readiness before mounting, returns the mutable auth store for role
 * changes, and lets the shared teardown unmount the component.
 */
async function render(role: 'admin' | 'user', link = true) {
  const pinia = createPinia()
  setActivePinia(pinia)
  const auth = useAuthStore()
  auth.user = { id: 1, role } as User
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }],
  })
  await router.push('/usage')
  await router.isReady()
  const wrapper = mount(RequestIdLink, {
    props: { value: 'request-id', link },
    global: { plugins: [pinia, router], stubs: { Icon: true } },
  })
  return { wrapper, auth }
}

describe('请求 ID 的诊断入口权限', () => {
  beforeEach(() => copyToClipboard.mockReset().mockResolvedValue(true))

  /**
   * An ordinary user views a populated request identifier and clicks copy. The
   * component exposes no diagnostic link and delegates the identifier to clipboard
   * feedback.
   */
  it('普通用户可以复制 ID，页面上没有诊断详情链接', async () => {
    const { wrapper } = await render('user')
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.text()).toContain('request-id')
    await wrapper.get('button').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('request-id', 'requests.copied')
  })

  /**
   * An administrator receives a diagnostic link before their role changes to user. The
   * component removes navigation reactively and keeps the copy action available
   * afterward.
   */
  it('管理员从个人使用记录打开管理员查询页，角色变化后移除链接', async () => {
    const { wrapper, auth } = await render('admin')
    expect(wrapper.get('a').attributes('href')).toBe('/admin/requests?request_id=request-id')
    auth.user = { id: 1, role: 'user' } as User
    await flushPromises()
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(true)
  })

  /**
   * An administrator opens a request identifier with linking disabled by the caller.
   * The component renders no diagnostic anchor and retains its independent copy
   * button.
   */
  it('详情卡片可以关闭管理员链接，保留复制按钮', async () => {
    const { wrapper } = await render('admin', false)
    expect(wrapper.find('a').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(true)
  })
})
