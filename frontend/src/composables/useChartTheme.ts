import { computed, watch } from 'vue'
import { useTheme } from './useTheme'

// useChartTheme.ts provides shared chart colors, typography sizes, and reactive theme
// observations. Chart configuration consumers use semantic text and grid colors, while
// distribution and token-series palettes keep their assigned colors across light and dark
// themes.

// CHART_PALETTE assigns distribution slice colors by rank so charts share a consistent
// ordered palette.
export const CHART_PALETTE = [
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
  '#06b6d4',
  '#a855f7',
] as const

// CHART_OTHER_COLOR identifies the aggregate Others slice independently of the ranked
// distribution palette colors.
export const CHART_OTHER_COLOR = '#94a3b8'

// CHART_SERIES_COLORS assigns stable colors to token input, output, cache writes, reads,
// and hit-rate series.
export const CHART_SERIES_COLORS = {
  input: '#3b82f6',
  output: '#10b981',
  cacheCreation: '#f59e0b',
  cacheRead: '#06b6d4',
  cacheHitRate: '#8b5cf6',
} as const

// CHART_TICK_FONT_SIZE sets the shared pixel size used for labels along chart axes.
export const CHART_TICK_FONT_SIZE = 10

// CHART_LEGEND_FONT_SIZE sets the shared pixel size used for chart series legend text.
export const CHART_LEGEND_FONT_SIZE = 11

// useChartTheme exposes reactive theme colors and a chart redraw subscription. Consumers
// rebuild chart options or update their chart when notified; the returned watcher stop
// function permits early disposal.
export function useChartTheme() {
  const { isDark } = useTheme()

  const colors = computed(() =>
    isDark.value
      ? { text: '#DEE0E2', muted: '#A1A1AA', grid: '#27272A' }
      : { text: '#3F3F46', muted: '#71717A', grid: '#E4E4E7' },
  )

  // onThemeChange subscribes a callback to changes in the resolved dark theme. It returns
  // Vue's stop function, and a watcher created during setup also follows that component's
  // disposal.
  const onThemeChange = (callback: (dark: boolean) => void) => {
    return watch(isDark, (dark) => callback(dark))
  }

  return { isDark, colors, onThemeChange }
}
