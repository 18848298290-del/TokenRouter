// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/trending-up.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// trending-up.ts renders a rising trend line with an arrowhead that draws after
// the line begins. An outer group moves up and right during playback, while the
// two polylines receive separate stroke and opacity transitions from the same
// controller.

// SVG_VARIANTS nudges the symbol up and right; PATH_VARIANTS draws the rising
// line and ARROW_VARIANTS gives the arrowhead its later drawing transition.
const SVG_VARIANTS: Variants = {
  animate: {
    x: 0,
    y: 0,
    translateX: [0, 2, 0],
    translateY: [0, -2, 0],
    transition: {
      duration: 0.5,
    },
  },
}

const PATH_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    transition: {
      duration: 0.4,
      opacity: { duration: 0.1 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    pathOffset: [1, 0],
    transition: {
      duration: 0.4,
      opacity: { duration: 0.1 },
    },
  },
}

const ARROW_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    transition: {
      delay: 0.3,
      duration: 0.3,
      opacity: { duration: 0.1, delay: 0.3 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    pathOffset: [0.5, 0],
    transition: {
      delay: 0.3,
      duration: 0.3,
      opacity: { duration: 0.1, delay: 0.3 },
    },
  },
}

// icon binds the trending-up SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'trending-up',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        animate: controls,
        initial: 'normal',
        variants: SVG_VARIANTS,
      },
      () => [
        h(
          motion.polyline,
          {
            animate: controls,
            initial: 'normal',
            points: '22 7 13.5 15.5 8.5 10.5 2 17',
            variants: PATH_VARIANTS,
          },
          () => [],
        ),
        h(
          motion.polyline,
          {
            animate: controls,
            initial: 'normal',
            points: '16 7 22 7 22 13',
            variants: ARROW_VARIANTS,
          },
          () => [],
        ),
      ],
    ),
}

export default icon
