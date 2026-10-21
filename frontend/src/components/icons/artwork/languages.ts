// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/languages.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// languages.ts renders translation glyphs whose strokes redraw according to
// their assigned order. Each path supplies a custom index to the shared
// variant, with matching opacity and stroke delays letting several strokes
// begin together when their indices match.

// PATH_VARIANTS converts each stroke's custom order into a shared opacity and
// path-length delay, allowing equal indices to draw their strokes together.
const PATH_VARIANTS: Variants = {
  normal: { opacity: 1, pathLength: 1, pathOffset: 0 },
  // Each custom step adds one tenth of a second to the stroke entrance.
  animate: (custom: number) => ({
    opacity: [0, 1],
    pathLength: [0, 1],
    pathOffset: [1, 0],
    transition: {
      opacity: { duration: 0.01, delay: custom * 0.1 },
      pathLength: {
        type: 'spring',
        duration: 0.5,
        bounce: 0,
        delay: custom * 0.1,
      },
    },
  }),
}

// icon binds the languages SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'languages',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          custom: 3,
          d: 'm5 8 6 6',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 2,
          d: 'm4 14 6-6 3-3',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 1,
          d: 'M2 5h12',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 0,
          d: 'M7 2h1',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 3,
          d: 'm22 22-5-10-5 10',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          custom: 3,
          d: 'M14 18h6',
          initial: 'normal',
          variants: PATH_VARIANTS,
        },
        () => [],
      ),
    ]),
}

export default icon
