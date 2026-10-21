// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/circle-check.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// circle-check.ts renders a fixed circle around an animated check mark. The
// check redraws using opacity and path length, while the outer circle remains a
// plain SVG element so its enclosing outline stays visible throughout playback.

// PATH_VARIANTS redraws the interior check with a brief opacity transition and
// longer stroke transition, leaving the surrounding circle outside these
// targets.
const PATH_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    transition: {
      duration: 0.3,
      opacity: { duration: 0.1 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    transition: {
      duration: 0.4,
      opacity: { duration: 0.1 },
    },
  },
}

// icon binds the circle-check SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'circle-check',
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
        motion.path,
        {
          animate: controls,
          d: 'm9 12 2 2 4-4',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
