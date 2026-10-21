// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/trending-down.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// trending-down.ts renders a falling trend line with a delayed arrowhead
// drawing. The enclosing group briefly moves down and right, while separate
// line and arrow variants reveal the stroke segments using their own opacity
// and path-offset timing.

// SVG_VARIANTS nudges the whole symbol down and right; PATH_VARIANTS draws the
// falling line before ARROW_VARIANTS reveals its delayed arrowhead.
const SVG_VARIANTS: Variants = {
  animate: {
    x: 0,
    y: 0,
    translateX: [0, 2, 0],
    translateY: [0, 2, 0],
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

// icon binds the trending-down SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'trending-down',
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
            points: '22 17 13.5 8.5 8.5 13.5 2 7',
            variants: PATH_VARIANTS,
          },
          () => [],
        ),
        h(
          motion.polyline,
          {
            animate: controls,
            initial: 'normal',
            points: '16 17 22 17 22 11',
            variants: ARROW_VARIANTS,
          },
          () => [],
        ),
      ],
    ),
}

export default icon
