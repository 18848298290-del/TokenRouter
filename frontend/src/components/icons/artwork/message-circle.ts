// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/message-circle.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// message-circle.ts renders a round speech bubble that grows and rocks during
// interaction. A single group keeps the tail attached to the outline, with a
// timed rotation and spring scale both returning through the normal variant
// when requested.

// ICON_VARIANTS couples the round bubble's short rocking motion with a spring
// scale increase and supplies the resting angle and scale for reset.
const ICON_VARIANTS: Variants = {
  normal: {
    scale: 1,
    rotate: 0,
  },
  animate: {
    scale: 1.05,
    rotate: [0, -7, 7, 0],
    transition: {
      rotate: {
        duration: 0.5,
        ease: 'easeInOut',
      },
      scale: {
        type: 'spring',
        stiffness: 400,
        damping: 10,
      },
    },
  },
}

// icon binds the message-circle SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'message-circle',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        variants: ICON_VARIANTS,
        initial: 'normal',
      },
      () => [
        h(
          'path',
          {
            d: 'M7.9 20A9 9 0 1 0 4 16.1L2 22Z',
          },
          [],
        ),
      ],
    ),
}

export default icon
