import { describe, expect, it } from 'vitest'
import { effectScope, nextTick } from 'vue'
import { setTheme } from '@/composables/useTheme'
import { useChartTheme, CHART_PALETTE, CHART_SERIES_COLORS } from '@/composables/useChartTheme'

// useChartTheme.spec.ts checks shared chart palettes and reactive theme integration. The
// cases exercise actual theme changes and watcher disposal, and retain exact color
// assertions so chart consumers can rely on stable semantic series and distribution
// colors.

describe('useChartTheme', () => {
  // A chart theme is created inside a disposable effect scope before light and dark
  // palettes are selected. Its computed colors must update synchronously for both
  // palettes.
  it('colors 跟随主题切换响应式更新(回归:非响应式快照曾导致切主题不重绘)', () => {
    const scope = effectScope()
    const theme = scope.run(() => useChartTheme())!

    setTheme(false)
    expect(theme.colors.value).toEqual({ text: '#3F3F46', muted: '#71717A', grid: '#E4E4E7' })

    setTheme(true)
    expect(theme.colors.value).toEqual({ text: '#DEE0E2', muted: '#A1A1AA', grid: '#27272A' })

    scope.stop()
    setTheme(false)
  })

  // A subscribed chart observes two theme changes before its watcher is stopped. Later
  // changes must leave the recorded notifications unchanged after explicit watcher
  // disposal.
  it('onThemeChange 回调在切换时触发,返回的停止函数可断开', async () => {
    const scope = effectScope()
    const theme = scope.run(() => useChartTheme())!
    const seen: boolean[] = []
    const stop = theme.onThemeChange((dark) => seen.push(dark))

    setTheme(true)
    // Theme notifications use Vue's queued watcher execution, so advance the reactive
    // flush before checking which callbacks were delivered.
    await nextTick()
    setTheme(false)
    await nextTick()
    expect(seen).toEqual([true, false])

    stop()
    setTheme(true)
    await nextTick()
    expect(seen).toEqual([true, false])

    scope.stop()
    setTheme(false)
  })

  // Distribution charts share a twelve-entry palette whose first ten colors have a defined
  // order. The assertions protect that prefix and total palette length together.
  it('调色板 12 色唯一来源,前缀 10 色与原 GroupDistributionChart 拷贝逐项一致', () => {
    expect(CHART_PALETTE.slice(0, 10)).toEqual([
      '#3b82f6',
      '#10b981',
      '#f59e0b',
      '#ef4444',
      '#8b5cf6',
      '#ec4899',
      '#00D2FF',
      '#f97316',
      '#6366f1',
      '#84cc16',
    ])
    expect(CHART_PALETTE).toHaveLength(12)
  })

  // Token charts distinguish input, output, cache creation, cache reads, and cache hit
  // rate through assigned colors. Each semantic series must retain its expected shared
  // value.
  it('序列色锁定 token 趋势图五色语义', () => {
    expect(CHART_SERIES_COLORS).toEqual({
      input: '#3b82f6',
      output: '#10b981',
      cacheCreation: '#f59e0b',
      cacheRead: '#06b6d4',
      cacheHitRate: '#8b5cf6',
    })
  })
})
