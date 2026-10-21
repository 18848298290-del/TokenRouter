// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/plus.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// plus.ts renders a plus sign whose two strokes turn together. The horizontal
// and vertical paths share an animated group that rotates through half a turn,
// and the normal target brings the group back through the configured spring.

// icon binds the plus SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'plus',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        transition: { type: 'spring', stiffness: 100, damping: 15 },
        variants: {
          normal: {
            rotate: 0,
          },
          animate: {
            rotate: 180,
          },
        },
        initial: 'normal',
      },
      () => [
        h(
          'path',
          {
            d: 'M5 12h14',
          },
          [],
        ),
        h(
          'path',
          {
            d: 'M12 5v14',
          },
          [],
        ),
      ],
    ),
}

export default icon
