// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/box.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// box.ts renders a cube whose outline and internal edges draw into view. All
// three paths share opacity and path length variants, so the common controller
// redraws the visible faces together and restores them to the full outline.

// PATH_VARIANTS redraws all cube edges with common stroke-length and opacity
// targets, including the transition used when restoring the visible outline.
const PATH_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    transition: {
      duration: 0.3,
      opacity: { duration: 0.1 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    transition: {
      duration: 0.4,
      opacity: { duration: 0.1 },
    },
  },
}

// icon binds the box SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'box',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M21 8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16Z',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'm3.3 7 8.7 5 8.7-5',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M12 22V12',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
