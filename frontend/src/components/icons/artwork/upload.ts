// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/upload.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// upload.ts renders an upload arrow moving away from a stationary tray. The
// arrowhead and shaft share a spring-driven group with an upward target, and
// the normal variant restores the connected arrow to its starting position.

// ARROW_VARIANTS moves the grouped arrow upward with a spring and supplies its
// zero-displacement resting target for the lifecycle controller's reset.
const ARROW_VARIANTS: Variants = {
  normal: { y: 0 },
  animate: {
    y: -2,
    transition: {
      type: 'spring',
      stiffness: 200,
      damping: 10,
      mass: 1,
    },
  },
}

// icon binds the upload SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'upload',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4',
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
            'polyline',
            {
              points: '17 8 12 3 7 8',
            },
            [],
          ),
          h(
            'line',
            {
              x1: '12',
              x2: '12',
              y1: '3',
              y2: '15',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
