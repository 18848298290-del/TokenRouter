// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chart-column-increasing.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// chart-column-increasing.ts renders increasing chart bars beside a stationary
// pair of axes. The definition first hides and then redraws the bars, using
// each bar's custom index to stagger both phases while retaining the visible
// resting state.

// LINE_VARIANTS defines visible and hidden bar poses; the sequence functions
// below supply staggered transitions using each rendered bar's custom index.
const LINE_VARIANTS: Variants = {
  visible: { pathLength: 1, opacity: 1 },
  hidden: { pathLength: 0, opacity: 0 },
}

// icon binds the chart-column-increasing SVG renderer to the ordered target
// functions for playback by useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'chart-column-increasing',
  normal: 'visible',
  // Each zero-based column index adds one tenth of a second to both erase and
  // redraw phases.
  animate: [
    (i) => ({
      pathLength: 0,
      opacity: 0,
      transition: { delay: i * 0.1, duration: 0.3 },
    }),
    (i) => ({
      pathLength: 1,
      opacity: 1,
      transition: { delay: i * 0.1, duration: 0.3 },
    }),
  ],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          custom: 1,
          d: 'M13 17V9',
          initial: 'visible',
          variants: LINE_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 2,
          d: 'M18 17V5',
          initial: 'visible',
          variants: LINE_VARIANTS,
        },
        () => [],
      ),
      h(
        'path',
        {
          d: 'M3 3v16a2 2 0 0 0 2 2h16',
        },
        [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 0,
          d: 'M8 17v-3',
          initial: 'visible',
          variants: LINE_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
