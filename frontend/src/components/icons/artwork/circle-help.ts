// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/circle-help.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// circle-help.ts renders a question mark that rocks inside a stationary circle.
// The question curve and its dot share an inner motion group, so their relative
// positions remain fixed while the surrounding circle remains at rest.

// VARIANTS rocks the inner question-mark group between opposing angles and ends
// at the resting angle while the enclosing circle remains stationary.
const VARIANTS: Variants = {
  normal: { rotate: 0 },
  animate: { rotate: [0, -10, 10, -10, 0] },
}

// icon binds the circle-help SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'circle-help',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'circle',
        {
          cx: '12',
          cy: '12',
          r: '10',
        },
        [],
      ),
      h(
        motion.g,
        {
          animate: controls,
          transition: {
            duration: 0.5,
            ease: 'easeInOut',
          },
          variants: VARIANTS,
          initial: 'normal',
        },
        () => [
          h(
            'path',
            {
              d: 'M9.09 9a3 3 0 0 1 5.83 1c0 2-3 3-3 3',
            },
            [],
          ),
          h(
            'path',
            {
              d: 'M12 17h.01',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
