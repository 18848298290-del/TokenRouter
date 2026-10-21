// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/home.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition, IconVariants as Variants } from '../types'

// home.ts renders a stationary house outline with a doorway that redraws into
// view. The doorway uses a longer drawing transition than its opacity fade,
// keeping the roof and exterior walls visible while the entrance stroke is
// animated.

// DEFAULT_TRANSITION separates the doorway's fade and drawing durations, while
// PATH_VARIANTS supplies its fully visible resting pose and redraw keyframes.
const DEFAULT_TRANSITION: Transition = {
  duration: 0.6,
  opacity: { duration: 0.2 },
}

const PATH_VARIANTS: Variants = {
  normal: {
    pathLength: 1,
    opacity: 1,
  },
  animate: {
    opacity: [0, 1],
    pathLength: [0, 1],
  },
}

// icon binds the home SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'home',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        'path',
        {
          d: 'M3 10a2 2 0 0 1 .709-1.528l7-5.999a2 2 0 0 1 2.582 0l7 5.999A2 2 0 0 1 21 10v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
        },
        [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M15 21v-8a1 1 0 0 0-1-1h-4a1 1 0 0 0-1 1v8',
          transition: DEFAULT_TRANSITION,
          variants: PATH_VARIANTS,
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
