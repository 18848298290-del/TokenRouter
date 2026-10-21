import type { CSSProperties } from 'vue'

// dashboardMotion.ts defines the timing values used by dashboard counters and decorative
// animations. Scripts consume millisecond constants directly, and the page publishes
// corresponding CSS variables for section, sparkline, heatmap, and model-share styles to
// consume.

// COUNT_UP_MS sets the millisecond duration used by dashboard metric counters when their
// numeric targets change.

export const COUNT_UP_MS = 900

// CHART_REVEAL_MS sets the millisecond reveal duration when the main trend receives data or
// changes metric.
export const CHART_REVEAL_MS = 800

// SPARKLINE_DRAW_MS supplies the millisecond clip animation duration for small trends within
// dashboard metric cards.
export const SPARKLINE_DRAW_MS = 700

// DASH_RISE_MS and DASH_RISE_STEP_MS coordinate the section animation duration and stagger
// between consecutive dashboard sections.
export const DASH_RISE_MS = 420
export const DASH_RISE_STEP_MS = 60

// HEATMAP_CELL_ENTER_MS controls each cell animation while HEATMAP_WAVE_STEP_MS and
// HEATMAP_WAVE_MAX_MS bound the column stagger.
export const HEATMAP_CELL_ENTER_MS = 320
export const HEATMAP_WAVE_STEP_MS = 8
export const HEATMAP_WAVE_MAX_MS = 480

// TOP_MODEL_BAR_MS and TOP_MODEL_BAR_STEP_MS coordinate the duration and stagger of model
// share bar animations.
export const TOP_MODEL_BAR_MS = 600
export const TOP_MODEL_BAR_STEP_MS = 60

// dashboardMotionVars publishes CSS durations on the dashboard root so component styles
// share the timing values used by scripts.
export const dashboardMotionVars: CSSProperties = {
  '--dash-rise-ms': `${DASH_RISE_MS}ms`,
  '--dash-rise-step-ms': `${DASH_RISE_STEP_MS}ms`,
  '--dash-sparkline-ms': `${SPARKLINE_DRAW_MS}ms`,
  '--dash-heatmap-enter-ms': `${HEATMAP_CELL_ENTER_MS}ms`,
  '--dash-top-model-bar-ms': `${TOP_MODEL_BAR_MS}ms`,
} as CSSProperties
