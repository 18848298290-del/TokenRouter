// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/play.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// play.ts renders a triangular play marker that tilts and moves horizontally.
// The polygon owns the motion target inside its group, with aligned keyframe
// times bringing its position and angle back to the starting pose at
// completion.

// PATH_VARIANTS aligns horizontal movement and rotation through shared keyframe
// times, returning the triangular marker to its starting pose at the final
// keyframe.
const PATH_VARIANTS: Variants = {
  normal: {
    x: 0,
    rotate: 0,
  },
  animate: {
    x: [0, -1, 2, 0],
    rotate: [0, -10, 0, 0],
    transition: {
      duration: 0.5,
      times: [0, 0.2, 0.5, 1],
      stiffness: 260,
      damping: 20,
    },
  },
}

// icon binds the play SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'play',
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
          motion.polygon,
          {
            animate: controls,
            points: '6 3 20 12 6 21 6 3',
            variants: PATH_VARIANTS,
            initial: 'normal',
          },
          () => [],
        ),
      ],
    ),
}

export default icon
