// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/key.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// key.ts renders a key that turns around an origin near its round head. The
// animated target ends at a tilted angle, and the normal variant supplies the
// spring transition that returns the grouped key paths after interaction ends.

// icon binds the key SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'key',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        initial: 'normal',
        style: { originX: 0.3, originY: 0.7 },
        variants: {
          normal: {
            rotate: 0,
            transition: {
              type: 'spring',
              stiffness: 120,
              damping: 14,
              duration: 0.8,
            },
          },
          animate: {
            rotate: [-3, -33, -25, -28],
            transition: {
              duration: 0.6,
              times: [0, 0.6, 0.8, 1],
              ease: 'easeInOut',
            },
          },
        },
      },
      () => [
        h(
          'path',
          {
            d: 'm15.5 7.5 2.3 2.3a1 1 0 0 0 1.4 0l2.1-2.1a1 1 0 0 0 0-1.4L19 4',
          },
          [],
        ),
        h(
          'path',
          {
            d: 'm21 2-9.6 9.6',
          },
          [],
        ),
        h(
          'circle',
          {
            cx: '7.5',
            cy: '15.5',
            r: '5.5',
          },
          [],
        ),
      ],
    ),
}

export default icon
