// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chart-no-axes-column-increasing.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// chart-no-axes-column-increasing.ts renders three increasing bars with a
// staggered erase and redraw sequence. Each bar supplies its column index to
// both target functions, and the visible resting variant keeps all columns
// drawn before the sequence starts.

// LINE_VARIANTS supplies fully drawn and hidden bar poses, while the playback
// target functions compute per-column delays from the renderer's indices.
const LINE_VARIANTS: Variants = {
  visible: { pathLength: 1, opacity: 1 },
  hidden: { pathLength: 0, opacity: 0 },
}

// icon binds the chart-no-axes-column-increasing SVG renderer to the ordered
// target functions for playback by useIconAnimation through the shared
// controls.
const icon: IconDefinition = {
  name: 'chart-no-axes-column-increasing',
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
          custom: 0,
          d: 'M6 20v-4',
          initial: 'visible',
          variants: LINE_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 1,
          d: 'M12 20v-10',
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
          d: 'M18 20v-16',
          initial: 'visible',
          variants: LINE_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
