// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/zap.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// zap.ts renders a lightning bolt that draws into view. The opacity transition
// completes sooner than the full stroke-length transition, allowing the bolt's
// path to remain visible while the rest of its outline is being drawn.

// PATH_VARIANTS redraws the lightning outline with a short opacity transition
// and longer stroke-length transition, using the same timing when restoring the
// normal pose.
const PATH_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    transition: {
      duration: 0.6,
      opacity: { duration: 0.1 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    transition: {
      duration: 0.6,
      opacity: { duration: 0.1 },
    },
  },
}

// icon binds the zap SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'zap',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M4 14a1 1 0 0 1-.78-1.63l9.9-10.2a.5.5 0 0 1 .86.46l-1.92 6.02A1 1 0 0 0 13 10h7a1 1 0 0 1 .78 1.63l-9.9 10.2a.5.5 0 0 1-.86-.46l1.92-6.02A1 1 0 0 0 11 14z',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
