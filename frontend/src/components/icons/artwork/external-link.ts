// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/external-link.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// external-link.ts renders an outward arrow moving above a stationary open
// square. The shaft and arrowhead share a group whose scale and diagonal
// offsets return through keyframes, keeping the exit gesture separate from the
// surrounding square outline.

// ARROW_VARIANTS combines a slight contraction with outward diagonal motion,
// using the upper-right origin and return keyframes for the complete arrow
// group.
const ARROW_VARIANTS: Variants = {
  normal: {
    scale: 1,
    translateX: 0,
    translateY: 0,
  },
  animate: {
    scale: [1, 0.92, 1],
    translateX: [0, 2, 0],
    translateY: [0, -2, 0],
    originX: 1,
    originY: 0,
    transition: {
      duration: 0.5,
      ease: 'easeInOut',
    },
  },
}

// icon binds the external-link SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'external-link',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6',
        },
        [],
      ),
      h(
        motion.g,
        {
          animate: controls,
          variants: ARROW_VARIANTS,
          initial: 'normal',
        },
        () => [
          h(
            'path',
            {
              d: 'M15 3h6v6',
            },
            [],
          ),
          h(
            'path',
            {
              d: 'M10 14 21 3',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
