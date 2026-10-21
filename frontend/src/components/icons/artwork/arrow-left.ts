// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/arrow-left.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// arrow-left.ts renders a left arrow with a retracting horizontal shaft and
// moving arrowhead. The head moves right as the shaft shortens, and matching
// durations give both strokes the same outward and return rhythm during
// interaction.

// PATH_VARIANTS translates the left arrowhead and SECOND_PATH_VARIANTS retracts
// its horizontal shaft over matching transition durations.
const PATH_VARIANTS: Variants = {
  normal: { d: 'm12 19-7-7 7-7', translateX: 0 },
  animate: {
    d: 'm12 19-7-7 7-7',
    translateX: [0, 3, 0],
    transition: {
      duration: 0.4,
    },
  },
}

const SECOND_PATH_VARIANTS: Variants = {
  normal: { d: 'M19 12H5' },
  animate: {
    d: ['M19 12H5', 'M19 12H10', 'M19 12H5'],
    transition: {
      duration: 0.4,
    },
  },
}

// icon binds the arrow-left SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'arrow-left',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm12 19-7-7 7-7',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M19 12H5',
          variants: SECOND_PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
