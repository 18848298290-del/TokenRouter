// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/history.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// history.ts renders a history arrow around two clock hands that rotate
// backward. The outer arrow uses a spring while the hands have separate timed
// transitions and origins, allowing all three parts to respond to the same
// controller target.

// ARROW_TRANSITION and ARROW_VARIANTS turn the outer arrow; the HAND and
// MINUTE_HAND pairs supply separate backward rotations and origins for the
// clock hands.
const ARROW_TRANSITION: Transition = {
  type: 'spring',
  stiffness: 250,
  damping: 25,
}

const ARROW_VARIANTS: Variants = {
  normal: {
    rotate: '0deg',
  },
  animate: {
    rotate: '-50deg',
  },
}

const HAND_TRANSITION: Transition = {
  duration: 0.6,
  ease: [0.4, 0, 0.2, 1],
}

const HAND_VARIANTS: Variants = {
  normal: {
    rotate: 0,
    originX: '0%',
    originY: '100%',
  },
  animate: {
    rotate: -360,
    originX: '0%',
    originY: '100%',
  },
}

const MINUTE_HAND_TRANSITION: Transition = {
  duration: 0.5,
  ease: 'easeInOut',
}

const MINUTE_HAND_VARIANTS: Variants = {
  normal: {
    rotate: 0,
    originX: '0%',
    originY: '0%',
  },
  animate: {
    rotate: -45,
    originX: '0%',
    originY: '0%',
  },
}

// icon binds the history SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'history',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.g,
        {
          animate: controls,
          transition: ARROW_TRANSITION,
          variants: ARROW_VARIANTS,
          initial: 'normal',
        },
        () => [
          h(
            'path',
            {
              d: 'M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8',
            },
            [],
          ),
          h(
            'path',
            {
              d: 'M3 3v5h5',
            },
            [],
          ),
        ],
      ),
      h(
        motion.line,
        {
          animate: controls,
          initial: 'normal',
          transition: HAND_TRANSITION,
          variants: HAND_VARIANTS,
          x1: '12',
          x2: '12',
          y1: '12',
          y2: '7',
        },
        () => [],
      ),
      h(
        motion.line,
        {
          animate: controls,
          initial: 'normal',
          transition: MINUTE_HAND_TRANSITION,
          variants: MINUTE_HAND_VARIANTS,
          x1: '12',
          x2: '16',
          y1: '12',
          y2: '14',
        },
        () => [],
      ),
    ]),
}

export default icon
