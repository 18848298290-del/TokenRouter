// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/eye-off.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// eye-off.ts renders a crossed eye with motion confined to the diagonal slash.
// The eye fragments remain plain SVG paths, while the slash combines path
// length, offset, and opacity to draw across the stationary eye outline.

// PATH_VARIANTS draws the diagonal slash using simultaneous opacity, path-
// length, and path-offset changes while the eye outline remains outside the
// motion target.
const PATH_VARIANTS: Variants = {
  normal: { pathLength: 1, opacity: 1, pathOffset: 0 },
  animate: {
    pathLength: [0, 2],
    opacity: [0, 1],
    pathOffset: [0, 2],
    transition: { duration: 0.6 },
  },
}

// icon binds the eye-off SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'eye-off',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M10.733 5.076a10.744 10.744 0 0 1 11.205 6.575 1 1 0 0 1 0 .696 10.747 10.747 0 0 1-1.444 2.49',
        },
        [],
      ),
      h(
        'path',
        {
          d: 'M14.084 14.158a3 3 0 0 1-4.242-4.242',
        },
        [],
      ),
      h(
        'path',
        {
          d: 'M17.479 17.499a10.75 10.75 0 0 1-15.417-5.151 1 1 0 0 1 0-.696 10.75 10.75 0 0 1 4.446-5.143',
        },
        [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'm2 2 20 20',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
