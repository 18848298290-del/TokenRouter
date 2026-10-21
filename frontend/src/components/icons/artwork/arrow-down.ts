// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/arrow-down.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// arrow-down.ts renders a downward arrow with a retracting shaft and moving
// arrowhead. The head moves upward as the shaft shortens, then both return
// through their own keyframes over the same duration.

// PATH_VARIANTS moves the downward arrowhead while SECOND_PATH_VARIANTS
// shortens its shaft with matching duration and return keyframes.
const PATH_VARIANTS: Variants = {
  normal: { d: 'm19 12-7 7-7-7', translateY: 0 },
  animate: {
    d: 'm19 12-7 7-7-7',
    translateY: [0, -3, 0],
    transition: {
      duration: 0.4,
    },
  },
}

const SECOND_PATH_VARIANTS: Variants = {
  normal: { d: 'M12 5v14' },
  animate: {
    d: ['M12 5v14', 'M12 5v9', 'M12 5v14'],
    transition: {
      duration: 0.4,
    },
  },
}

// icon binds the arrow-down SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'arrow-down',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm19 12-7 7-7-7',
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M12 5v14',
          variants: SECOND_PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
