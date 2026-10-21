// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/x.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// x.ts renders a cross whose diagonal strokes draw with a short stagger. The
// two paths share the opacity and path-length variants, while the second path
// supplies a delay so its drawing starts after the first diagonal.

// PATH_VARIANTS supplies the drawing and opacity targets shared by both
// diagonals, while the second rendered path adds its own delay for staggered
// playback.
const PATH_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
  },
}

// icon binds the x SVG renderer to ['animate'] for playback by useIconAnimation
// through the shared controls.
const icon: IconDefinition = {
  name: 'x',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M18 6 6 18',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'm6 6 12 12',
          transition: { delay: 0.2 },
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
