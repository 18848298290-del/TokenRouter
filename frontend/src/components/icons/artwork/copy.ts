// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/copy.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// copy.ts renders overlapping document outlines that move toward one another.
// The front rectangle and rear path use opposite diagonal offsets with shared
// spring settings, allowing the normal target to restore their initial overlap
// after interaction ends.

// DEFAULT_TRANSITION supplies one spring response for the front rectangle and
// rear outline as their opposing diagonal offsets change the overlap.
const DEFAULT_TRANSITION: Transition = {
  type: 'spring',
  stiffness: 160,
  damping: 17,
  mass: 1,
}

// icon binds the copy SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'copy',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.rect,
        {
          animate: controls,
          height: '14',
          rx: '2',
          ry: '2',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateY: 0, translateX: 0 },
            animate: { translateY: -3, translateX: -3 },
          },
          width: '14',
          x: '8',
          y: '8',
          initial: 'normal',
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { x: 0, y: 0 },
            animate: { x: 3, y: 3 },
          },
          initial: 'normal',
        },
        () => [],
      ),
    ]),
}

export default icon
