// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/bell.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// bell.ts renders the bell outline and clapper inside one rotating group. The
// group rocks in both directions and ends at its resting angle, keeping the two
// static paths together while Icon.vue controls the outer SVG.

// SVG_VARIANTS gives the bell group a resting angle and alternating rotations
// that finish at the initial angle.
const SVG_VARIANTS: Variants = {
  normal: { rotate: 0 },
  animate: { rotate: [0, -10, 10, -10, 0] },
}

// icon binds the bell SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'bell',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        transition: {
          duration: 0.5,
          ease: 'easeInOut',
        },
        variants: SVG_VARIANTS,
        initial: 'normal',
      },
      () => [
        h(
          'path',
          {
            d: 'M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9',
          },
          [],
        ),
        h(
          'path',
          {
            d: 'M10.3 21a1.94 1.94 0 0 0 3.4 0',
          },
          [],
        ),
      ],
    ),
}

export default icon
