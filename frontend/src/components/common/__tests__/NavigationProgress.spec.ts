import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { nextTick } from 'vue'
import NavigationProgress from '../NavigationProgress.vue'
import {
  _resetNavigationLoadingInstance,
  useNavigationLoadingState,
} from '@/composables/useNavigationLoading'

/**
 * NavigationProgress.spec.ts checks the rendered navigation indicator against the
 * shared loading controller. Cases cover immediate completion, unrelated animation
 * events, and accessible labeling; resetting the controller before and after each case
 * isolates navigation identifiers and state.
 */

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: () => '加载中' }) }))

beforeEach(_resetNavigationLoadingInstance)
afterEach(_resetNavigationLoadingInstance)

describe('NavigationProgress', () => {
  /**
   * Navigation starts and completes before the next render tick. The completion state
   * still appears and its completion animation event removes the progress indicator.
   */
  it('导航完成后由动画事件隐藏，快速切换也会显示完成反馈', async () => {
    const state = useNavigationLoadingState()
    const wrapper = mount(NavigationProgress)
    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
    const id = state.startNavigation()
    state.endNavigation(id)
    await nextTick()
    expect(wrapper.get('.navigation-progress').classes()).toContain('is-complete')
    await wrapper.get('.navigation-progress-bar').trigger('animationend', {
      animationName: 'navigation-complete-scoped',
    })
    expect(wrapper.find('[role="progressbar"]').exists()).toBe(false)
    wrapper.unmount()
  })

  /**
   * The advance animation finishes while navigation is still loading. Its event leaves
   * the progress indicator visible because only completion animations can finish the
   * displayed feedback.
   */
  it('推进动画结束不会提前隐藏仍在加载的导航', async () => {
    const state = useNavigationLoadingState()
    state.startNavigation()
    const wrapper = mount(NavigationProgress)
    await wrapper.get('.navigation-progress-bar').trigger('animationend', {
      animationName: 'navigation-advance-scoped',
    })
    expect(wrapper.find('[role="progressbar"]').exists()).toBe(true)
    wrapper.unmount()
  })

  /**
   * Navigation is active and exposes its status to assistive technology. The progress
   * bar has a loading label and omits a fabricated current percentage value.
   */
  it('向辅助技术提供加载标签，不报告虚构百分比', () => {
    useNavigationLoadingState().startNavigation()
    const wrapper = mount(NavigationProgress)
    const bar = wrapper.get('[role="progressbar"]')
    expect(bar.attributes('aria-label')).toBe('加载中')
    expect(bar.attributes('aria-valuenow')).toBeUndefined()
    wrapper.unmount()
  })
})
