// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/layers.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// layers.ts renders stacked layers that collapse toward the top layer and
// expand again. The two lower paths have different upward offsets, and
// useIconAnimation advances from firstState to secondState after the first
// spring movement completes.

// DEFAULT_TRANSITION supplies the spring used by both lower layers during the
// upward collapse target and the following target that restores their spacing.
const DEFAULT_TRANSITION: Transition = {
  type: 'spring',
  stiffness: 100,
  damping: 14,
  mass: 1,
}

// icon binds the layers SVG renderer to ['firstState', 'secondState'] for
// playback by useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'layers',
  normal: 'normal',
  animate: ['firstState', 'secondState'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'm12.83 2.18a2 2 0 0 0-1.66 0L2.6 6.08a1 1 0 0 0 0 1.83l8.58 3.91a2 2 0 0 0 1.66 0l8.58-3.9a1 1 0 0 0 0-1.83Z',
        },
        [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'm22 17.65-9.17 4.16a2 2 0 0 1-1.66 0L2 17.65',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { y: 0 },
            firstState: { y: -9 },
            secondState: { y: 0 },
          },
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'm22 12.65-9.17 4.16a2 2 0 0 1-1.66 0L2 12.65',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { y: 0 },
            firstState: { y: -5 },
            secondState: { y: 0 },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
