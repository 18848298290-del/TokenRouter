// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/users.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// users.ts renders a pair of people with motion on the rear silhouette. The
// front person's head and shoulders remain plain SVG shapes, while both rear
// paths share the same delayed horizontal spring movement into their resting
// positions.

// PATH_VARIANTS moves both rear-person paths from a left offset into their
// resting positions, using the same delayed spring for the head and shoulders.
const PATH_VARIANTS: Variants = {
  normal: {
    translateX: 0,
    transition: {
      type: 'spring',
      stiffness: 200,
      damping: 13,
    },
  },
  animate: {
    translateX: [-6, 0],
    transition: {
      delay: 0.1,
      type: 'spring',
      stiffness: 200,
      damping: 13,
    },
  },
}

// icon binds the users SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'users',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M16 21v-2a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v2',
        },
        [],
      ),
      h(
        'circle',
        {
          cx: '9',
          cy: '7',
          r: '4',
        },
        [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M22 21v-2a4 4 0 0 0-3-3.87',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M16 3.13a4 4 0 0 1 0 7.75',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
