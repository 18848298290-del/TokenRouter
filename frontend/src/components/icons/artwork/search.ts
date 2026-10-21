// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/search.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// search.ts renders a magnifying glass that moves upward and sideways before
// returning. The lens and handle share one animated group, so the icon follows
// its position keyframes as a complete shape while the wrapper retains its
// layout.

// icon binds the search SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'search',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        transition: {
          duration: 1,
          bounce: 0.3,
        },
        variants: {
          normal: { x: 0, y: 0 },
          animate: {
            x: [0, 0, -3, 0],
            y: [0, -4, 0, 0],
          },
        },
        initial: 'normal',
      },
      () => [
        h(
          'circle',
          {
            cx: '11',
            cy: '11',
            r: '8',
          },
          [],
        ),
        h(
          'path',
          {
            d: 'm21 21-4.3-4.3',
          },
          [],
        ),
      ],
    ),
}

export default icon
