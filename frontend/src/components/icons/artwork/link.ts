// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/link.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// link.ts renders two chain links with a small rotation and changing stroke
// offsets. Both paths share the same animation variant; its resting geometry is
// declared under initial, while the definition and rendered nodes request the
// normal target.

// PATH_VARIANTS applies matching rotation and stroke-offset movement to both
// chain links; its resting declaration uses initial while the renderer requests
// normal.
const PATH_VARIANTS: Variants = {
  initial: { pathLength: 1, pathOffset: 0, rotate: 0 },
  animate: {
    pathLength: [1, 0.97, 1, 0.97, 1],
    pathOffset: [0, 0.05, 0, 0.05, 0],
    rotate: [0, -5, 0],
    transition: {
      rotate: {
        duration: 0.5,
      },
      duration: 1,
      times: [0, 0.2, 0.4, 0.6, 1],
      ease: 'easeInOut',
    },
  },
}

// icon binds the link SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'link',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
