// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/moon.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// moon.ts renders a crescent that rocks from side to side. The path stays
// inside one animated group, whose diminishing rotation keyframes end at zero
// while the shared transition supplies the duration and easing for the
// movement.

// SVG_VARIANTS supplies the crescent's alternating angles, while SVG_TRANSITION
// times the complete diminishing rocking sequence from the initial pose back to
// zero.
const SVG_VARIANTS: Variants = {
  normal: {
    rotate: 0,
  },
  animate: {
    rotate: [0, -10, 10, -5, 5, 0],
  },
}

const SVG_TRANSITION: Transition = {
  duration: 1.2,
  ease: 'easeInOut',
}

// icon binds the moon SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'moon',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        transition: SVG_TRANSITION,
        variants: SVG_VARIANTS,
        initial: 'normal',
      },
      () => [
        h(
          'path',
          {
            d: 'M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z',
          },
          [],
        ),
      ],
    ),
}

export default icon
