import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent, onMounted } from 'vue'
import { createMemoryHistory, createRouter, useRoute } from 'vue-router'
import { flushPromises, mount, type VueWrapper } from '@vue/test-utils'
import { vContentReveal } from '../contentReveal'
import { mockMotionEnvironment } from '@/__tests__/helpers/motion'

// contentReveal.spec.ts verifies content opacity feedback across route changes, reduced
// motion, and unmount cleanup. A mounted router fixture retains an editable input across
// parameter changes. Animation doubles and motion-preference listeners expose
// cancellation and teardown separately from component identity.

// The suite owns its mounted page and animation records; teardown unmounts the page and
// restores the original browser animation descriptor.
let wrapper: VueWrapper | undefined
const animations: Array<{
  cancel: ReturnType<typeof vi.fn> // Animation cancellation observation spy.
  // finished remains pending throughout each test, keeping completion callbacks from
  // clearing the animation handle before cancellation assertions inspect it.
  finished: Promise<void>
}> = []
// animate records cancellable animation doubles whose completion remains pending during
// each test. This lets assertions inspect replacement and reduced-motion cleanup without
// automatic animation completion hiding the owned handle.
const animate = vi.fn(() => {
  const animation = { cancel: vi.fn(), finished: new Promise<void>(() => {}) }
  animations.push(animation)
  return animation
})
// originalAnimate retains the browser descriptor for teardown restoration; an absent
// descriptor makes teardown delete the installed animation double instead.
const originalAnimate = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'animate')

beforeEach(() => {
  animations.length = 0
  animate.mockClear()
  Object.defineProperty(HTMLElement.prototype, 'animate', { configurable: true, value: animate })
})
afterEach(() => {
  wrapper?.unmount()
  wrapper = undefined
  vi.restoreAllMocks()
  if (originalAnimate) Object.defineProperty(HTMLElement.prototype, 'animate', originalAnimate)
  else delete (HTMLElement.prototype as { animate?: unknown }).animate
})

// mountPage mounts a routed fixture with content feedback and a persistent input. It
// returns the router and mount counter, while suite teardown owns unmounting the shared
// wrapper and restoring browser doubles.
async function mountPage() {
  let mounts = 0
  const Page = defineComponent({
    directives: { contentReveal: vContentReveal },
    // setup exposes the active route and records component mounts for identity assertions.
    setup() {
      onMounted(() => mounts++)
      return { route: useRoute() }
    },
    template:
      '<main v-content-reveal="route.path" style="--motion-fast: 150ms; --motion-ease: ease-out"><input /></main>',
  })
  const router = createRouter({
    history: createMemoryHistory(),
    routes: [{ path: '/page/:id', component: Page }],
  })
  await router.push('/page/1')
  wrapper = mount({ template: '<RouterView />' }, { global: { plugins: [router] } })
  await flushPromises()
  return { router, mounts: () => mounts }
}

describe('页面内容淡入', () => {
  // A routed page changes query, hash, and then its path parameter. Only the path change
  // replays opacity feedback, while component identity and the input draft remain
  // intact.
  it('查询参数及 hash 不触发动画，参数换页保留组件与输入状态', async () => {
    mockMotionEnvironment()
    const page = await mountPage()
    await wrapper!.get('input').setValue('draft')
    expect(animate).toHaveBeenCalledTimes(1)
    await page.router.push('/page/1?sort=cost#details')
    await flushPromises()
    expect(animate).toHaveBeenCalledTimes(1)
    await page.router.push('/page/2')
    await flushPromises()
    expect(animate).toHaveBeenCalledTimes(2)
    expect(animations[0].cancel).toHaveBeenCalledOnce()
    expect(page.mounts()).toBe(1)
    expect((wrapper!.get('input').element as HTMLInputElement).value).toBe('draft')
    expect(animate.mock.calls[0]).toEqual([
      [{ opacity: 0 }, { opacity: 1 }],
      { duration: 150, easing: 'ease-out' },
    ])
  })

  // Rapid route changes replace active animations before reduced motion is enabled.
  // Earlier effects are cancelled, reduced motion suppresses new effects, and unmount
  // removes its preference listener.
  it('快速换页和动态开启减少动画时取消旧动画，卸载移除监听', async () => {
    const environment = mockMotionEnvironment()
    const page = await mountPage()
    await page.router.push('/page/2')
    await page.router.push('/page/3')
    await flushPromises()
    expect(animations[0].cancel).toHaveBeenCalledOnce()
    expect(animations[1].cancel).toHaveBeenCalledOnce()
    environment.reduce(true)
    expect(animations[2].cancel).toHaveBeenCalledOnce()
    await page.router.push('/page/4')
    await flushPromises()
    expect(animate).toHaveBeenCalledTimes(3)
    wrapper!.unmount()
    wrapper = undefined
    expect(environment.listeners.size).toBe(0)
  })

  // Reduced motion is enabled before mounting the routed page. Content remains
  // immediately interactive and present while no decorative opacity animation is created
  // for the first view.
  it('减少动态效果下首屏立即可用', async () => {
    mockMotionEnvironment(true)
    await mountPage()
    expect(animate).not.toHaveBeenCalled()
    expect(wrapper!.find('input').exists()).toBe(true)
  })
})
