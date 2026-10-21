// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/flame.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// flame.ts renders a flame outline that draws into view after a short delay.
// The path combines opacity and stroke length transitions, with the normal
// target restoring the complete outline through the same shared animation
// controller used by Icon.vue.

// PATH_VARIANTS delays the flame drawing briefly and uses a shorter opacity
// transition than the stroke-length transition to reveal the outline.
const PATH_VARIANTS: Variants = {
  normal: {
    pathLength: 1,
    opacity: 1,
    pathOffset: 0,
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    transition: {
      delay: 0.1,
      duration: 0.4,
      opacity: { duration: 0.1, delay: 0.1 },
    },
  },
}

// icon binds the flame SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'flame',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M8.9 14.5A2.5 2.5 0 0 0 11 12c0-1.38-.5-2-1-3-1.072-2.143-.224-4.054 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.153.433-2.294 1-3a2.5 2.5 0 0 0 2.5 2.5z',
          fill: 'none',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
