// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/arrow-up.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// arrow-up.ts renders an upward arrow with a shortening shaft and a head that
// moves downward. Both paths return to their full arrow positions through
// matching keyframes, while the supplied controls start and stop their motion
// together.

// PATH_VARIANTS moves the upward arrowhead down while SECOND_PATH_VARIANTS
// shortens the shaft, with matching durations bringing both back together.
const PATH_VARIANTS: Variants = {
  normal: { d: 'm5 12 7-7 7 7', translateY: 0 },
  animate: {
    d: 'm5 12 7-7 7 7',
    translateY: [0, 3, 0],
    transition: {
      duration: 0.4,
    },
  },
}

const SECOND_PATH_VARIANTS: Variants = {
  normal: { d: 'M12 19V5' },
  animate: {
    d: ['M12 19V5', 'M12 19V10', 'M12 19V5'],
    transition: {
      duration: 0.4,
    },
  },
}

// icon binds the arrow-up SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'arrow-up',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm5 12 7-7 7 7',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M12 19V5',
          variants: SECOND_PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
