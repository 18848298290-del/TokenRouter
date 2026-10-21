// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/credit-card.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// credit-card.ts renders a payment card that slides horizontally and settles at
// its resting position. The card outline and stripe live in one motion group,
// so their relative placement stays fixed during the overshoot and return.

// CARD_VARIANTS gives the card group timed horizontal overshoot keyframes for
// playback and a spring transition when returning to the resting target.
const CARD_VARIANTS: Variants = {
  normal: {
    x: 0,
    transition: {
      type: 'spring',
      stiffness: 280,
      damping: 18,
    },
  },
  animate: {
    x: [0, -4, 1.5, 0],
    transition: {
      duration: 0.7,
      times: [0, 0.4, 0.75, 1],
      ease: 'easeInOut',
    },
  },
}

// icon binds the credit-card SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'credit-card',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.g,
        {
          animate: controls,
          initial: 'normal',
          variants: CARD_VARIANTS,
        },
        () => [
          h(
            'rect',
            {
              height: '14',
              rx: '2',
              width: '20',
              x: '2',
              y: '5',
            },
            [],
          ),
          h(
            'line',
            {
              x1: '2',
              x2: '22',
              y1: '10',
              y2: '10',
            },
            [],
          ),
        ],
      ),
    ]),
}

export default icon
