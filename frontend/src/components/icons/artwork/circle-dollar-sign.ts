// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/circle-dollar-sign.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// circle-dollar-sign.ts renders a fixed circular outline around a dollar sign
// that redraws in stages. The curved stroke begins first, and the delayed
// vertical stroke uses its own offset and opacity timing within the same
// animation target.

// DOLLAR_MAIN_VARIANTS draws the curved stroke first; DOLLAR_SECONDARY_VARIANTS
// delays the vertical stroke and animates its path offset into the finished
// symbol.
const DOLLAR_MAIN_VARIANTS: Variants = {
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
    transition: {
      duration: 0.6,
      opacity: { duration: 0.1 },
    },
  },
}

const DOLLAR_SECONDARY_VARIANTS: Variants = {
  normal: {
    opacity: 1,
    pathLength: 1,
    pathOffset: 0,
    transition: {
      delay: 0.3,
      duration: 0.3,
      opacity: { duration: 0.1, delay: 0.3 },
    },
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
    pathOffset: [1, 0],
    transition: {
      delay: 0.5,
      duration: 0.4,
      opacity: { duration: 0.1, delay: 0.5 },
    },
  },
}

// icon binds the circle-dollar-sign SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'circle-dollar-sign',
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
          d: 'M16 8h-6a2 2 0 1 0 0 4h4a2 2 0 1 1 0 4H8',
          initial: 'normal',
          variants: DOLLAR_MAIN_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M12 18V6',
          initial: 'normal',
          variants: DOLLAR_SECONDARY_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
