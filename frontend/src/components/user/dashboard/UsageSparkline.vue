<template>
  <div class="usage-sparkline relative" aria-hidden="true">
    <!-- Recreate the wrapper when its path changes to replay the horizontal reveal. Keep clipping on the wrapper so percentages follow the container rather than SVG coordinates. -->
    <div :key="path" class="sparkline-wipe h-full w-full">
      <svg
        class="h-full w-full overflow-visible"
        viewBox="0 0 100 32"
        preserveAspectRatio="none"
        focusable="false"
      >
        <path
          v-if="path"
          :d="path"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          vector-effect="non-scaling-stroke"
        />
      </svg>
    </div>
    <!-- Render the endpoint as HTML so stretching the SVG keeps the marker circular. -->
    <span
      v-if="endPoint"
      :key="`end-${path}`"
      data-testid="sparkline-end"
      class="sparkline-end absolute h-1.5 w-1.5 rounded-full bg-current"
      :style="{ left: `${endPoint.x}%`, top: `${endPoint.y}%` }"
    ></span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

// UsageSparkline.vue draws the small usage trend within a dashboard metric card. Values keep
// their chronological horizontal positions, and null observations disappear from the path
// while neighboring valid points remain connected, matching the main chart's gap handling.
//
// Vertical positions use the largest available value and a zero baseline with room for the
// line stroke. A single supplied bucket becomes a horizontal segment so its value remains
// visible, while entirely absent data yields no path or endpoint. The endpoint uses
// percentage coordinates to follow the SVG when its container resizes.

// The metric card supplies chronological values, and the sparkline retains their original
// positions when omitting unavailable observations.
const props = defineProps<{
  // values follows bucket order; null omits a point while the line connects its
  // surrounding observations, matching the main trend's gap-spanning display.
  values: Array<number | null>
}>()

// WIDTH, HEIGHT, and PADDING define the SVG coordinate space and reserve vertical room for
// its stroked line.
const WIDTH = 100
const HEIGHT = 32
// PADDING keeps the line stroke inside the vertical viewport at zero and maximum values.
const PADDING = 2

// coordinates retains each bucket index on the horizontal axis while scaling present values
// against their maximum and a zero baseline.
const coordinates = computed(() => {
  const values = props.values
  const max = Math.max(0, ...values.filter((value): value is number => value !== null))
  // toY converts a present metric value into its vertically padded SVG coordinate.
  const toY = (value: number) => {
    const ratio = max > 0 ? value / max : 0
    return HEIGHT - PADDING - ratio * (HEIGHT - PADDING * 2)
  }
  const step = values.length > 1 ? WIDTH / (values.length - 1) : 0
  return values
    .map((value, index) => (value === null ? null : { x: index * step, y: toY(value) }))
    .filter(
      (
        point,
      ): point is {
        x: number // Horizontal SVG coordinate units.
        y: number // Vertical SVG coordinate units.
      } => point !== null,
    )
})

// path joins the available coordinates in bucket order, returning an empty path when every
// observation is absent.
const path = computed(() => {
  const points = coordinates.value
  if (points.length === 0) return ''
  // A single supplied bucket has no neighboring point to form a segment, so
  // extend its level across the viewport to keep the observation visible.
  if (props.values.length === 1) {
    const y = points[0].y.toFixed(2)
    return `M0 ${y} L${WIDTH} ${y}`
  }
  return `M${points.map((point) => `${point.x.toFixed(2)} ${point.y.toFixed(2)}`).join(' L')}`
})

// endPoint converts the last visible observation into container percentages; a single-bucket
// line places its marker at the right edge.
const endPoint = computed(() => {
  const points = coordinates.value
  if (points.length === 0) return null
  const last = props.values.length === 1 ? { x: WIDTH, y: points[0].y } : points[points.length - 1]
  return { x: (last.x / WIDTH) * 100, y: (last.y / HEIGHT) * 100 }
})
</script>

<style scoped>
/* Reveal with a horizontal clip around the non-scaling stroke. Extra vertical space keeps rounded line caps visible while the container grows or shrinks. */
.sparkline-wipe {
  animation: sparkline-wipe var(--dash-sparkline-ms, 700ms) var(--motion-ease) both;
}

.sparkline-end {
  transform: translate(-50%, -50%);
  animation: sparkline-end var(--motion-normal) var(--motion-ease) var(--dash-sparkline-ms, 700ms)
    both;
}

@keyframes sparkline-wipe {
  from {
    clip-path: inset(-20% 100% -20% 0);
  }
  to {
    clip-path: inset(-20% 0 -20% 0);
  }
}

@keyframes sparkline-end {
  from {
    opacity: 0;
    transform: translate(-50%, -50%) scale(0);
  }
  to {
    opacity: 1;
    transform: translate(-50%, -50%) scale(1);
  }
}

@media (prefers-reduced-motion: reduce) {
  .sparkline-wipe,
  .sparkline-end {
    animation: none;
  }
}
</style>
