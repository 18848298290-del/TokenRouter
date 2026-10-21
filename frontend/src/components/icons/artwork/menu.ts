// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/menu.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// menu.ts renders three menu bars that move into a cross during interaction.
// Custom values identify the upper, middle, and lower bars, letting one variant
// rotate and translate the outside bars while hiding the middle bar.

// LINE_VARIANTS interprets custom values as bar positions, rotating the outside
// bars into a cross and hiding the middle bar during activation.
const LINE_VARIANTS: Variants = {
  normal: {
    rotate: 0,
    y: 0,
    opacity: 1,
  },
  animate: (custom: number) => ({
    rotate: custom === 1 ? 45 : custom === 3 ? -45 : 0,
    y: custom === 1 ? 6 : custom === 3 ? -6 : 0,
    opacity: custom === 2 ? 0 : 1,
    transition: {
      type: 'spring',
      stiffness: 260,
      damping: 20,
    },
  }),
}

// icon binds the menu SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'menu',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.line,
        {
          animate: controls,
          custom: 1,
          initial: 'normal',
          variants: LINE_VARIANTS,
          x1: '4',
          x2: '20',
          y1: '6',
          y2: '6',
        },
        () => [],
      ),
      h(
        motion.line,
        {
          animate: controls,
          custom: 2,
          initial: 'normal',
          variants: LINE_VARIANTS,
          x1: '4',
          x2: '20',
          y1: '12',
          y2: '12',
        },
        () => [],
      ),
      h(
        motion.line,
        {
          animate: controls,
          custom: 3,
          initial: 'normal',
          variants: LINE_VARIANTS,
          x1: '4',
          x2: '20',
          y1: '18',
          y2: '18',
        },
        () => [],
      ),
    ]),
}

export default icon
