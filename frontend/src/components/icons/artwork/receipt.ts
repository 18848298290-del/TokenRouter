// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/receipt.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// receipt.ts renders a receipt outline around a dollar sign with separate
// drawing stages. The curved dollar stroke redraws first, while the vertical
// stroke has a delayed offset and opacity transition; the surrounding receipt
// remains a plain SVG path.

// DOLLAR_MAIN_VARIANTS draws the curved dollar stroke, while
// DOLLAR_SECONDARY_VARIANTS delays the vertical stroke and animates its offset
// for the staged reveal.
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

// icon binds the receipt SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'receipt',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h(
      motion.g,
      {
        initial: 'normal',
      },
      () => [
        h(
          motion.path,
          {
            animate: controls,
            d: 'M12 17V7',
            initial: 'normal',
            variants: DOLLAR_SECONDARY_VARIANTS,
          },
          () => [],
        ),
        h(
          motion.path,
          {
            animate: controls,
            d: 'M16 8h-6a2 2 0 0 0 0 4h4a2 2 0 0 1 0 4H8',
            initial: 'normal',
            variants: DOLLAR_MAIN_VARIANTS,
          },
          () => [],
        ),
        h(
          'path',
          {
            d: 'M4 3a1 1 0 0 1 1-1 1.3 1.3 0 0 1 .7.2l.933.6a1.3 1.3 0 0 0 1.4 0l.934-.6a1.3 1.3 0 0 1 1.4 0l.933.6a1.3 1.3 0 0 0 1.4 0l.933-.6a1.3 1.3 0 0 1 1.4 0l.934.6a1.3 1.3 0 0 0 1.4 0l.933-.6A1.3 1.3 0 0 1 19 2a1 1 0 0 1 1 1v18a1 1 0 0 1-1 1 1.3 1.3 0 0 1-.7-.2l-.933-.6a1.3 1.3 0 0 0-1.4 0l-.934.6a1.3 1.3 0 0 1-1.4 0l-.933-.6a1.3 1.3 0 0 0-1.4 0l-.933.6a1.3 1.3 0 0 1-1.4 0l-.934-.6a1.3 1.3 0 0 0-1.4 0l-.933.6a1.3 1.3 0 0 1-.7.2 1 1 0 0 1-1-1z',
          },
          [],
        ),
      ],
    ),
}

export default icon
