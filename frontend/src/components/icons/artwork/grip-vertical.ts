// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/grip-vertical.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// grip-vertical.ts renders six grip dots with pulses staggered by their grid
// positions. The column-major coordinate array supplies indices for row and
// column delays, and the playback sequence returns to the normal target after
// completing the pulse.

// CIRCLES stores dot centers in column-major order, ROWS defines that indexing
// scheme, and VARIANTS derives the pulse delay from each dot's position.
const CIRCLES = [
  { cx: 9, cy: 5 },
  { cx: 9, cy: 12 },
  { cx: 9, cy: 19 },
  { cx: 15, cy: 5 },
  { cx: 15, cy: 12 },
  { cx: 15, cy: 19 },
]

const ROWS = 3

const VARIANTS: Variants = {
  normal: {
    opacity: 1,
    scale: 1,
    transition: { duration: 0.25, ease: 'easeOut' },
  },
  animate: (data: { index: number /* column-major dot index */ }) => {
    // Column-major indices give each column three rows; convert the index before
    // assigning stagger delays in seconds.
    const row = data.index % ROWS
    const col = Math.floor(data.index / ROWS)
    const delay = row * 0.15 + col * (ROWS * 0.15 - 0.2)
    return {
      opacity: [1, 0.4, 1],
      scale: [1, 0.85, 1],
      transition: { delay, duration: 1, ease: 'easeInOut' },
    }
  },
}

// icon binds the grip-vertical SVG renderer to ['animate', 'normal'] for
// playback by useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'grip-vertical',
  normal: 'normal',
  animate: ['animate', 'normal'],
  render: (controls) =>
    h('g', {}, [
      CIRCLES.map((circle, index) =>
        h(
          motion.circle,
          {
            animate: controls,
            custom: { index },
            cx: circle.cx,
            cy: circle.cy,
            initial: 'normal',
            key: `${circle.cx}-${circle.cy}`,
            r: '1',
            variants: VARIANTS,
          },
          () => [],
        ),
      ),
    ]),
}

export default icon
