import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useNavigationLoading } from '../useNavigationLoading'

// useNavigationLoading.spec.ts checks navigation progress phases using controlled time and
// explicit navigation identifiers. Delayed completion and animation callbacks are
// exercised against newer navigation so the visible indicator follows the current route
// operation and its finishing animation.

describe('导航加载反馈', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  // Navigation begins and completes before its finishing animation callback arrives. The
  // indicator must remain visible through completion and disappear only after
  // finishNavigation receives its identifier.
  it('导航开始立即可见，快速完成后等动画结束再隐藏', () => {
    const state = useNavigationLoading()
    const id = state.startNavigation()
    expect(state.isLoading.value).toBe(true)
    expect(state.isNavigating.value).toBe(true)

    state.endNavigation(id)
    expect(state.isLoading.value).toBe(true)
    expect(state.isNavigating.value).toBe(false)
    state.finishNavigation(id)
    expect(state.isLoading.value).toBe(false)
  })

  // Two navigations start before the first completion callback arrives. Completing the old
  // identifier must leave the newer navigation active until its own completion runs.
  it('旧导航的完成回调不能结束新的导航', () => {
    const state = useNavigationLoading()
    const first = state.startNavigation()
    const second = state.startNavigation()
    state.endNavigation(first)
    expect(state.isNavigating.value).toBe(true)
    state.endNavigation(second)
    expect(state.isNavigating.value).toBe(false)
  })

  // A new navigation starts while the previous completion animation is pending. Old
  // animation callbacks must leave both the newer loading and completion phases visible.
  it('旧完成动画不能隐藏下一次导航', () => {
    const state = useNavigationLoading()
    const first = state.startNavigation()
    state.endNavigation(first)
    const second = state.startNavigation()
    state.finishNavigation(first)
    expect(state.isLoading.value).toBe(true)
    state.endNavigation(second)
    state.finishNavigation(first)
    expect(state.isLoading.value).toBe(true)
    state.finishNavigation(second)
    expect(state.isLoading.value).toBe(false)
  })

  // The current navigation is cancelled through its cancellation alias. It must leave the
  // navigating phase and allow the matching finishing callback to hide the indicator.
  it('取消导航也能正常收尾', () => {
    const state = useNavigationLoading()
    const id = state.startNavigation()
    state.cancelNavigation(id)
    expect(state.isNavigating.value).toBe(false)
    state.finishNavigation(id)
    expect(state.isLoading.value).toBe(false)
  })

  // Fake time advances during a navigation before completion and reset occur. Duration
  // must exist only while navigating, and reset must invalidate callbacks holding the
  // earlier identifier.
  it('只记录正在进行的导航时长，重置使旧回调失效', () => {
    const state = useNavigationLoading()
    expect(state.getNavigationDuration()).toBeNull()
    const id = state.startNavigation()
    vi.advanceTimersByTime(500)
    expect(state.getNavigationDuration()).toBe(500)
    state.endNavigation(id)
    expect(state.getNavigationDuration()).toBeNull()
    state.resetState()
    state.endNavigation(id)
    expect(state.isLoading.value).toBe(false)
  })
})
