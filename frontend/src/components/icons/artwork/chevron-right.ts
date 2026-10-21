// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chevron-right.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// chevron-right.ts renders a right chevron with a short horizontal bounce. The
// path moves right and returns to its resting position, using the transition's
// shared keyframe times for the outward and return portions of the effect.

// DEFAULT_TRANSITION controls both segments of the rightward bounce, placing
// the intermediate position before the midpoint of the total duration.
const DEFAULT_TRANSITION: Transition = {
  times: [0, 0.4, 1],
  duration: 0.5,
}

// icon binds the chevron-right SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'chevron-right',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm9 18 6-6-6-6',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { x: 0 },
            animate: { x: [0, 2, 0] },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
