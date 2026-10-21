// Artwork and element animations adapted from Lucide Animated.
// See ../README.md for the license and pinned version.
// https://github.com/pqoqubbw/icons/blob/072c38b1b04ea738d90a084485ccaad4b890ddca/icons/chevrons-up-down.tsx
import { h } from 'vue'
import { motion, type Transition } from 'motion-v'
import type { IconDefinition } from '../types'

// chevrons-up-down.ts renders paired chevrons that separate vertically when
// animation starts. Both paths use the same spring settings with opposite
// offsets, and returning to the normal target brings the arrows back to their
// initial spacing.

// DEFAULT_TRANSITION supplies the shared spring for both chevrons so their
// opposite vertical offsets follow the same response during activation and
// reset.
const DEFAULT_TRANSITION: Transition = {
  type: 'spring',
  stiffness: 250,
  damping: 25,
}

// icon binds the chevrons-up-down SVG renderer to ['animate'] for playback by
// useIconAnimation through the shared controls.
const icon: IconDefinition = {
  name: 'chevrons-up-down',
  normal: 'normal',
  animate: ['animate'],
  render: (controls) =>
    h('g', {}, [
      h(
        motion.path,
        {
          animate: controls,
          d: 'm7 15 5 5 5-5',
          initial: 'normal',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateY: '0%' },
            animate: { translateY: '2px' },
          },
        },
        () => [],
      ),
      h(
        motion.path,
        {
          animate: controls,
          d: 'm7 9 5-5 5 5',
          initial: 'normal',
          transition: DEFAULT_TRANSITION,
          variants: {
            normal: { translateY: '0%' },
            animate: { translateY: '-2px' },
          },
        },
        () => [],
      ),
    ]),
}

export default icon
