// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/clock.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// clock.ts renders a stationary clock face with two independently rotating
// hands. Each hand has its own transition and rotation target, while their
// origin settings keep the animations anchored to the corner specified by the
// corresponding line geometry.

// HAND_TRANSITION and HAND_VARIANTS rotate the upright hand through a full
// turn; MINUTE_HAND_TRANSITION and MINUTE_HAND_VARIANTS move the other hand
// through a smaller angle.
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
    rotate: 360,
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
    originY: '100%',
  },
  animate: {
    rotate: 45,
    originX: '0%',
    originY: '100%',
  },
}

// icon binds the clock SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'clock',
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
        motion.line,
        {
          animate: controls,
          initial: 'normal',
          transition: HAND_TRANSITION,
          variants: HAND_VARIANTS,
          x1: '12',
          x2: '12',
          y1: '12',
          y2: '6',
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
          y2: '12',
        },
        () => [],
      ),
    ]),
}

export default icon
