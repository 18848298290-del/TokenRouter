// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/eye.tsx
import { h } from 'vue'
import { motion } from 'motion-v'
import type { IconDefinition } from '../types'

// eye.ts renders an eye that blinks through changes to its outline and pupil.
// The outline compresses vertically while the pupil scales in both directions,
// and matching durations bring both shapes back to their visible resting states
// together.

// icon binds the eye SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'eye',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'M2.062 12.348a1 1 0 0 1 0-.696 10.75 10.75 0 0 1 19.876 0 1 1 0 0 1 0 .696 10.75 10.75 0 0 1-19.876 0',
          style: { originY: '50%' },
          transition: { duration: 0.4, ease: 'easeInOut' },
          variants: {
            normal: { scaleY: 1, opacity: 1 },
            animate: { scaleY: [1, 0.1, 1], opacity: [1, 0.3, 1] },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.circle,
        {
          animate: controls,
          cx: '12',
          cy: '12',
          r: '3',
          transition: { duration: 0.4, ease: 'easeInOut' },
          variants: {
            normal: { scale: 1, opacity: 1 },
            animate: { scale: [1, 0.3, 1], opacity: [1, 0.3, 1] },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
