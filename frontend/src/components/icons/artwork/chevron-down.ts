// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chevron-down.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// chevron-down.ts renders a downward chevron with a short vertical bounce. Its
// three position keyframes return to the resting position, and the shared
// transition places the outward movement before the longer return segment of
// the animation.

// DEFAULT_TRANSITION places the downward bounce's middle keyframe before
// halfway, leaving more of the shared duration for the return movement.
const DEFAULT_TRANSITION: Transition = {
  times: [0, 0.4, 1],
  duration: 0.5,
}

// icon binds the chevron-down SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'chevron-down',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm6 9 6 6 6-6',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { y: 0 },
            animate: { y: [0, 2, 0] },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
