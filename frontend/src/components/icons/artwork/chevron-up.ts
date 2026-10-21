// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chevron-up.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// chevron-up.ts renders an upward chevron with a short vertical bounce. The
// path moves upward and returns to zero within one target, while the transition
// gives the return movement more time than the outward movement.

// DEFAULT_TRANSITION times the upward chevron's outward and return keyframes,
// with the final position reached at the end of playback.
const DEFAULT_TRANSITION: Transition = {
  times: [0, 0.4, 1],
  duration: 0.5,
}

// icon binds the chevron-up SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'chevron-up',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm18 15-6-6-6 6',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { y: 0 },
            animate: { y: [0, -2, 0] },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
