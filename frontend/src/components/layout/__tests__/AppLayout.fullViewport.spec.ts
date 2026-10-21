import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import AppLayout from '../AppLayout.vue'

// AppLayout.fullViewport.spec.ts verifies viewport classes and document scroll ownership
// for the shared page shell. Stubbed stores and navigation isolate layout modes, while
// saved document styles let each case assert scroll locking and restoration
// independently.

vi.mock('vue-router', () => ({
  useRoute: () => ({ meta: { hideSidebar: true } }),
}))

vi.mock('@/stores', () => ({
  useAppStore: () => ({ sidebarCollapsed: false }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({ user: null }),
}))

vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    setReplayCallback: vi.fn(),
    setTeamGuideCallback: vi.fn(),
  }),
}))

vi.mock('@/composables/onboarding/useOnboardingTour', () => ({
  useOnboardingTour: () => ({
    replayTour: vi.fn(),
    startTeamTour: vi.fn(),
  }),
}))

vi.mock('@/composables/usePageMeta', () => ({
  usePageMeta: () => ({ pageTitle: '', pageDescription: '' }),
}))

describe('AppLayout 全屏视口模式', () => {
  // Saved document styles are restored after each case so scroll-lock assertions do not
  // leak inline overflow changes into later tests.
  let originalHtmlOverflowY = ''
  let originalBodyOverflowY = ''

  beforeEach(() => {
    originalHtmlOverflowY = document.documentElement.style.overflowY
    originalBodyOverflowY = document.body.style.overflowY
    document.documentElement.style.overflowY = ''
    document.body.style.overflowY = ''
  })

  // Restore both saved document styles after each case so a failed assertion cannot
  // leave scrolling settings changed for subsequent tests.
  afterEach(() => {
    document.documentElement.style.overflowY = originalHtmlOverflowY
    document.body.style.overflowY = originalBodyOverflowY
  })

  // mountLayout mounts the shell with stubbed navigation and a content marker.
  //
  // The caller selects full viewport mode and owns unmounting, which releases the
  // document scroll lock before the next mount or assertion.
  function mountLayout(fullViewport: boolean) {
    return mount(AppLayout, {
      props: { fullViewport },
      global: {
        stubs: {
          AppHeader: true,
          AppSidebar: true,
        },
      },
      slots: { default: '<div data-testid="content" />' },
    })
  }

  // The layout mounts with full viewport mode enabled. The shell and content column must
  // receive locked-height classes, and root overflow must be hidden until unmount
  // restores it.
  it('全屏模式固定动态视口并锁定页面滚动', () => {
    const wrapper = mountLayout(true)
    const shell = wrapper.element
    const contentWrapper = shell.querySelector(':scope > div.relative')

    expect(shell.classList).toContain('fixed')
    expect(shell.classList).toContain('inset-0')
    expect(shell.classList).toContain('h-[100dvh]')
    expect(shell.classList).toContain('overflow-hidden')
    expect(contentWrapper?.classList).toContain('h-full')
    expect(contentWrapper?.classList).toContain('min-h-0')
    expect(document.documentElement.style.overflowY).toBe('hidden')
    expect(document.body.style.overflowY).toBe('hidden')

    wrapper.unmount()

    expect(document.documentElement.style.overflowY).toBe('')
    expect(document.body.style.overflowY).toBe('')
  })

  // Document and body begin with different inline overflow values before full viewport
  // mounting. Unmount must restore both saved values independently after the temporary
  // scroll lock.
  it('卸载时恢复进入全屏模式前的 overflow 设置', () => {
    document.documentElement.style.overflowY = 'scroll'
    document.body.style.overflowY = 'auto'
    const wrapper = mountLayout(true)

    expect(document.documentElement.style.overflowY).toBe('hidden')
    expect(document.body.style.overflowY).toBe('hidden')

    wrapper.unmount()

    expect(document.documentElement.style.overflowY).toBe('scroll')
    expect(document.body.style.overflowY).toBe('auto')
  })

  // The layout mounts in its ordinary document-flow mode. It must keep the
  // minimum-height shell and leave document and body scrolling styles unchanged during
  // its lifetime.
  it('普通模式保留文档流布局且不锁定页面滚动', () => {
    const wrapper = mountLayout(false)
    const shell = wrapper.element

    expect(shell.classList).toContain('min-h-screen')
    expect(shell.classList).not.toContain('fixed')
    expect(document.documentElement.style.overflowY).toBe('')
    expect(document.body.style.overflowY).toBe('')

    wrapper.unmount()
  })

  // Full and ordinary layouts render the same main-column flex chain. Full viewport mode
  // must remove padding, while ordinary mode must retain the standard page padding
  // classes.
  it('空间分配契约:主区始终 flex-1 flex-col,全屏模式无内边距,普通模式保留标准内边距链', () => {
    const full = mountLayout(true)
    const fullMain = full.element.querySelector('main.app-main')
    expect(fullMain?.classList).toContain('flex-1')
    expect(fullMain?.classList).toContain('flex-col')
    expect(fullMain?.classList).toContain('p-0')
    expect(fullMain?.classList).not.toContain('px-4')
    full.unmount()

    const normal = mountLayout(false)
    const normalMain = normal.element.querySelector('main.app-main')
    expect(normalMain?.classList).toContain('flex-1')
    expect(normalMain?.classList).toContain('flex-col')
    expect(normalMain?.classList).toContain('px-4')
    expect(normalMain?.classList).not.toContain('p-0')
    normal.unmount()
  })

  // Desktop fitting is enabled without full viewport mode. Responsive classes must lock
  // height at lg, retain padding, and leave root scrolling styles unchanged for
  // document- flow use.
  it('宽屏锁定模式只在 lg 及以上锁定高度，保留内边距且不改写文档滚动', () => {
    const wrapper = mount(AppLayout, {
      props: { fitViewport: true },
      global: { stubs: { AppHeader: true, AppSidebar: true } },
      slots: { default: '<div data-testid="content" />' },
    })
    const shell = wrapper.element
    const contentWrapper = shell.querySelector(':scope > div.relative')
    const main = shell.querySelector('main.app-main')

    // Responsive classes retain natural document height below lg and apply viewport
    // locking only when the desktop breakpoint is active.
    expect(shell.classList).toContain('min-h-screen')
    expect(shell.classList).toContain('lg:h-[100dvh]')
    expect(shell.classList).toContain('lg:overflow-hidden')
    expect(shell.classList).not.toContain('fixed')
    expect(contentWrapper?.classList).toContain('lg:h-full')
    expect(contentWrapper?.classList).toContain('lg:min-h-0')
    expect(main?.classList).toContain('px-4')
    expect(main?.classList).toContain('lg:min-h-0')
    expect(document.documentElement.style.overflowY).toBe('')
    expect(document.body.style.overflowY).toBe('')

    wrapper.unmount()
  })
})
