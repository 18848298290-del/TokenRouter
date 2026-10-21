// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/lock-keyhole.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// lock-keyhole.ts renders a padlock whose body rocks while the shackle stroke
// shortens. The outer group combines rotation and scale, and the shackle has a
// separate path-length transition, allowing both effects to start through one
// animation controller.

// icon binds the lock-keyhole SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'lock-keyhole',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        initial: 'normal',
        transition: {
          duration: 1,
          ease: [0.4, 0, 0.2, 1],
        },
        variants: {
          normal: {
            rotate: 0,
            scale: 1,
          },
          animate: {
            rotate: [-3, 1, -2, 0],
            scale: [0.95, 1.05, 0.98, 1],
          },
        },
      },
      () => [
        h(
          'circle',
          {
            cx: '12',
            cy: '16',
            r: '1',
          },
          [],
        ),
        h(
          'rect',
          {
            height: '12',
            rx: '2',
            width: '18',
            x: '3',
            y: '10',
          },
          [],
        ),
        h(
          motion.path,
          {
            animate: controls,
            d: 'M7 10V7a5 5 0 0 1 10 0v3',
            initial: 'normal',
            transition: {
              duration: 0.3,
              ease: [0.4, 0, 0.2, 1],
            },
            variants: {
              normal: {
                pathLength: 1,
              },
              animate: {
                pathLength: 0.7,
              },
            },
          },
          () => [],
        ),
      ],
    ),
}

export default icon
