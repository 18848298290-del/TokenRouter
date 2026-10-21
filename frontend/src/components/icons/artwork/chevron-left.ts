// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chevron-left.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// chevron-left.ts renders a left chevron with a short horizontal bounce. The
// path moves toward the left and returns to zero, using uneven keyframe times
// to distinguish the outward step from the remainder of the motion.

// DEFAULT_TRANSITION divides the leftward bounce into a short outward step and
// a longer return through the three position keyframes.
const DEFAULT_TRANSITION: Transition = {
  times: [0, 0.4, 1],
  duration: 0.5,
}

// icon binds the chevron-left SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'chevron-left',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm15 18-6-6 6-6',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { x: 0 },
            animate: { x: [0, -2, 0] },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
