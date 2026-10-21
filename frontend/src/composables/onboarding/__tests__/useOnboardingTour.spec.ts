import { defineComponent, nextTick } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useOnboardingTour } from '../useOnboardingTour'

// useOnboardingTour.spec.ts checks navigation between team onboarding targets
// through a mocked router and driver. Visible target fixtures isolate route
// ordering and highlight refresh, while the test drives forward, backward, and
// close callbacks from the registered driver configuration.

// Driver, router, and onboarding store fixtures record navigation and lifecycle
// operations while the test controls active step and route state.
const { driverFactory, routerPush, currentRoute, onboardingStore } = vi.hoisted(() => ({
  driverFactory: vi.fn(),
  routerPush: vi.fn(),
  currentRoute: { value: { fullPath: '/team' } },
  onboardingStore: {
    getDriverInstance: vi.fn(() => null),
    setDriverInstance: vi.fn(),
    isDriverActive: vi.fn(() => false),
    setControlMethods: vi.fn(),
    clearControlMethods: vi.fn(),
  },
}))

vi.mock('driver.js', () => ({ driver: driverFactory }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: { id: 7, role: 'user' } }),
}))
vi.mock('@/stores/onboarding', () => ({ useOnboardingStore: () => onboardingStore }))
vi.mock('vue-router', () => ({
  useRouter: () => ({
    currentRoute,
    resolve: (route: {
      path: string // Destination application route path.
      query?: Record<string, string> // Destination route query values.
    }) => ({
      fullPath: route.query?.scope ? `${route.path}?scope=${route.query.scope}` : route.path,
    }),
    push: routerPush,
  }),
}))

describe('useOnboardingTour team flow', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    currentRoute.value = { fullPath: '/team' }
    routerPush.mockImplementation(
      async (route: {
        path: string // Destination application route path.
        query?: Record<string, string> // Destination route query values.
      }) => {
        currentRoute.value = {
          fullPath: route.query?.scope ? `${route.path}?scope=${route.query.scope}` : route.path,
        }
        await nextTick()
      },
    )
  })

  // A team owner advances through steps that cross team and key routes, then
  // navigates backward. Router calls must complete before the driver highlights
  // the destination step.
  it('在团队、密钥和使用记录步骤之间按目标路由切换', async () => {
    let activeIndex = 0
    let active = true
    let config: any
    const driverInstance = {
      drive: vi.fn((index = 0) => {
        activeIndex = index
      }),
      moveTo: vi.fn((index: number) => {
        activeIndex = index
      }),
      moveNext: vi.fn(() => {
        activeIndex += 1
      }),
      movePrevious: vi.fn(() => {
        activeIndex -= 1
      }),
      getActiveIndex: vi.fn(() => activeIndex),
      getActiveElement: vi.fn(() => null),
      refresh: vi.fn(),
      isActive: vi.fn(() => active),
      destroy: vi.fn(() => {
        active = false
        config?.onDestroyed?.()
      }),
    }
    driverFactory.mockImplementation((nextConfig) => {
      config = nextConfig
      return driverInstance
    })

    // Provide a visible target for every selector so the test observes router
    // sequencing rather than waiting for unrelated page rendering.
    const target = document.createElement('div')
    vi.spyOn(target, 'getBoundingClientRect').mockReturnValue({ height: 40 } as DOMRect)
    const querySelector = vi.spyOn(document, 'querySelector').mockReturnValue(target)

    const Harness = defineComponent({
      // setup exposes tour methods from a mounted Vue component fixture.
      setup() {
        return useOnboardingTour({ autoStart: false })
      },
      template: '<div />',
    })
    const wrapper = mount(Harness)
    await flushPromises()

    const startTeamTour = (wrapper.vm as any).startTeamTour as (options: {
      isOwner: boolean // Current team ownership flag.
      hasTeam: boolean // Current team membership flag.
    }) => void
    startTeamTour({ isOwner: true, hasTeam: true })
    await flushPromises()

    // The driver keeps progress visible while the highlight callback reveals and
    // refreshes the target before further route navigation.
    expect(config.showProgress).toBe(true)

    const reveal = vi.fn()
    target.addEventListener('onboarding-reveal', reveal)
    await config.onHighlightStarted(target, { element: '[data-tour="group-form-multiplier"]' })
    expect(reveal).toHaveBeenCalledOnce()
    expect(driverInstance.refresh).toHaveBeenCalledOnce()

    for (let index = 0; index < 5; index += 1) {
      await config.onNextClick(null, config.steps[index], {
        config,
        state: { activeIndex: index },
      })
    }

    expect(routerPush).toHaveBeenCalledWith({ path: '/keys', query: { scope: 'team' } })
    expect(driverInstance.moveTo).toHaveBeenLastCalledWith(5)

    await config.onPrevClick(null, config.steps[5], { state: { activeIndex: 5 } })
    expect(routerPush).toHaveBeenLastCalledWith({ path: '/team' })
    expect(driverInstance.moveTo).toHaveBeenLastCalledWith(4)

    config.onCloseClick()
    wrapper.unmount()
    querySelector.mockRestore()
  })
})
