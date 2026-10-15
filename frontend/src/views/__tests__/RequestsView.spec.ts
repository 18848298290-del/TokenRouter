import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { createMemoryHistory, createRouter } from 'vue-router'
import RequestsView from '../RequestsView.vue'

enableAutoUnmount(afterEach)
const { findRequests } = vi.hoisted(() => ({ findRequests: vi.fn() }))
vi.mock('@/api/requests', () => ({ findRequests }))
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn().mockResolvedValue(true) }) }))

async function open(path: string) {
  const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/:pathMatch(.*)*', component: { template: '<div />' } }] })
  await router.push(path)
  await router.isReady()
  const wrapper = mount(RequestsView, {
    global: {
      plugins: [router],
      stubs: { AppLayout: { template: '<main><slot /></main>' }, OpsErrorDetailModal: true, Icon: true },
    },
  })
  await flushPromises()
  return { wrapper, router }
}

describe('请求详情查询', () => {
  beforeEach(() => findRequests.mockReset().mockResolvedValue({ items: [], has_more: false }))

  it('从链接加载完整 ID，并按管理员入口查询', async () => {
    const { wrapper } = await open('/admin/requests?request_id=client%3Aold-id')
    expect(findRequests).toHaveBeenCalledWith('client:old-id', expect.any(AbortSignal))
    expect(wrapper.text()).toContain('requests.notFound')
  })

  it('管理员查询显示多个同名外部 ID 的结果，并可再次搜索', async () => {
    findRequests.mockResolvedValue({ items: [
      { request_id: 'first', started_at: '2026-10-09T00:00:00Z', state: 'completed', timings: { upstream_first_sse_data_ms: 950 }, aliases: [{ kind: 'upstream', value: 'supplier' }], usage: [], errors: [] },
      { request_id: 'second', started_at: '2026-10-09T00:00:00Z', state: 'failed', legacy: true, usage: [], errors: [] },
    ], has_more: false })
    const { wrapper, router } = await open('/admin/requests?request_id=supplier')
    expect(findRequests).toHaveBeenCalledWith('supplier', expect.any(AbortSignal))
    expect(wrapper.findAll('article')).toHaveLength(2)
    expect(wrapper.text()).toContain('requests.legacy')
    expect(wrapper.text()).toContain('950 ms')
    expect(wrapper.text()).toContain('requests.timingNames.upstream_first_event_ms')
    await wrapper.get('input[aria-label="requests.id"]').setValue('next-id')
    await wrapper.get('input[aria-label="requests.id"]').trigger('keydown.enter')
    await flushPromises()
    expect(router.currentRoute.value.query.request_id).toBe('next-id')
    expect(findRequests).toHaveBeenLastCalledWith('next-id', expect.any(AbortSignal))
  })

  it('点击搜索按钮时提交输入框里的 ID', async () => {
    const { wrapper, router } = await open('/admin/requests')
    expect(findRequests).not.toHaveBeenCalled()
    await wrapper.get('input[aria-label="requests.id"]').setValue('  typed-id  ')
    await wrapper.get('button.btn-primary').trigger('click')
    await flushPromises()
    expect(router.currentRoute.value.query.request_id).toBe('typed-id')
    expect(findRequests).toHaveBeenLastCalledWith('typed-id', expect.any(AbortSignal))
  })

  it('切换 ID 时取消旧查询，迟到结果不会覆盖当前请求', async () => {
    let resolveOld: (value: unknown) => void = () => {}
    findRequests.mockImplementationOnce(() => new Promise(resolve => { resolveOld = resolve }))
    const { wrapper, router } = await open('/admin/requests?request_id=old')
    const signal = findRequests.mock.calls[0][1] as AbortSignal
    await router.replace('/admin/requests?request_id=current')
    await flushPromises()
    expect(signal.aborted).toBe(true)
    resolveOld({ items: [{ request_id: 'old', started_at: '2026-10-09T00:00:00Z', state: 'completed' }], has_more: false })
    await flushPromises()
    expect(wrapper.findAll('article')).toHaveLength(0)
  })
})
