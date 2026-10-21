// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/user-plus.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// user-plus.ts renders a stationary user silhouette with an animated addition
// mark. The plus strokes share a group centered on their intersection, and
// delayed scale, rotation, and opacity keyframes reveal the mark together after
// playback begins.

// PLUS_VARIANTS reveals the addition mark after a delay, combining rotation and
// scale overshoot with opacity before settling at the full visible symbol.
const PLUS_VARIANTS: Variants = {
  normal: {
    scale: 1,
    rotate: 0,
    opacity: 1,
    transition: {
      duration: 0.3,
      ease: 'easeOut',
    },
  },
  animate: {
    scale: [0, 1.15, 1],
    rotate: [-90, 0, 0],
    opacity: [0, 1, 1],
    transition: {
      delay: 0.25,
      duration: 0.45,
      ease: 'easeOut',
      times: [0, 0.7, 1],
    },
  },
}

// icon binds the user-plus SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'user-plus',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2',
        },
        [],
      ),
      h(
        'circle',
        {
          cx: '9',
          cy: '7',
          r: '4',
        },
        [],
      ),
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          style: { transformOrigin: '19px 11px' },
          variants: PLUS_VARIANTS,
        },
        () => [
          h(
            'line',
            {
              x1: '19',
              x2: '19',
              y1: '8',
              y2: '14',
            },
            [],
          ),
          h(
            'line',
            {
              x1: '22',
              x2: '16',
              y1: '11',
              y2: '11',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
