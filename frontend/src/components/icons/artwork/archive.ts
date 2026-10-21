// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/archive.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// archive.ts renders an archive box whose lid lifts while the body and handle
// move downward. The three elements use separate target shapes so the handle
// stays inside the moving box during the opening effect.

// RECT_VARIANTS, PATH_VARIANTS, and SECONDARY_PATH_VARIANTS move the lid, box
// body, and handle to the corresponding open and resting shapes.
const RECT_VARIANTS: Variants = {
  normal: {
    translateY: 0,
    transition: {
      duration: 0.2,
      type: 'spring',
      stiffness: 200,
      damping: 25,
    },
  },
  animate: {
    translateY: -1.5,
    transition: {
      duration: 0.2,
      type: 'spring',
      stiffness: 200,
      damping: 25,
    },
  },
}

const PATH_VARIANTS: Variants = {
  normal: { d: 'M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8' },
  animate: { d: 'M4 11v9a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V11' },
}

const SECONDARY_PATH_VARIANTS: Variants = {
  normal: { d: 'M10 12h4' },
  animate: { d: 'M10 15h4' },
}

// icon binds the archive SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'archive',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.rect,
        {
          animate: controls,
          height: '5',
          initial: 'normal',
          rx: '1',
          variants: RECT_VARIANTS,
          width: '20',
          x: '2',
          y: '3',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M10 12h4',
          variants: SECONDARY_PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
