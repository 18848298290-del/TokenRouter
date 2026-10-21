// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/check.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// check.ts renders a check mark that grows while its stroke draws into view.
// Opacity, scale, and path length share the same target, with a shorter opacity
// transition keeping the drawing visible during the rest of the effect.

// PATH_VARIANTS grows the check while drawing its stroke, with independent
// opacity timing making the visible mark appear before the drawing completes.
const PATH_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    scale: 1,
    transition: {
      duration: 0.3,
      opacity: { duration: 0.1 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    scale: [0.5, 1],
    transition: {
      duration: 0.4,
      opacity: { duration: 0.1 },
    },
  },
}

// icon binds the check SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'check',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M4 12 9 17L20 6',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
